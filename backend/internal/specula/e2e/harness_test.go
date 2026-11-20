package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/executor"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	tenantID = "01920000-0000-7000-8000-00000000000a"
	hookURL  = "https://hooks.slack.com/services/TAAA/BAAA/secretAAAA"
)

// link is the network between the executor and the gateway. cut drops every connection.
type link struct {
	srv        *httptest.Server
	clk        *simClock
	cut        atomic.Bool
	noSchedule atomic.Bool // answers 404 on /schedule, as a gateway without it does
	mu         sync.Mutex
	claims     []string // bodies of the answers to /claim
	hits       map[string]int
}

type tee struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (w *tee) Write(b []byte) (int, error) { w.buf.Write(b); return w.ResponseWriter.Write(b) }

func newLink(t *testing.T, clk *simClock, h http.Handler) *link {
	l := &link{clk: clk, hits: map[string]int{}}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.cut.Load() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		clk.busy.Add(1)
		defer clk.busy.Add(-1)
		l.mu.Lock()
		l.hits[r.URL.Path]++
		l.mu.Unlock()
		if l.noSchedule.Load() && r.URL.Path == wire.PathSchedule {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == wire.PathClaim {
			tw := &tee{ResponseWriter: w}
			h.ServeHTTP(tw, r)
			l.mu.Lock()
			l.claims = append(l.claims, tw.buf.String())
			l.mu.Unlock()
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(l.srv.Close)
	return l
}

// prober answers checks from the failure windows of the test, by the simulated time.
type prober struct {
	clk  *simClock
	mu   sync.Mutex
	down map[string][][2]time.Time
	seen []checks.Request
}

func (p *prober) failDuring(key string, from, to time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down[key] = append(p.down[key], [2]time.Time{from, to})
}

func (p *prober) Probe(_ context.Context, req checks.Request) checks.Result {
	p.clk.busy.Add(1)
	defer p.clk.busy.Add(-1)
	now := p.clk.Now()
	key := req.URL
	if req.TCP {
		key = net.JoinHostPort(req.Host, fmt.Sprint(req.Port))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, req)
	for _, w := range p.down[key] {
		if !now.Before(w[0]) && now.Before(w[1]) {
			return checks.Result{ErrorKind: "timeout", LatencyMs: 10000, CheckedAt: now}
		}
	}
	res := checks.Result{OK: true, LatencyMs: 12, CheckedAt: now}
	if !req.TCP {
		res.StatusCode = 200
	}
	return res
}

func (p *prober) tcpSeen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.seen {
		if r.TCP {
			n++
		}
	}
	return n
}

// mon is a monitor of the Specula with the grid the test gave it.
type mon struct {
	ID       uuid.UUID
	Name     string
	Key      string // what the prober sees: the URL, or host:port
	Anchor   int64
	Interval int64
	TCP      bool
}

// slots are the slots of m in [from, to] (both inclusive).
func (m mon) slots(from, to time.Time) []int64 {
	var out []int64
	for s := m.Anchor; s <= to.Unix(); s += m.Interval {
		if s >= from.Unix() {
			out = append(out, s)
		}
	}
	return out
}

type monSpec struct {
	name      string
	interval  int
	threshold int
	tcp       bool
}

type env struct {
	t       *testing.T
	ctx     context.Context
	pg      *pgtest.Env
	admin   *pgx.Conn
	worker  *pgx.Conn
	pool    *db.Pool
	keys    *crypto.Keyring
	token   string
	spec    specula.Specula
	mons    []mon
	t0      time.Time // start of the simulation, on an hour
	clk     *simClock
	link    *link
	prober  *prober
	reg     *prometheus.Registry
	logs    *syncBuf
	mon     *monitors.Service
	actor   monitors.Actor
	sent    *recordedSlack
	deliver *jobs.Deliverer
	// watermark and lastCatchUp belong to the simulated rollup run (catchUp).
	watermark, lastCatchUp time.Time
	// shift is how far moveToOldMonth moved the data into the past.
	shift time.Duration
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type recordedSlack struct {
	mu    sync.Mutex
	texts []string
}

func (r *recordedSlack) Send(_ context.Context, _, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return nil
}

func (r *recordedSlack) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.texts)
}

// newEnv builds the server side: a database, an organization with a Specula and the monitors, the
// real gateway behind a link, and a simulation that starts back before the current
// hour, so everything it does lies in the past of the database clock and in the 7 days the rollups
// can still be corrected.
func newEnv(t *testing.T, name string, back time.Duration, specs []monSpec) *env {
	t.Helper()
	ctx := context.Background()
	pg := pgtest.New(t, pgtest.DBName(name))
	e := &env{t: t, ctx: ctx, pg: pg, admin: pg.AdminConn(t), worker: pg.Conn(t, pg.WorkerURL), reg: prometheus.NewRegistry(), logs: &syncBuf{}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("executor and gateway log:\n%s", e.logs.String())
		}
	})
	e.t0 = time.Now().UTC().Truncate(time.Hour).Add(-back)
	e.clk = newSimClock(e.t0)
	user := uuid.New()
	pgtest.MustExec(t, e.admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantID+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`,
		`UPDATE tenants SET plan_key = 'internal'`)
	apiPool, workerPool := pg.Pool(t, pg.APIURL, 4), pg.Pool(t, pg.WorkerURL, 8)
	e.pool = workerPool
	var err error
	if e.keys, err = crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{6}, 32)}, "r1"); err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	e.mon = monitors.NewService(apiPool, sched, inc).WithRequestKeys(e.keys)
	e.actor = monitors.Actor{TenantID: uuid.MustParse(tenantID), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	svc := specula.NewService(apiPool)
	sp, token, err := svc.Create(ctx, specula.Actor{TenantID: e.actor.TenantID, UserID: user}, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	e.spec, e.token = sp, token
	// A channel of the Default project, where the monitors are, so every incident queues its messages.
	ch := notify.NewService(apiPool, e.keys, nil, "")
	if _, err := ch.Create(ctx, notify.Actor{TenantID: e.actor.TenantID, UserID: user, Access: authz.Actor{Role: authz.Owner}},
		notify.CreateInput{Name: "alerts", WebhookURL: hookURL}); err != nil {
		t.Fatal(err)
	}
	e.sent = &recordedSlack{}
	e.deliver = jobs.NewDeliverer(jobs.DeliveryConfig{PublicURL: "https://proreta.example.com", ChannelInterval: time.Millisecond}, jobs.DeliveryDeps{
		Pool: workerPool, Channels: notify.NewService(workerPool, e.keys, nil, ""), Notifier: e.sent,
		Metrics: jobs.NewMetrics(prometheus.NewRegistry()), Log: slog.New(slog.DiscardHandler),
		// Past the grace a late "down" waits for its recovery (the test is about what follows it).
		Clock: clock.NewFake(time.Now().Add(time.Hour)),
	})

	for i, s := range specs {
		in := monitors.CreateInput{Name: s.name, SpeculaID: &sp.ID}
		m := mon{Name: s.name, Interval: int64(s.interval), TCP: s.tcp}
		if s.tcp {
			host, port := fmt.Sprintf("db%d.internal.example.com", i), 5432
			in.Type, in.TCP = monitors.TypeTCP, &monitors.TCPSettings{Host: &host, Port: &port, FailureThreshold: &s.threshold}
			m.Key = net.JoinHostPort(host, fmt.Sprint(port))
		} else {
			url := fmt.Sprintf("https://svc%d.internal.example.com/", i)
			in.HTTP = &monitors.HTTPSettings{URL: &url, IntervalSeconds: &s.interval, FailureThreshold: &s.threshold}
			m.Key = url
		}
		created, err := e.mon.Create(ctx, e.actor, in)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = created.ID
		// The epoch starts at the beginning of the simulation, on a grid of the test's own.
		m.Anchor = e.t0.Unix() + int64(7+13*i)%m.Interval
		pgtest.MustExec(t, e.admin, fmt.Sprintf(`UPDATE monitor_schedule_epochs SET active_from = to_timestamp(%d), anchor = %d WHERE monitor_id = '%s'`,
			e.t0.Unix(), m.Anchor, m.ID))
		e.mons = append(e.mons, m)
	}

	// Pull mode (the old executor, the fallback) claims what is due on the database clock.
	pgtest.MustExec(t, e.admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 hour'`)
	e.prober = &prober{clk: e.clk, down: map[string][][2]time.Time{}}
	h := gateway.New(gateway.Deps{
		Pool: workerPool, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: e.clk},
		RequestKeys: e.keys, Clock: e.clk,
		Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelWarn})), Registerer: e.reg,
	})
	e.link = newLink(t, e.clk, h)
	return e
}

// runner is one executor process.
type runner struct {
	ex     *executor.Executor
	cancel context.CancelFunc
	done   chan error
}

// start runs an executor with the state directory dir and the clock view clk.
func (e *env) start(clk executor.Clock, dir string) *runner {
	e.t.Helper()
	ex := executor.New(executor.Config{
		BaseURL: e.link.srv.URL, Token: e.token, Version: "e2e", Hostname: "e2e-host", HTTP: e.clk.counted(e.link.srv.Client()),
		Prober: e.prober, Clock: clk, StateDir: dir,
		PollInterval: 20 * time.Millisecond, // pull mode polls on a real ticker
		Log:          slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	ctx, cancel := context.WithCancel(e.ctx)
	r := &runner{ex: ex, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- ex.Run(ctx) }()
	e.t.Cleanup(func() { _ = r.stop(e.t, e.clk) })
	return r
}

// stop ends the executor and forgets its timers; stopping twice is fine.
func (r *runner) stop(t *testing.T, clk *simClock) error {
	r.cancel()
	select {
	case err := <-r.done:
		r.done <- err
		clk.forget()
		return err
	case <-time.After(30 * time.Second):
		t.Error("the executor did not stop")
		return nil
	}
}

// run advances the simulated time by d in steps of at most step.
func (e *env) run(d, step time.Duration) {
	for ; d > 0; d -= step {
		e.clk.Advance(min(step, d))
		e.tick()
	}
}

// until advances in steps until cond holds, or fails after limit of simulated time.
func (e *env) until(what string, limit, step time.Duration, cond func() bool) {
	e.t.Helper()
	for waited := time.Duration(0); !cond(); waited += step {
		if waited >= limit {
			e.t.Fatalf("after %v of simulated time: %s\n%s", limit, what, e.state())
		}
		e.clk.Advance(step)
		e.tick()
		time.Sleep(2 * time.Millisecond) // lets in-flight requests finish in real time
	}
}

// stored returns the check_results rows by monitor, as slots; it fails when a row is not exactly on a second.
func (e *env) stored() map[uuid.UUID][]int64 {
	e.t.Helper()
	rows, err := e.admin.Query(e.ctx, `SELECT monitor_id, checked_at FROM check_results WHERE location_id = $1 ORDER BY monitor_id, checked_at`, e.spec.LocationID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]int64{}
	for rows.Next() {
		var id uuid.UUID
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			e.t.Fatal(err)
		}
		if at.Nanosecond() != 0 {
			e.t.Fatalf("a row is not on a whole second: %v", at.Format(time.RFC3339Nano))
		}
		out[id] = append(out[id], at.Unix())
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) rowCount() int {
	n := 0
	for _, s := range e.stored() {
		n += len(s)
	}
	return n
}

// wantCovered fails unless every monitor has exactly the slots from..to, each once.
func (e *env) wantCovered(from, to time.Time) {
	e.t.Helper()
	got := e.stored()
	for _, m := range e.mons {
		want := m.slots(from, to)
		if !slices.Equal(got[m.ID], want) {
			e.t.Errorf("%s: %d rows, want the %d slots %d..%d; first difference: %s", m.Name, len(got[m.ID]), len(want), from.Unix(), to.Unix(), firstDiff(got[m.ID], want))
		}
	}
}

// wantCoveredExcept is wantCovered when the slots in [gapFrom, gapTo) may be missing, but no row may
// be anywhere else than on a slot.
func (e *env) wantCoveredExcept(from, to, gapFrom, gapTo time.Time) (missing int) {
	e.t.Helper()
	got := e.stored()
	for _, m := range e.mons {
		want := m.slots(from, to)
		for _, s := range want {
			if inGap := s >= gapFrom.Unix() && s < gapTo.Unix(); !inGap && !slices.Contains(got[m.ID], s) {
				e.t.Errorf("%s: slot %d is missing (outside the gap)", m.Name, s)
			} else if inGap && !slices.Contains(got[m.ID], s) {
				missing++
			}
		}
		for _, s := range got[m.ID] {
			if !slices.Contains(want, s) {
				e.t.Errorf("%s: a row at %d is not a slot", m.Name, s)
			}
		}
	}
	return missing
}

func firstDiff(got, want []int64) string {
	for i := 0; i < max(len(got), len(want)); i++ {
		switch {
		case i >= len(got):
			return fmt.Sprintf("missing slot %d (and %d more)", want[i], len(want)-i-1)
		case i >= len(want):
			return fmt.Sprintf("extra row %d (and %d more)", got[i], len(got)-i-1)
		case got[i] != want[i]:
			return fmt.Sprintf("row %d where slot %d was expected", got[i], want[i])
		}
	}
	return "none"
}

// catchUp is the regular rollup run (compute_rollups_catch_up with a lookback of 3 hours) at
// simulated time at. The database clock is not the simulated one, so the function is not called;
// its window is: from the older of the watermark and three hours before the last closed hour, to
// the last closed hour.
func (e *env) catchUp(at time.Time) {
	e.t.Helper()
	closed := at.Truncate(time.Hour)
	if e.watermark.IsZero() {
		e.watermark = e.t0
	}
	lo := e.watermark
	if l := closed.Add(-3 * time.Hour); l.Before(lo) {
		lo = l
	}
	if closed.After(lo) {
		if _, err := e.worker.Exec(e.ctx, `SELECT compute_rollups($1, $2)`, lo, closed); err != nil {
			e.t.Fatal(err)
		}
	}
	if closed.After(e.watermark) {
		e.watermark = closed
	}
	e.lastCatchUp = at
}

// tick runs the rollup every five simulated minutes, as jobs does.
func (e *env) tick() {
	if e.clk.Now().Sub(e.lastCatchUp) >= 5*time.Minute {
		e.catchUp(e.clk.Now())
	}
}

// drain runs the dirty hour drain until nothing is left.
func (e *env) drain() (hours int) {
	e.t.Helper()
	for {
		var processed int
		var remaining int64
		if err := e.worker.QueryRow(e.ctx, `SELECT processed, remaining FROM rollup_drain_dirty(6)`).Scan(&processed, &remaining); err != nil {
			e.t.Fatal(err)
		}
		hours += processed
		if remaining == 0 {
			return hours
		}
	}
}

// rolledUp returns the hourly rollup totals by monitor and hour.
func (e *env) rolledUp() map[uuid.UUID]map[time.Time]int64 {
	e.t.Helper()
	rows, err := e.admin.Query(e.ctx, `SELECT monitor_id, hour, sum(total)::bigint FROM result_rollups_hourly WHERE location_id = $1 GROUP BY 1, 2`, e.spec.LocationID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID]map[time.Time]int64{}
	for rows.Next() {
		var id uuid.UUID
		var h time.Time
		var n int64
		if err := rows.Scan(&id, &h, &n); err != nil {
			e.t.Fatal(err)
		}
		if out[id] == nil {
			out[id] = map[time.Time]int64{}
		}
		out[id][h.UTC().Add(-e.shift)] = n
	}
	return out
}

// wantRolledUp fails unless, for every closed hour before end, each monitor's rollup counts exactly
// the slots of that hour. It returns the number of hours whose rollup was wrong.
func (e *env) rolledUpMismatches(from, end time.Time) (bad int) {
	e.t.Helper()
	got := e.rolledUp()
	for _, m := range e.mons {
		for h := from.Truncate(time.Hour); h.Before(end.Truncate(time.Hour)); h = h.Add(time.Hour) {
			want := int64(len(m.slots(laterOf(from, h), h.Add(time.Hour-time.Second))))
			if got[m.ID][h] != want {
				bad++
			}
		}
	}
	return bad
}

func (e *env) count(q string, args ...any) (n int) {
	e.t.Helper()
	if err := e.admin.QueryRow(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) monitor(name string) mon {
	for _, m := range e.mons {
		if m.Name == name {
			return m
		}
	}
	e.t.Fatalf("no monitor %q", name)
	return mon{}
}

// pgNow is the database clock; the simulation must stay behind it.
func (e *env) pgNow() time.Time {
	var now time.Time
	if err := e.admin.QueryRow(e.ctx, `SELECT now()`).Scan(&now); err != nil {
		e.t.Fatal(err)
	}
	return now.UTC()
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// state describes what is stored and what the executor has reported, for a failure message.
func (e *env) state() string {
	var b strings.Builder
	got := e.stored()
	fmt.Fprintf(&b, "now %v\n", e.clk.Now().Format(time.RFC3339))
	for _, m := range e.mons {
		fmt.Fprintf(&b, "%s: %d rows, first %v last %v\n", m.Name, len(got[m.ID]), first(got[m.ID]), last(got[m.ID]))
	}
	return b.String()
}

func first(s []int64) int64 {
	if len(s) == 0 {
		return 0
	}
	return s[0]
}

func last(s []int64) int64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

func (l *link) hit(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hits[path]
}

// deliverAll sends the outbox until nothing is waiting.
func (e *env) deliverAll() {
	e.t.Helper()
	for i := 0; i < 20 && e.count(`SELECT count(*) FROM notification_deliveries WHERE status IN ('pending', 'in_progress')`) > 0; i++ {
		e.deliver.RunOnce(e.ctx)
	}
}

// texts are the messages sent about a monitor, oldest first.
func (e *env) textsAbout(name string) (out []string) {
	for _, t := range e.sent.all() {
		if strings.Contains(t, "*"+name+"*") {
			out = append(out, t)
		}
	}
	return out
}

// post sends reports as a client of the gateway that is not the executor (a bug, a replay of an old
// batch, or a hostile holder of the token) and returns the outcomes.
func (e *env) post(rs ...wire.Report) []string {
	e.t.Helper()
	raw, _ := json.Marshal(wire.ReportsRequest{Results: rs})
	req, _ := http.NewRequest(http.MethodPost, e.link.srv.URL+wire.PathReports, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := e.link.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out wire.ReportsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK || len(out.Outcomes) != len(rs) {
		e.t.Fatalf("reports: status %d, %v", resp.StatusCode, err)
	}
	got := make([]string, len(out.Outcomes))
	for i, o := range out.Outcomes {
		got[i] = o.Outcome
	}
	return got
}

// dump is everything the schedule and the history consist of, to compare before and after.
func (e *env) dump() string {
	e.t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT monitor_id, checked_at, ok, status_code, latency_ms, error_kind FROM check_results ORDER BY 1, 2`,
		`SELECT monitor_id, epoch, anchor, interval_seconds, active_from, active_until FROM monitor_schedule_epochs ORDER BY 1, 2`,
		`SELECT monitor_id, next_run_at FROM monitor_location_state ORDER BY 1`,
		`SELECT monitor_id, status FROM monitor_state ORDER BY 1`,
		`SELECT id, monitor_id, started_at, resolved_at FROM incidents ORDER BY 1`,
	} {
		rows, err := e.admin.Query(e.ctx, q)
		if err != nil {
			e.t.Fatal(err)
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				e.t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
		fmt.Fprintln(&b, "--")
	}
	return b.String()
}

// moveToOldMonth moves the raw rows, the rollups and the dirty marks to a month that ended more than
// 30 days ago, so that retention can look at them (the simulation has to stay within the last days,
// where the database clock lets rows be marked). It returns the name of the partition.
func (e *env) moveToOldMonth() string {
	e.t.Helper()
	month := time.Now().UTC().AddDate(0, -3, 0)
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	e.shift = start.Add(10 * 24 * time.Hour).Sub(e.t0) // hours: both are on the hour
	name := "check_results_p" + start.Format("200601")
	ident := pgx.Identifier{name}.Sanitize()
	pgtest.MustExec(e.t, e.admin,
		fmt.Sprintf(`CREATE TABLE %s PARTITION OF check_results FOR VALUES FROM ('%s') TO ('%s')`, ident, start.Format(time.RFC3339), start.AddDate(0, 1, 0).Format(time.RFC3339)),
		`ALTER TABLE `+ident+` OWNER TO proreta_owner`,
		`ALTER TABLE `+ident+` ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE `+ident+` FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY tenant_isolation ON `+ident+` USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)`,
		fmt.Sprintf(`UPDATE check_results SET checked_at = checked_at - make_interval(secs => %d)`, int64(-e.shift/time.Second)),
		fmt.Sprintf(`UPDATE result_rollups_hourly SET hour = hour - make_interval(secs => %d)`, int64(-e.shift/time.Second)),
		fmt.Sprintf(`UPDATE rollup_dirty_hours SET hour = hour - make_interval(secs => %d)`, int64(-e.shift/time.Second)))
	return name
}

// detach asks the retention function to detach the partition.
func (e *env) detach(name string) bool {
	e.t.Helper()
	var ok bool
	if err := e.worker.QueryRow(e.ctx, `SELECT detach_expired_partition($1)`, name).Scan(&ok); err != nil {
		e.t.Fatal(err)
	}
	return ok
}
