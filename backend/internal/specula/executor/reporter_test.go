package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// past is a slot time safely inside the gateway's window.
var past = t0.Unix() - 3600

// reportEnv runs only the reporter against a pre-filled buffer.
type reportEnv struct {
	t        *testing.T
	clk      *fakeClock
	gw       *schedGateway
	e        *Executor
	rejected chan error
	stop     func()
}

func newReportEnv(t *testing.T, gw *schedGateway, memMax int) *reportEnv {
	t.Helper()
	srv := httptest.NewServer(gw.handler())
	t.Cleanup(srv.Close)
	e := New(Config{BaseURL: srv.URL, Token: gw.token, Version: "test", Clock: gw.clk})
	e.buf = openBuffer(bufferConfig{MemoryMax: memMax}, newStore(""), e.cfg.Log, e.now)
	env := &reportEnv{t: t, clk: gw.clk, gw: gw, e: e, rejected: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.rep.loop(ctx, env.rejected); close(done) }()
	env.stop = func() { cancel(); <-done }
	t.Cleanup(env.stop)
	return env
}

func (r *reportEnv) fill(id uuid.UUID, first int64, n int, ok bool) {
	for i := range int64(n) {
		x := rep(first + i)
		x.MonitorID, x.OK = id, ok
		r.e.buf.Append(x)
	}
}

func TestAFailureIsSentAtOnce(t *testing.T) {
	clk := newFakeClock(t0)
	bad := monitor(t0.Unix()+35, 60)
	gw := newGateway(clk, bad)
	h := newHarness(t, gw, "")
	h.prob.mu.Lock()
	h.prob.fail = func(checks.Request) bool { return true }
	h.prob.mu.Unlock()
	h.waitRunning(1)
	clk.Advance(36 * time.Second) // the slot at +35 s, between two batch times
	if got := gw.storedSlots(bad.MonitorID); len(got) != 1 || got[0] != t0.Unix()+35 {
		t.Fatalf("failure not sent at once: %v", got)
	}
}

func TestSuccessesWaitForTheBatchTime(t *testing.T) {
	clk := newFakeClock(t0)
	good := monitor(t0.Unix()+35, 60)
	gw := newGateway(clk, good)
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	clk.Advance(36 * time.Second)
	if got := gw.storedSlots(good.MonitorID); len(got) != 0 {
		t.Fatalf("a success was sent before its batch time: %v", got)
	}
	clk.Advance(10 * time.Second)
	if got := gw.storedSlots(good.MonitorID); len(got) != 1 {
		t.Fatalf("the success was not sent with the next batch: %v", got)
	}
}

func partition(t *testing.T, skew time.Duration, outage, total time.Duration, monitors int) {
	t.Helper()
	clk := newFakeClock(t0)
	serverT0 := t0.Add(-skew)
	var ms []wire.ScheduledMonitor
	for i := range monitors {
		ms = append(ms, monitor(serverT0.Unix()-1000+int64(i*7), 60))
	}
	gw := newGateway(clk, ms...)
	gw.skew = skew
	gw.sched.ValidUntil = serverT0.Add(48 * time.Hour)
	h := newHarness(t, gw, t.TempDir())
	h.waitRunning(monitors)
	clk.Advance(5 * time.Minute)
	gw.down.Store(true)
	clk.Advance(outage)
	gw.down.Store(false)
	clk.Advance(total - 5*time.Minute - outage)
	// The buffer may hold the newest successes until the next batch time; give it that.
	clk.Advance(30 * time.Second)
	for _, m := range ms {
		got := gw.storedSlots(m.MonitorID)
		if len(got) < int(total/time.Minute)-1 {
			t.Fatalf("only %d slots stored, want about %d", len(got), total/time.Minute)
		}
		for i := 1; i < len(got); i++ {
			if got[i]-got[i-1] != 60 {
				t.Fatalf("a gap or a duplicate between %d and %d", got[i-1], got[i])
			}
		}
		if want := slotAtOrAfter(m.Anchor, 60, serverT0); got[0] != want {
			t.Fatalf("first slot %d, want %d", got[0], want)
		}
	}
	if d := h.e.buf.Depth(); d > monitors*2 {
		t.Fatalf("%d results still buffered", d)
	}
	if st := h.e.rep.Stats(); st.Outcomes[wire.OutcomeRejected] != 0 || st.Outcomes[wire.OutcomeExpired] != 0 {
		t.Fatalf("outcomes %v", st.Outcomes)
	}
}

func TestAnEightMinutePartitionLeavesNoGap(t *testing.T) {
	partition(t, 0, 8*time.Minute, 20*time.Minute, 5)
}
func TestAnEightMinutePartitionWithASkewedClock(t *testing.T) {
	partition(t, 2*time.Minute, 8*time.Minute, 20*time.Minute, 3)
}
func TestATwoHourPartitionLeavesNoGap(t *testing.T) {
	partition(t, 0, 2*time.Hour, 2*time.Hour+10*time.Minute, 3)
}

func TestACrashBetweenCommitAndAckSendsTheBatchAgainAsDuplicates(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(t0)
	m := monitor(t0.Unix()+35, 60)
	gw := newGateway(clk, m)
	h := newHarness(t, gw, dir)
	h.prob.mu.Lock()
	h.prob.fail = func(checks.Request) bool { return true } // failures are sent at once
	h.prob.mu.Unlock()
	h.waitRunning(1)
	gw.set(func(g *schedGateway) { g.commitThenFail = 1 })
	clk.Advance(36 * time.Second)
	if got := gw.storedSlots(m.MonitorID); len(got) != 1 {
		t.Fatalf("the gateway has %v", got)
	}
	// The answer was lost: the result is still buffered. The process dies before it retries.
	gw.down.Store(true)
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	gw.down.Store(false)
	h.start(dir)
	h.waitRunning(1)
	clk.Advance(5 * time.Second)
	if got := gw.storedSlots(m.MonitorID); len(got) < 1 || got[0] != t0.Unix()+35 {
		t.Fatalf("stored %v", got)
	}
	eventually(t, "the batch to be answered", func() bool { return h.e.rep.Stats().Outcomes[wire.OutcomeDuplicate] >= 1 })
	if n := len(gw.storedSlots(m.MonitorID)); n != 1 && n != 2 {
		t.Fatalf("%d rows", n)
	}
	if st := h.e.rep.Stats(); st.Outcomes[wire.OutcomeRecorded] > 1 {
		t.Fatalf("outcomes %v", st.Outcomes)
	}
}

func TestFinalOutcomesAreAcknowledgedAndNeverSentAgain(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	gw.override = map[int64]string{
		past + 1: wire.OutcomeSuperseded, past + 2: wire.OutcomeExpired, past + 3: wire.OutcomeRejected,
		past + 4: wire.OutcomeDuplicate,
	}
	env := newReportEnv(t, gw, 100)
	env.fill(uuid.New(), past, 8, true)
	env.e.rep.poke()
	clk.Advance(time.Second)
	eventually(t, "an empty buffer", func() bool { return env.e.buf.Depth() == 0 })
	st := env.e.rep.Stats()
	if st.Outcomes[wire.OutcomeSuperseded] != 1 || st.Outcomes[wire.OutcomeExpired] != 1 || st.Outcomes[wire.OutcomeRejected] != 1 ||
		st.Outcomes[wire.OutcomeDuplicate] != 1 || st.Outcomes[wire.OutcomeRecorded] != 4 {
		t.Fatalf("outcomes %v", st.Outcomes)
	}
	calls := len(gw.reportTimes)
	clk.Advance(time.Minute)
	if len(gw.reportTimes) != calls {
		t.Fatal("something was sent again")
	}
}

func TestRetryAfterIsHonouredAndNothingIsLost(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		clk := newFakeClock(t0)
		gw := newGateway(clk)
		gw.reportCode, gw.retryAfter = code, 40
		env := newReportEnv(t, gw, 100)
		env.fill(uuid.New(), past, 5, false)
		env.e.rep.poke()
		clk.Advance(time.Second)
		gw.set(func(g *schedGateway) { g.reportCode = 0 })
		clk.Advance(35 * time.Second)
		if env.e.buf.Depth() != 5 {
			t.Fatalf("%d: sent before Retry-After passed", code)
		}
		clk.Advance(10 * time.Second)
		eventually(t, "delivery after Retry-After", func() bool { return env.e.buf.Depth() == 0 })
		env.stop()
	}
}

func TestBackoffGrowsWhileTheGatewayStaysDown(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	gw.reportCode = http.StatusServiceUnavailable
	env := newReportEnv(t, gw, 100)
	env.fill(uuid.New(), past, 3, false)
	env.e.rep.poke()
	clk.Advance(30 * time.Minute)
	gw.mu.Lock()
	n := len(gw.reportTimes)
	gw.mu.Unlock()
	// Waits are jittered between half and all of 2s..60s (ending at the cap), so the count is
	// between 35 and 64 whatever the jitter draws; the mean sits near 45, hence no tighter bound.
	if n < 35 || n > 64 {
		t.Fatalf("%d calls in 30 minutes", n)
	}
	if env.e.buf.Depth() != 3 {
		t.Fatal("results were lost")
	}
}

func TestATooLargeBatchIsSplit(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	gw.maxBatch = 10
	env := newReportEnv(t, gw, 500)
	id := uuid.New()
	env.fill(id, past, 95, true)
	env.e.rep.poke()
	clk.Advance(30 * time.Second)
	eventually(t, "delivery", func() bool { return env.e.buf.Depth() == 0 })
	if got := gw.storedSlots(id); !eq(got, seq(past, 95)...) {
		t.Fatalf("stored %d", len(got))
	}
}

func TestABadResultIsDroppedAndTheRestDelivered(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	bad, good := uuid.New(), uuid.New()
	gw.poison = bad
	env := newReportEnv(t, gw, 500)
	env.fill(good, past, 20, true)
	env.fill(bad, past+100, 1, true)
	env.fill(good, past+200, 20, true)
	env.e.rep.poke()
	clk.Advance(30 * time.Second)
	eventually(t, "delivery", func() bool { return env.e.buf.Depth() == 0 })
	if n := len(gw.storedSlots(good)); n != 40 {
		t.Fatalf("stored %d", n)
	}
	if env.e.rep.Stats().Dropped != 1 {
		t.Fatalf("dropped %d", env.e.rep.Stats().Dropped)
	}
}

func TestReplayRunsAtMostFiveBatchesPerSecond(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	env := newReportEnv(t, gw, 5000)
	id := uuid.New()
	env.fill(id, past, 2000, true)
	env.e.rep.poke()
	for i := 0; i < 100 && env.e.buf.Depth() > 0; i++ { // the clock may outrun a slow round trip
		clk.Advance(time.Second)
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	times := gw.reportTimes
	if len(times) != 20 {
		t.Fatalf("%d batches for 2000 results", len(times))
	}
	for i := 5; i < len(times); i++ {
		if times[i].Sub(times[i-5]) < time.Second {
			t.Fatalf("6 batches within %v", times[i].Sub(times[i-5]))
		}
	}
	for _, n := range gw.batchSizes {
		if n > wire.MaxResults {
			t.Fatalf("batch of %d", n)
		}
	}
}

func TestAnOlderGatewayWithoutReportsKeepsTheBuffer(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	gw.noReports = true
	env := newReportEnv(t, gw, 100)
	env.fill(uuid.New(), past, 5, false)
	env.e.rep.poke()
	clk.Advance(time.Minute)
	eventually(t, "the reporter to stop", func() bool {
		env.e.rep.mu.Lock()
		defer env.e.rep.mu.Unlock()
		return env.e.rep.stopped
	})
	if env.e.buf.Depth() != 5 {
		t.Fatal("results were dropped")
	}
}

func TestARejectedTokenStopsTheReporter(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk)
	env := newReportEnv(t, gw, 100)
	gw.set(func(g *schedGateway) { g.token = "spc_other" })
	env.fill(uuid.New(), past, 2, false)
	env.e.rep.poke()
	clk.Advance(time.Second)
	select {
	case err := <-env.rejected:
		if err != ErrRejected {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not rejected")
	}
}

func TestShutdownSendsWhatIsBufferedOnce(t *testing.T) {
	clk := newFakeClock(t0)
	m := monitor(t0.Unix()+35, 60)
	gw := newGateway(clk, m)
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	clk.Advance(36 * time.Second) // the success of slot +35 s waits for its batch time
	if len(gw.storedSlots(m.MonitorID)) != 0 {
		t.Fatal("sent too early")
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if got := gw.storedSlots(m.MonitorID); len(got) != 1 {
		t.Fatalf("the shutdown did not flush: %v", got)
	}
}
