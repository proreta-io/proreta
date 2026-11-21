package executor

import (
	"slices"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// lastCounters returns the slotCounters of the latest hello that carried them, and how many did.
func lastCounters(g *schedGateway) (c *wire.SlotCounters, n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.helloReqs {
		if r.SlotCounters != nil {
			c, n = r.SlotCounters, n+1
		}
	}
	return c, n
}

// started runs a harness and records misses by hand: the scheduler counts a busy slot only when
// the slow check returns, which is not what these tests are about.
func started(t *testing.T, gw *schedGateway) *harness {
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	return h
}

func advanceMinutes(h *harness, n int) {
	for range n {
		h.clk.Advance(time.Minute)
	}
}

// New executor, gateway of the previous protocol version (strict hello, offers schedule/1 only):
// it announces both capabilities, stays in schedule mode and never sends the counters.
func TestNewExecutorOldGatewayStaysInScheduleMode(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	gw.legacyHello = true
	h := started(t, gw)
	h.e.sched.n.missedBusy.Add(2)
	advanceMinutes(h, 5)
	if _, n := lastCounters(gw); n != 0 {
		t.Fatalf("%d hellos carried counters to a gateway that does not offer schedule/2", n)
	}
	if got := h.e.sched.n.missedBusy.Load(); got == 0 {
		t.Fatal("the scenario produced no missed slot")
	}
	if gw.claims.Load() != 0 || h.e.plainHello || h.e.pullOnly.Load() {
		t.Fatalf("left schedule mode: claims=%d plain=%t pullOnly=%t", gw.claims.Load(), h.e.plainHello, h.e.pullOnly.Load())
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if last := gw.helloReqs[len(gw.helloReqs)-1]; !slices.Contains(last.Capabilities, wire.CapSchedule) {
		t.Fatalf("the latest hello does not announce schedule/1: %+v", last)
	}
}

// New executor, new gateway: the first hello has no counters, the later ones carry cumulative values.
func TestNewExecutorNewGatewaySendsCumulativeCounters(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	gw.v2 = true
	h := started(t, gw)
	gw.mu.Lock()
	if first := gw.helloReqs[0]; first.SlotCounters != nil {
		t.Fatalf("the first hello carried counters: %+v", first)
	}
	gw.mu.Unlock()
	h.e.sched.n.missedBusy.Add(2)
	h.e.sched.n.missedExpired.Add(1)
	advanceMinutes(h, 4)
	c1, n1 := lastCounters(gw)
	if c1 == nil || c1.Missed.Busy != 2 || c1.Missed.ScheduleExpired != 1 {
		t.Fatalf("counters %+v after 4 minutes", c1)
	}
	h.e.sched.n.missedBusy.Add(3)
	advanceMinutes(h, 4)
	c2, n2 := lastCounters(gw)
	if n2 <= n1 || c2.Missed.Busy != 5 {
		t.Fatalf("not cumulative: %+v (%d hellos) then %+v (%d hellos)", c1, n1, c2, n2)
	}
	if c2.Dropped != (wire.DroppedCounters{}) {
		t.Fatalf("dropped %+v, nothing was dropped", c2.Dropped)
	}
}

// A gateway downgraded mid-run answers 400 to the counters: the executor resends a plain hello that
// still announces schedule/1, never changes mode, and stops sending the counters.
func TestMidRunDowngradeDropsCountersAndKeepsScheduleMode(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	gw.v2 = true
	h := started(t, gw)
	advanceMinutes(h, 3)
	if _, n := lastCounters(gw); n == 0 {
		t.Fatal("no counters before the downgrade")
	}
	gw.mu.Lock()
	gw.v2, gw.legacyHello = false, true
	gw.mu.Unlock()
	advanceMinutes(h, 4)
	gw.mu.Lock()
	last := gw.helloReqs[len(gw.helloReqs)-1]
	gw.mu.Unlock()
	if last.SlotCounters != nil || !slices.Contains(last.Capabilities, wire.CapSchedule) {
		t.Fatalf("the hello after the downgrade: %+v", last)
	}
	if h.e.plainHello || h.e.pullOnly.Load() || gw.claims.Load() != 0 {
		t.Fatalf("the downgrade changed the mode: plain=%t pullOnly=%t claims=%d", h.e.plainHello, h.e.pullOnly.Load(), gw.claims.Load())
	}
	if !h.e.noCounters {
		t.Fatal("the counters were not switched off for the run")
	}
	h.waitRunning(1)
}

// The counters belong to the process: after a restart they start at zero and the first hello has none.
func TestCountersStartAtZeroAfterARestart(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	gw.v2 = true
	h := started(t, gw)
	h.e.sched.n.missedBusy.Add(2)
	advanceMinutes(h, 4)
	if c, _ := lastCounters(gw); c == nil || c.Missed.Busy != 2 {
		t.Fatalf("counters %+v", c)
	}
	gw.mu.Lock()
	before := len(gw.helloReqs)
	gw.mu.Unlock()
	h.restart("")
	eventually(t, "a hello of the new process", func() bool { gw.mu.Lock(); defer gw.mu.Unlock(); return len(gw.helloReqs) > before })
	gw.mu.Lock()
	first := gw.helloReqs[before]
	gw.mu.Unlock()
	if first.SlotCounters != nil {
		t.Fatalf("the first hello of the new process carried counters: %+v", first)
	}
	if c := h.e.slotCounters(); c != (wire.SlotCounters{}) {
		t.Fatalf("counters after a restart: %+v", c)
	}
}

// The counters read the reporter's outcomes: rejected includes results the reporter gave up on.
func TestCountersMapGatewayOutcomes(t *testing.T) {
	e := New(Config{BaseURL: "http://unused", Token: "x"})
	e.rep.mu.Lock()
	e.rep.outcomes[wire.OutcomeRejected], e.rep.outcomes[wire.OutcomeSuperseded], e.rep.outcomes[wire.OutcomeExpired] = 1, 2, 3
	e.rep.outcomes[wire.OutcomeRecorded], e.rep.outcomes[wire.OutcomeDuplicate] = 100, 7
	e.rep.dropped = 4
	e.rep.mu.Unlock()
	want := wire.DroppedCounters{Rejected: 5, Superseded: 2, TooOld: 3}
	if got := e.slotCounters().Dropped; got != want {
		t.Fatalf("dropped %+v, want %+v", got, want)
	}
}
