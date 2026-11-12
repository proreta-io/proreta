package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

type fakeGateway struct {
	t        *testing.T
	token    string
	mu       sync.Mutex
	jobs     []wire.Job
	results  []wire.Result
	hellos   atomic.Int32
	failNext atomic.Int32 // answer /results with 503 this many times
}

func (g *fakeGateway) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+g.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case wire.BasePath + "/hello":
			g.hellos.Add(1)
			_ = json.NewEncoder(w).Encode(wire.HelloResponse{ServerTime: time.Now().UTC()})
		case wire.BasePath + "/claim":
			g.mu.Lock()
			jobs := g.jobs
			g.jobs = nil
			g.mu.Unlock()
			_ = json.NewEncoder(w).Encode(wire.ClaimResponse{Jobs: append([]wire.Job{}, jobs...)})
		case wire.BasePath + "/results":
			if g.failNext.Add(-1) >= 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var in wire.ResultsRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			g.mu.Lock()
			g.results = append(g.results, in.Results...)
			g.mu.Unlock()
			var out wire.ResultsResponse
			for _, x := range in.Results {
				out.Outcomes = append(out.Outcomes, wire.ResultOutcome{MonitorID: x.MonitorID, Outcome: wire.OutcomeRecorded})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (g *fakeGateway) got() []wire.Result {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]wire.Result(nil), g.results...)
}

type proberFunc func(context.Context, checks.Request) checks.Result

func (f proberFunc) Probe(ctx context.Context, r checks.Request) checks.Result { return f(ctx, r) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newJob() wire.Job {
	return wire.Job{
		MonitorID: uuid.New(), LeaseToken: uuid.New(), LeaseUntil: time.Now().Add(30 * time.Second), URL: "http://svc.internal:8080/health",
		Method: "POST", Headers: []wire.Header{{Name: "Authorization", Value: "Bearer x"}}, Body: `{"a":1}`, ExpectedStatus: "2xx", TimeoutSeconds: 7,
	}
}

func start(t *testing.T, g *fakeGateway, p checks.Prober) (cancel func() error) {
	t.Helper()
	srv := httptest.NewServer(g.handler())
	t.Cleanup(srv.Close)
	e := New(Config{BaseURL: srv.URL, Token: g.token, Version: "test", Hostname: "pod", Concurrency: 2, PollInterval: 20 * time.Millisecond, Prober: p})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() { runErr = e.Run(ctx); close(done) }()
	finish := func() error { stop(); <-done; return runErr }
	t.Cleanup(func() { _ = finish() })
	return finish
}

func TestRunsClaimedJobsAndReports(t *testing.T) {
	g := &fakeGateway{t: t, token: "spc_t"}
	job := newJob()
	g.jobs = []wire.Job{job}
	var seen checks.Request
	var seenMu sync.Mutex
	stopExec := start(t, g, proberFunc(func(_ context.Context, r checks.Request) checks.Result {
		seenMu.Lock()
		seen = r
		seenMu.Unlock()
		return checks.Result{OK: true, StatusCode: 200, LatencyMs: 12, CheckedAt: time.Now().UTC()}
	}))
	eventually(t, "the result", func() bool { return len(g.got()) == 1 })
	got := g.got()[0]
	if got.MonitorID != job.MonitorID || got.LeaseToken != job.LeaseToken || !got.OK || got.StatusCode != 200 || got.LatencyMs != 12 {
		t.Fatalf("result %+v", got)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if seen.URL != job.URL || seen.Method != "POST" || string(seen.Body) != job.Body || seen.Header.Get("Authorization") != "Bearer x" ||
		seen.Timeout != 7*time.Second || seen.ExpectedStatus != "2xx" {
		t.Fatalf("request %+v", seen)
	}
	if g.hellos.Load() < 1 {
		t.Fatal("no hello")
	}
	if err := stopExec(); err != nil {
		t.Fatalf("clean stop: %v", err)
	}
}

func TestKeepsResultsWhileTheGatewayFails(t *testing.T) {
	g := &fakeGateway{t: t, token: "spc_t"}
	g.failNext.Store(2)
	g.jobs = []wire.Job{newJob()}
	start(t, g, proberFunc(func(context.Context, checks.Request) checks.Result {
		return checks.Result{ErrorKind: checks.KindConnect, CheckedAt: time.Now().UTC()}
	}))
	// flushInterval is one second: two failures, then success, without losing the result.
	deadline := time.Now().Add(8 * time.Second)
	for len(g.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	got := g.got()
	if len(got) != 1 || got[0].OK || got[0].ErrorKind != checks.KindConnect {
		t.Fatalf("result %+v", got)
	}
}

func TestRejectedTokenStopsTheExecutor(t *testing.T) {
	g := &fakeGateway{t: t, token: "spc_right"}
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	e := New(Config{BaseURL: srv.URL, Token: "spc_wrong", Prober: proberFunc(func(context.Context, checks.Request) checks.Result { return checks.Result{} })})
	if err := e.Run(context.Background()); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v", err)
	}
}

func TestSkipsExpiredLeasesAndDropsOldResults(t *testing.T) {
	g := &fakeGateway{t: t, token: "spc_t"}
	expired := newJob()
	expired.LeaseUntil = time.Now().Add(-time.Second)
	g.jobs = []wire.Job{expired}
	var ran atomic.Int32
	e := New(Config{BaseURL: "http://unused", Token: "x", Prober: proberFunc(func(context.Context, checks.Request) checks.Result {
		ran.Add(1)
		return checks.Result{OK: true, CheckedAt: time.Now().UTC()}
	})})
	e.run(context.Background(), expired)
	if ran.Load() != 0 || e.queued() != 0 {
		t.Fatalf("an expired lease was run (%d) or queued (%d)", ran.Load(), e.queued())
	}
	e.enqueue(wire.Result{MonitorID: uuid.New(), CheckedAt: time.Now().Add(-time.Hour)})
	e.enqueue(wire.Result{MonitorID: uuid.New(), CheckedAt: time.Now()})
	if batch := e.takeBatch(); len(batch) != 1 {
		t.Fatalf("batch %d, want only the fresh result", len(batch))
	}
}

func TestCancelledChecksAreNotReported(t *testing.T) {
	e := New(Config{BaseURL: "http://unused", Token: "x", Prober: proberFunc(func(context.Context, checks.Request) checks.Result {
		return checks.Result{Cancelled: true}
	})})
	e.run(context.Background(), newJob())
	if e.queued() != 0 {
		t.Fatal("a cancelled check was queued")
	}
}

func TestQueueIsBounded(t *testing.T) {
	e := New(Config{BaseURL: "http://unused", Token: "x"})
	for range maxQueued + 5 {
		e.enqueue(wire.Result{MonitorID: uuid.New(), CheckedAt: time.Now()})
	}
	if e.queued() != maxQueued {
		t.Fatalf("queued %d", e.queued())
	}
}
