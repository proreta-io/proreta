package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// oldExecutor is a client of the first protocol: a hello with no capabilities, claim, results.
type oldExecutor struct {
	e *env
}

func (o oldExecutor) call(path string, in, out any) {
	o.e.t.Helper()
	raw, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, o.e.link.srv.URL+wire.BasePath+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.e.token)
	resp, err := o.e.link.srv.Client().Do(req)
	if err != nil {
		o.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, out) != nil {
		o.e.t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
	}
}

// round says hello, claims, and reports every job it got as a success; it returns the jobs.
func (o oldExecutor) round() (wire.HelloResponse, []wire.Job) {
	var hello wire.HelloResponse
	o.call("/hello", wire.HelloRequest{Version: "0.1.0", Hostname: "old"}, &hello)
	var claimed wire.ClaimResponse
	o.call("/claim", wire.ClaimRequest{Max: 50}, &claimed)
	now := o.e.clk.Now()
	var results wire.ResultsRequest
	for _, j := range claimed.Jobs {
		results.Results = append(results.Results, wire.Result{MonitorID: j.MonitorID, LeaseToken: j.LeaseToken, CheckedAt: now, OK: true, StatusCode: 200, LatencyMs: 9})
	}
	if len(results.Results) > 0 {
		var out wire.ResultsResponse
		o.call("/results", results, &out)
		for _, r := range out.Outcomes {
			if r.Outcome != wire.OutcomeRecorded {
				o.e.t.Errorf("an old executor's result was %s", r.Outcome)
			}
		}
	}
	return hello, claimed.Jobs
}

func (e *env) rowsOf(m mon) int { return len(e.stored()[m.ID]) }

// An executor of the first protocol keeps working against the gateway with schedule mode: it is
// served in pull mode, never gets a tcp job, and takes over from (and hands back to) a new one.
func TestOldExecutorKeepsWorking(t *testing.T) {
	e := newEnv(t, "e2eoldexec", 3*time.Hour, []monSpec{
		{name: "api", interval: 60, threshold: 1},
		{name: "db", interval: 60, threshold: 1, tcp: true},
	})
	api, db := e.monitor("api"), e.monitor("db")
	old := oldExecutor{e}

	// Rollback after an upgrade: the new executor ran in schedule mode, then the old image comes back.
	r := e.start(e.clk, t.TempDir())
	e.ready()
	e.run(5*time.Minute, time.Minute)
	e.until("both monitors have rows", 5*time.Minute, 5*time.Second, func() bool { return e.rowsOf(api) >= 4 && e.rowsOf(db) >= 4 })
	if err := r.stop(t, e.clk); err != nil {
		t.Fatal(err)
	}
	scheduled := e.rowsOf(db)
	if o := e.count(`SELECT count(*) FROM specula_agents WHERE id = $1 AND 'schedule/1' = ANY (capabilities)`, e.spec.ID); o != 1 {
		t.Fatalf("the new executor did not announce schedule mode")
	}

	var seen []wire.Job
	for i := 0; i < 4; i++ {
		e.clk.Advance(time.Minute)
		hello, jobs := old.round()
		if !slices.Equal(hello.Modes, []string{wire.ModePull}) || hello.ScheduleVersion != "" {
			t.Fatalf("hello answered %+v to an executor with no capabilities", hello)
		}
		seen = append(seen, jobs...)
	}
	if len(seen) == 0 {
		t.Fatal("the old executor was given no job: an outage at the rollback")
	}
	for _, j := range seen {
		if j.Type == wire.TypeTCP || j.MonitorID == db.ID || j.Host != "" {
			t.Errorf("the old executor was handed a tcp job: %+v", j)
		}
	}
	for _, body := range e.link.claims {
		var c wire.ClaimResponse
		if json.Unmarshal([]byte(body), &c) != nil {
			t.Fatalf("claim answer %q", body)
		}
		for _, j := range c.Jobs {
			if j.MonitorID == db.ID {
				t.Errorf("a claim answer holds the tcp monitor: %s", body)
			}
		}
	}
	if got := e.rowsOf(db); got != scheduled {
		t.Errorf("the tcp monitor got %d rows from an executor that cannot run tcp checks", got-scheduled)
	}
	if n := e.count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND latency_ms = 9`, api.ID); n != len(seen) {
		t.Errorf("%d rows from the old executor, want %d (one per job)", n, len(seen))
	}

	// And forward again: the latest hello decides, the new executor takes the location back.
	e.start(e.clk, t.TempDir())
	e.ready()
	e.run(3*time.Minute, time.Minute)
	e.until("the tcp monitor is checked again", 5*time.Minute, 5*time.Second, func() bool { return e.rowsOf(db) > scheduled })
	var claimed wire.ClaimResponse
	old.call("/claim", wire.ClaimRequest{Max: 50}, &claimed)
	if len(claimed.Jobs) != 0 {
		t.Errorf("the claim handed %d jobs to a location in schedule mode", len(claimed.Jobs))
	}
}

// A new executor that finds no /schedule (a gateway without it) says so and falls back to pull mode.
func TestNewExecutorFallsBackWithoutSchedule(t *testing.T) {
	e := newEnv(t, "e2enoschedule", 3*time.Hour, []monSpec{
		{name: "api", interval: 60, threshold: 1},
		{name: "db", interval: 60, threshold: 1, tcp: true},
	})
	api, db := e.monitor("api"), e.monitor("db")
	e.link.noSchedule.Store(true)
	e.start(e.clk, t.TempDir())
	e.until("both monitors are checked in pull mode", 5*time.Minute, time.Second, func() bool {
		return e.link.hit(wire.PathSchedule) > 0 && e.rowsOf(api) >= 3 && e.rowsOf(db) >= 3
	})
	if n := e.count(`SELECT count(*) FROM specula_agents WHERE id = $1 AND capabilities = ARRAY['tcp']`, e.spec.ID); n != 1 {
		t.Errorf("the last hello still announced schedule mode")
	}
	if e.prober.tcpSeen() == 0 {
		t.Error("the tcp monitor was never checked: the executor announced tcp, it must be handed the job")
	}
	if got := e.link.hit(wire.PathReports); got != 0 {
		t.Errorf("%d calls to /reports in pull mode", got)
	}
	if got := e.link.hit(wire.PathSchedule); got != 1 {
		t.Errorf("%d calls to /schedule, want one: the 404 is final", got)
	}
}
