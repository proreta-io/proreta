package executor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

// schedGateway is a fake gateway that offers schedule mode. down makes every call answer 503, like
// a partition seen from the executor.
type schedGateway struct {
	token string
	clk   *fakeClock
	skew  time.Duration // the executor's clock minus the server's

	mu          sync.Mutex
	sched       wire.Schedule
	modes       []string
	scheduleErr int // answer /schedule with this status when set
	helloReqs   []wire.HelloRequest
	ifNoneMatch []string
	validExtend time.Time
	version     string // announced in hello, empty = the schedule's
	down        atomic.Bool
	schedCalls  atomic.Int32
	claims      atomic.Int32
	oldHello    bool

	// /reports
	stored         map[string]wire.Report // monitor/slot -> report
	reportTimes    []time.Time
	batchSizes     []int
	reportCode     int              // answer /reports with this status
	retryAfter     int              // seconds, sent with reportCode
	maxBatch       int              // 413 above this
	poison         uuid.UUID        // 400 for a batch holding this monitor
	override       map[int64]string // outcome by slot, not stored
	commitThenFail int              // store the next n batches, then answer 503
	noReports      bool
}

func key(id uuid.UUID, slot int64) string { return id.String() + "/" + strconv.FormatInt(slot, 10) }

// storedSlots returns the stored slots of a monitor, ascending.
func (g *schedGateway) storedSlots(id uuid.UUID) []int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []int64
	for _, r := range g.stored {
		if r.MonitorID == id {
			out = append(out, r.Slot)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (g *schedGateway) handleReports(w http.ResponseWriter, r *http.Request) {
	if g.noReports {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	g.reportTimes = append(g.reportTimes, g.clk.Now())
	if g.reportCode != 0 {
		if g.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(g.retryAfter))
		}
		w.WriteHeader(g.reportCode)
		return
	}
	var in wire.ReportsRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.batchSizes = append(g.batchSizes, len(in.Results))
	if g.maxBatch > 0 && len(in.Results) > g.maxBatch {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if g.stored == nil {
		g.stored = map[string]wire.Report{}
	}
	var out wire.ReportsResponse
	out.ScheduleVersion = g.sched.Version
	for _, x := range in.Results {
		if x.MonitorID == g.poison {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	for _, x := range in.Results {
		oc := wire.OutcomeRecorded
		now := g.serverNow()
		switch {
		case g.override[x.Slot] != "":
			oc = g.override[x.Slot]
		case x.Slot > now.Add(30*time.Second).Unix():
			oc = wire.OutcomeRejected
		case x.Slot < now.Add(-24*time.Hour).Unix():
			oc = wire.OutcomeExpired
		default:
			if _, dup := g.stored[key(x.MonitorID, x.Slot)]; dup {
				oc = wire.OutcomeDuplicate
			} else {
				g.stored[key(x.MonitorID, x.Slot)] = x
			}
		}
		out.Outcomes = append(out.Outcomes, wire.ReportOutcome{MonitorID: x.MonitorID, Slot: x.Slot, Outcome: oc})
	}
	if g.commitThenFail > 0 { // committed, but the answer is lost
		g.commitThenFail--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (g *schedGateway) serverNow() time.Time { return g.clk.Now().Add(-g.skew) }

func (g *schedGateway) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.clk.busy.Add(1)
		defer g.clk.busy.Add(-1)
		if g.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+g.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case wire.PathHello:
			var in wire.HelloRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			g.helloReqs = append(g.helloReqs, in)
			if g.oldHello && len(in.Capabilities) > 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			out := wire.HelloResponse{ServerTime: g.serverNow(), Modes: g.modes, ScheduleVersion: g.version}
			_ = json.NewEncoder(w).Encode(out)
		case wire.PathSchedule:
			g.schedCalls.Add(1)
			inm := r.Header.Get("If-None-Match")
			g.ifNoneMatch = append(g.ifNoneMatch, inm)
			if g.scheduleErr != 0 {
				w.WriteHeader(g.scheduleErr)
				return
			}
			if inm == `"`+g.sched.Version+`"` {
				if !g.validExtend.IsZero() {
					w.Header().Set(wire.HeaderScheduleValidUntil, g.validExtend.Format(time.RFC3339))
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			s := g.sched
			s.ServerTime, s.IssuedAt = g.serverNow(), g.serverNow()
			w.Header().Set("ETag", `"`+s.Version+`"`)
			_ = json.NewEncoder(w).Encode(s)
		case wire.PathClaim:
			g.claims.Add(1)
			_ = json.NewEncoder(w).Encode(wire.ClaimResponse{Jobs: []wire.Job{}})
		case wire.PathReports:
			g.handleReports(w, r)
		case wire.PathResults:
			_ = json.NewEncoder(w).Encode(wire.ResultsResponse{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (g *schedGateway) set(fn func(*schedGateway)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fn(g)
}

func (g *schedGateway) schedCallCount() int { return int(g.schedCalls.Load()) }

func monitor(anchor int64, interval int) wire.ScheduledMonitor {
	return wire.ScheduledMonitor{
		MonitorID: uuid.New(), Epoch: 1, Active: true, Anchor: anchor, IntervalSeconds: interval, TimeoutSeconds: 10,
		URL: "https://svc.internal/health", Method: "GET", ExpectedStatus: "2xx",
	}
}

func newGateway(clk *fakeClock, ms ...wire.ScheduledMonitor) *schedGateway {
	return &schedGateway{
		token: "spc_t", clk: clk, modes: []string{wire.ModePull, wire.ModeSchedule},
		sched: wire.Schedule{Version: "v1", ValidUntil: clk.Now().Add(24 * time.Hour), RefreshAfterSeconds: 300, Monitors: ms},
	}
}

type testProber struct {
	mu    sync.Mutex
	calls int
	block chan struct{} // when set, a probe waits for it
	fail  func(checks.Request) bool
}

func (p *testProber) Probe(ctx context.Context, r checks.Request) checks.Result {
	p.mu.Lock()
	p.calls++
	block, fail := p.block, p.fail
	p.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return checks.Result{Cancelled: true}
		}
	}
	if fail != nil && fail(r) {
		return checks.Result{ErrorKind: checks.KindConnect}
	}
	return checks.Result{OK: true, StatusCode: 200, LatencyMs: 5}
}

type harness struct {
	t    *testing.T
	clk  *fakeClock
	gw   *schedGateway
	srv  *httptest.Server
	prob *testProber
	log  *slog.Logger
	e    *Executor
	stop func() error
}

func newHarness(t *testing.T, gw *schedGateway, stateDir string) *harness {
	t.Helper()
	h := &harness{t: t, clk: gw.clk, gw: gw, prob: &testProber{}}
	h.srv = httptest.NewServer(gw.handler())
	t.Cleanup(h.srv.Close)
	h.start(stateDir)
	return h
}

func (h *harness) start(stateDir string) {
	h.e = New(Config{
		BaseURL: h.srv.URL, Token: h.gw.token, Version: "test", Hostname: "pod", Prober: h.prob, Clock: h.clk, StateDir: stateDir, Log: h.log,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.e.Run(ctx) }()
	var once sync.Once
	var stopErr error
	h.stop = func() error { cancel(); once.Do(func() { stopErr = <-done }); return stopErr }
	h.t.Cleanup(func() { _ = h.stop() })
}

func (h *harness) restart(stateDir string) {
	h.t.Helper()
	if err := h.stop(); err != nil {
		h.t.Fatal(err)
	}
	h.start(stateDir)
}

// slots returns the slots produced for a monitor, sent or still buffered, ascending.
func (h *harness) slots(id uuid.UUID) []int64 {
	seen := map[int64]bool{}
	for _, s := range h.gw.storedSlots(id) {
		seen[s] = true
	}
	for _, r := range h.e.buf.Peek(1 << 30).Reports {
		if r.MonitorID == id {
			seen[r.Slot] = true
		}
	}
	out := make([]int64, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (h *harness) allReports() []wire.Report {
	h.gw.mu.Lock()
	var out []wire.Report
	for _, r := range h.gw.stored {
		out = append(out, r)
	}
	h.gw.mu.Unlock()
	out = append(out, h.e.buf.Peek(1<<30).Reports...)
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

func (h *harness) firstReport() wire.Report { return h.allReports()[0] }
func (h *harness) lastReport() wire.Report {
	all := h.allReports()
	return all[len(all)-1]
}

func (h *harness) waitRunning(n int) {
	h.t.Helper()
	eventually(h.t, "runners", func() bool {
		h.e.sched.mu.Lock()
		defer h.e.sched.mu.Unlock()
		return len(h.e.sched.runners) == n
	})
}

func eq(a []int64, b ...int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSlotAtOrAfter(t *testing.T) {
	a := int64(1000)
	for _, tc := range []struct {
		now  int64
		want int64
	}{{900, 1000}, {1000, 1000}, {1001, 1060}, {1060, 1060}, {1119, 1120}, {1120, 1120}} {
		if got := slotAtOrAfter(a, 60, time.Unix(tc.now, 0)); got != tc.want {
			t.Errorf("now %d: %d, want %d", tc.now, got, tc.want)
		}
	}
	if got := slotAtOrAfter(a, 60, time.Unix(1000, 1)); got != 1060 {
		t.Errorf("fraction: %d", got)
	}
}

func TestChecksRunAtTheSlotsOfTheSchedule(t *testing.T) {
	clk := newFakeClock(t0)
	m1, m2 := monitor(t0.Unix()-3600+17, 60), monitor(t0.Unix()-3600+41, 120)
	h := newHarness(t, newGateway(clk, m1, m2), "")
	h.waitRunning(2)
	clk.Advance(5 * time.Minute)
	// Slots are anchor + n*interval whatever the time the check really ran at.
	s1 := t0.Unix() + 17
	if got := h.slots(m1.MonitorID); !eq(got, s1, s1+60, s1+120, s1+180, s1+240) {
		t.Fatalf("m1 slots %v", got)
	}
	s2 := t0.Unix() + 41
	if got := h.slots(m2.MonitorID); !eq(got, s2, s2+120, s2+240) {
		t.Fatalf("m2 slots %v", got)
	}
	if r := h.firstReport(); !r.OK || r.Epoch != 1 || r.StatusCode != 200 {
		t.Fatalf("report %+v", r)
	}
}

func TestAnnouncesCapabilitiesAndInstance(t *testing.T) {
	clk := newFakeClock(t0)
	h := newHarness(t, newGateway(clk, monitor(t0.Unix(), 60)), "")
	h.waitRunning(1)
	h.gw.mu.Lock()
	defer h.gw.mu.Unlock()
	first := h.gw.helloReqs[0]
	if len(first.Capabilities) != 2 || first.Capabilities[0] != wire.CapTCP || first.Capabilities[1] != wire.CapSchedule || first.InstanceID == "" {
		t.Fatalf("hello %+v", first)
	}
}

func TestSecondFetchSendsTheETagAndAnswers304(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	ext := t0.Add(48 * time.Hour)
	gw.set(func(g *schedGateway) { g.validExtend = ext })
	clk.Advance(6 * time.Minute) // past refreshAfterSeconds
	if gw.schedCallCount() < 2 {
		t.Fatal("no refresh")
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if gw.ifNoneMatch[0] != "" || gw.ifNoneMatch[1] != `"v1"` {
		t.Fatalf("If-None-Match %q", gw.ifNoneMatch)
	}
	h.e.sched.mu.Lock()
	defer h.e.sched.mu.Unlock()
	if !h.e.sched.validUntil.Equal(ext) || len(h.e.sched.runners) != 1 {
		t.Fatalf("validUntil %v runners %d", h.e.sched.validUntil, len(h.e.sched.runners))
	}
}

func TestRefusesAScheduleOverTheLimitAndKeepsTheOldOne(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	big := make([]wire.ScheduledMonitor, wire.MaxSchedule+1)
	for i := range big {
		big[i] = monitor(t0.Unix(), 60)
	}
	gw.set(func(g *schedGateway) { g.sched.Version, g.sched.Monitors = "v2", big })
	err := h.e.sched.refresh(context.Background())
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if h.e.sched.version() != "v1" {
		t.Fatal("the refused schedule replaced the one in use")
	}
	h.waitRunning(1)
	// 413 from the gateway is the same.
	gw.set(func(g *schedGateway) { g.scheduleErr = http.StatusRequestEntityTooLarge })
	if err := h.e.sched.refresh(context.Background()); !errors.Is(err, errTooLarge) {
		t.Fatalf("413: %v", err)
	}
}

func TestChecksStopAtValidUntilAndResumeOnRenewal(t *testing.T) {
	clk := newFakeClock(t0)
	m := monitor(t0.Unix(), 60)
	gw := newGateway(clk, m)
	gw.sched.ValidUntil = t0.Add(3*time.Minute + 30*time.Second)
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	gw.down.Store(true) // the link drops; the schedule cannot be renewed
	clk.Advance(10 * time.Minute)
	got := h.slots(m.MonitorID)
	// Slots at +0,+1,+2,+3 minutes are before validUntil; nothing after.
	if !eq(got, t0.Unix(), t0.Unix()+60, t0.Unix()+120, t0.Unix()+180) {
		t.Fatalf("slots %v", got)
	}
	gw.down.Store(false)
	gw.set(func(g *schedGateway) { g.sched.Version, g.sched.ValidUntil = "v2", clk.Now().Add(time.Hour) })
	clk.Advance(2 * time.Minute) // the refresh backoff is at most a minute
	if got := h.slots(m.MonitorID); len(got) < 5 || got[len(got)-1] <= t0.Unix()+600 {
		t.Fatalf("no checks after the renewal: %v", got)
	}
}

func TestRestartWithoutAConnectionRunsFromTheCache(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(t0)
	m := monitor(t0.Unix(), 60)
	gw := newGateway(clk, m)
	h := newHarness(t, gw, dir)
	h.waitRunning(1)
	clk.Advance(2*time.Minute + time.Second)
	if got := h.slots(m.MonitorID); len(got) != 3 {
		t.Fatalf("before the restart: %v", got)
	}
	gw.down.Store(true)
	h.restart(dir)
	h.waitRunning(1)
	clk.Advance(3 * time.Minute)
	// The three results of the first run were buffered on disk and survive; the new run goes on.
	got := h.slots(m.MonitorID)
	base := t0.Unix()
	if !eq(got, base, base+60, base+120, base+180, base+240, base+300) {
		t.Fatalf("slots after the restart %v", got)
	}
}

func TestHelloReportsADegradedBuffer(t *testing.T) {
	clk := newFakeClock(t0)
	m := monitor(t0.Unix(), 60)
	gw := newGateway(clk, m)
	h := newHarness(t, gw, "/proc/nonexistent/specula")
	h.waitRunning(1)
	clk.Advance(2*time.Minute + 5*time.Second)
	gw.mu.Lock()
	defer gw.mu.Unlock()
	last := gw.helloReqs[len(gw.helloReqs)-1]
	if last.BufferDegraded == nil || !*last.BufferDegraded || last.BufferDepth == nil {
		t.Fatalf("hello %+v", last)
	}
}

func TestACorruptCacheIsIgnored(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	gw.down.Store(true)
	if err := newStore(dir).writeAtomic(scheduleFile, []byte("{nope")); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, gw, dir)
	clk.Advance(time.Minute)
	h.e.sched.mu.Lock()
	defer h.e.sched.mu.Unlock()
	if len(h.e.sched.runners) != 0 {
		t.Fatal("a corrupt cache started checks")
	}
}

func TestFallsBackToPullModeOnAnOlderGateway(t *testing.T) {
	for name, set := range map[string]func(*schedGateway){
		"no /schedule":       func(g *schedGateway) { g.scheduleErr = http.StatusNotFound },
		"hello not accepted": func(g *schedGateway) { g.oldHello = true },
		"no mode offered":    func(g *schedGateway) { g.modes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			clk := newFakeClock(t0)
			gw := newGateway(clk, monitor(t0.Unix(), 60))
			set(gw)
			h := newHarness(t, gw, "")
			eventually(t, "claims", func() bool { return gw.claims.Load() > 0 })
			h.e.sched.mu.Lock()
			n := len(h.e.sched.runners)
			h.e.sched.mu.Unlock()
			if n != 0 {
				t.Fatal("scheduled checks in pull mode")
			}
			if name == "no /schedule" {
				// Said hello again without schedule/1, so the server hands out jobs.
				eventually(t, "a hello without schedule/1", func() bool {
					gw.mu.Lock()
					defer gw.mu.Unlock()
					last := gw.helloReqs[len(gw.helloReqs)-1]
					return len(last.Capabilities) == 1 && last.Capabilities[0] == wire.CapTCP
				})
			}
		})
	}
}

func TestAnnouncedVersionTriggersARefresh(t *testing.T) {
	clk := newFakeClock(t0)
	m := monitor(t0.Unix(), 60)
	gw := newGateway(clk, m)
	h := newHarness(t, gw, "")
	h.waitRunning(1)
	calls := gw.schedCallCount()
	m2 := m
	m2.Epoch = 2
	gw.set(func(g *schedGateway) {
		g.sched.Version, g.sched.Monitors, g.version = "v2", []wire.ScheduledMonitor{m2}, "v2"
	})
	clk.Advance(time.Minute) // the next hello announces v2; no waiting for the 5 minute refresh
	if gw.schedCallCount() <= calls || h.e.sched.version() != "v2" {
		t.Fatalf("calls %d version %q", gw.schedCallCount(), h.e.sched.version())
	}
	clk.Advance(2 * time.Minute)
	if last := h.lastReport(); last.Epoch != 2 {
		t.Fatalf("the new epoch is not used: %+v", last)
	}
}

func TestPausedAndChangedMonitorsStopTheirRunner(t *testing.T) {
	clk := newFakeClock(t0)
	a, b := monitor(t0.Unix(), 60), monitor(t0.Unix(), 60)
	gw := newGateway(clk, a, b)
	h := newHarness(t, gw, "")
	h.waitRunning(2)
	a.Active = false
	gw.set(func(g *schedGateway) {
		g.sched.Version, g.sched.Monitors = "v2", []wire.ScheduledMonitor{a, b}
		g.version = "v2"
	})
	clk.Advance(2 * time.Minute)
	h.waitRunning(1)
	before := len(h.slots(a.MonitorID))
	clk.Advance(3 * time.Minute)
	if after := len(h.slots(a.MonitorID)); after != before {
		t.Fatalf("a paused monitor was checked: %d -> %d", before, after)
	}
}

func TestSkipsSlotsOfACheckThatOutlastsItsInterval(t *testing.T) {
	clk := newFakeClock(t0)
	m := monitor(t0.Unix(), 60)
	gw := newGateway(clk, m)
	h := newHarness(t, gw, "")
	h.prob.mu.Lock()
	h.prob.block = make(chan struct{})
	h.prob.mu.Unlock()
	h.waitRunning(1)
	clk.Advance(150 * time.Second) // the check of slot 0 is still running at slots 60 and 120
	close(h.prob.block)
	eventually(t, "the late check", func() bool { return len(h.slots(m.MonitorID)) == 1 })
	h.prob.mu.Lock()
	h.prob.block = nil
	h.prob.mu.Unlock()
	clk.Advance(2 * time.Minute)
	if got := h.e.sched.n.missedBusy.Load(); got != 2 {
		t.Fatalf("missed busy %d, want 2", got)
	}
	if got := h.slots(m.MonitorID); got[0] != t0.Unix() || got[1] != t0.Unix()+180 {
		t.Fatalf("slots %v", got)
	}
}

func TestSlotsFollowTheServersClockNotAWrongLocalOne(t *testing.T) {
	for _, skew := range []time.Duration{2 * time.Minute, -10 * time.Minute} {
		t.Run(skew.String(), func(t *testing.T) {
			clk := newFakeClock(t0)
			// The executor's clock is skew ahead of the server's: the server's time is t0 - skew.
			serverT0 := t0.Add(-skew)
			m := monitor(serverT0.Unix()-600+7, 60)
			gw := newGateway(clk, m)
			gw.skew = skew
			gw.sched.ValidUntil = serverT0.Add(24 * time.Hour)
			h := newHarness(t, gw, "")
			h.waitRunning(1)
			clk.Advance(3 * time.Minute)
			got := h.slots(m.MonitorID)
			first := serverT0.Unix() + 7
			if !eq(got, first, first+60, first+120) {
				t.Fatalf("slots %v, want from %d", got, first)
			}
			if off := h.e.clk.Offset(); off < -skew-time.Second || off > -skew+time.Second {
				t.Fatalf("offset %v, want %v", off, -skew)
			}
		})
	}
}

func TestSavedOffsetIsUsedUntilTheGatewayAnswers(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(t0)
	skew := 5 * time.Minute
	serverT0 := t0.Add(-skew)
	m := monitor(serverT0.Unix(), 60)
	gw := newGateway(clk, m)
	gw.skew = skew
	gw.sched.ValidUntil = serverT0.Add(24 * time.Hour)
	h := newHarness(t, gw, dir)
	h.waitRunning(1)
	gw.down.Store(true)
	h.restart(dir)
	h.waitRunning(1)
	clk.Advance(2 * time.Minute)
	for _, s := range h.slots(m.MonitorID) {
		if s < serverT0.Unix() {
			t.Fatalf("slot %d is before the server's time %d", s, serverT0.Unix())
		}
		if s > serverT0.Unix()+130 {
			t.Fatalf("slot %d is far ahead of the server's time", s)
		}
	}
	if len(h.slots(m.MonitorID)) == 0 {
		t.Fatal("no slots")
	}
}

func TestServerClockMath(t *testing.T) {
	local := newFakeClock(t0)
	c := &serverClock{local: local}
	if !c.Now().Equal(t0) || c.Offset() != 0 {
		t.Fatal("uncorrected clock")
	}
	sent := t0
	local.mu.Lock()
	local.now = t0.Add(200 * time.Millisecond)
	local.mu.Unlock()
	c.Observe(t0.Add(time.Hour), sent, local.Now()) // 100 ms of the round trip each way
	if off := c.Offset(); off != time.Hour-100*time.Millisecond {
		t.Fatalf("offset %v", off)
	}
	local.Advance(time.Minute)
	if got, want := c.Now(), t0.Add(time.Hour+200*time.Millisecond+time.Minute-200*time.Millisecond+100*time.Millisecond); !got.Equal(want) {
		t.Fatalf("now %v want %v", got, want)
	}
	d := &serverClock{local: local}
	d.Restore(-time.Minute)
	if d.Offset() != -time.Minute {
		t.Fatalf("restored %v", d.Offset())
	}
}

func TestRunningWithAnUnusableStateDirStillChecks(t *testing.T) {
	clk := newFakeClock(t0)
	m := monitor(t0.Unix(), 60)
	h := newHarness(t, newGateway(clk, m), "/proc/nonexistent/specula")
	h.waitRunning(1)
	clk.Advance(3 * time.Minute)
	if got := h.slots(m.MonitorID); len(got) != 4 {
		t.Fatalf("slots %v", got)
	}
}

func TestStateFilesArePrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	if err := s.writeAtomic("x.json", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := s.writeAtomic("x.json", []byte("22")); err != nil {
		t.Fatal(err)
	}
	b, _ := s.readFile("x.json")
	if string(b) != "22" {
		t.Fatalf("content %q", b)
	}
	if b, err := s.readFile("missing"); b != nil || err != nil {
		t.Fatalf("missing: %v %v", b, err)
	}
	st, err := os.Stat(filepath.Join(dir, "x.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st, err)
	}
}
