package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationReports(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculareports"))
	ctx := context.Background()
	admin := pg.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	pgtest.MustExec(t, admin, `UPDATE tenants SET plan_key = 'internal'`)
	apiPool, worker := pg.Pool(t, pg.APIURL, 4), pg.Pool(t, pg.WorkerURL, 4)
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc)
	svc := specula.NewService(apiPool)
	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, token, err := svc.Create(ctx, actor, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	macts := func(tenant string) monitors.Actor {
		return monitors.Actor{TenantID: uuid.MustParse(tenant), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	}
	url, one := "https://api.internal.example.com/", 1
	m, err := mon.Create(ctx, macts(tenantA), monitors.CreateInput{Name: "API", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url}})
	if err != nil {
		t.Fatal(err)
	}
	flaky, err := mon.Create(ctx, macts(tenantA), monitors.CreateInput{Name: "Flaky", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url, FailureThreshold: &one}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := mon.Create(ctx, macts(tenantA), monitors.CreateInput{Name: "Replay", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url, FailureThreshold: &one}})
	if err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `INSERT INTO notification_channels (id, tenant_id, type, config_encrypted, project_id)
		VALUES (gen_random_uuid(), '`+tenantA+`', 'slack_webhook', '\x01', (SELECT id FROM projects WHERE tenant_id = '`+tenantA+`' AND is_default))`)
	tolerant, err := mon.Create(ctx, macts(tenantA), monitors.CreateInput{Name: "Tolerant", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url, FailureThreshold: &one}})
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := mon.Create(ctx, macts(tenantA), monitors.CreateInput{Name: "Cloud", HTTP: &monitors.HTTPSettings{URL: &url}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := mon.Create(ctx, macts(tenantB), monitors.CreateInput{Name: "Foreign", HTTP: &monitors.HTTPSettings{URL: &url}})
	if err != nil {
		t.Fatal(err)
	}
	// The epochs start 72 hours ago, so the 24 hour backlog is what limits old slots.
	pgtest.MustExec(t, admin, `UPDATE monitor_schedule_epochs SET active_from = now() - interval '72 hours', anchor = floor(extract(epoch FROM now() - interval '72 hours'))::bigint + 7`)

	reg := prometheus.NewRegistry()
	srv := httptest.NewServer(gateway.New(gateway.Deps{
		Limits: openLimits,
		Pool:   worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: reg,
	}))
	t.Cleanup(srv.Close)

	// slot returns the slot of monitor mon k intervals before the latest one that is not after now.
	slot := func(mon monitors.Monitor, k int64) int64 {
		var anchor, interval int64
		if err := admin.QueryRow(ctx, `SELECT anchor, interval_seconds FROM monitor_schedule_epochs WHERE monitor_id = $1 ORDER BY epoch DESC LIMIT 1`, mon.ID).Scan(&anchor, &interval); err != nil {
			t.Fatal(err)
		}
		return anchor + ((time.Now().Unix()-anchor)/interval-k)*interval
	}
	report := func(mon monitors.Monitor, ep int, sl int64, ok bool) wire.Report {
		r := wire.Report{MonitorID: mon.ID, Epoch: ep, Slot: sl, OK: ok, LatencyMs: 12}
		if ok {
			r.StatusCode = 200
		} else {
			r.ErrorKind = "timeout"
		}
		return r
	}
	send := func(rs ...wire.Report) []string {
		t.Helper()
		code, b := call(t, srv, token, "/reports", wire.ReportsRequest{Results: rs})
		var out wire.ReportsResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil || len(out.Outcomes) != len(rs) {
			t.Fatalf("reports: %d %s", code, b)
		}
		var got []string
		for i, o := range out.Outcomes {
			if o.MonitorID != rs[i].MonitorID || o.Slot != rs[i].Slot {
				t.Fatalf("outcome %d does not name its report: %+v", i, o)
			}
			got = append(got, o.Outcome)
		}
		return got
	}
	want := func(step string, got []string, exp ...string) {
		t.Helper()
		if !slices.Equal(got, exp) {
			t.Errorf("%s: %v, want %v", step, got, exp)
		}
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("recorded at the slot, applied, replay changes nothing", func(t *testing.T) {
		var batch []wire.Report
		for k := int64(5); k >= 0; k-- {
			batch = append(batch, report(m, 1, slot(m, k), true))
		}
		want("first", send(batch...), "recorded", "recorded", "recorded", "recorded", "recorded", "recorded")
		if n := count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND ok AND status_code = 200 AND extract(epoch FROM checked_at) = ANY($2)`,
			m.ID, []int64{batch[0].Slot, batch[5].Slot}); n != 2 {
			t.Fatalf("rows at their slots: %d", n)
		}
		var status string
		if err := admin.QueryRow(ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&status); err != nil || status != "up" {
			t.Fatalf("status %q %v", status, err)
		}
		if n := count(`SELECT count(*) FROM rollup_dirty_hours`); n != 0 {
			t.Fatalf("recent rows marked %d dirty hours", n)
		}
		// The same slots again, now claiming failures: all duplicates, stored rows untouched.
		for i := range batch {
			batch[i] = report(m, 1, batch[i].Slot, false)
		}
		want("replay", send(batch...), "duplicate", "duplicate", "duplicate", "duplicate", "duplicate", "duplicate")
		if n := count(`SELECT count(*) FROM check_results WHERE monitor_id = $1`, m.ID); n != 6 {
			t.Fatalf("rows after replay: %d", n)
		}
		if n := count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND NOT ok`, m.ID); n != 0 {
			t.Fatalf("a replay changed %d rows", n)
		}
		// One slot twice in a batch is one row.
		twice := report(flaky, 1, slot(flaky, 1), true)
		want("twice in a batch", send(twice, twice), "recorded", "duplicate")
	})

	t.Run("refusals", func(t *testing.T) {
		now := slot(m, 0)
		bad := report(m, 1, now, true)
		bad.ErrorKind = "tls" // ok with an error kind
		want("batch", send(
			report(m, 1, now-1, true),       // off the grid
			report(m, 1, now+3600, true),    // future grid slot
			report(m, 1, 0, true),           // zero
			report(m, 1, now-25*3600, true), // on the grid, older than the backlog
			report(m, 7, now, true),         // an epoch the monitor does not have
			bad,                             // malformed
			report(cloud, 1, now, true),     // a monitor at another location
			report(foreign, 1, now, true),   // another organization's monitor
			report(monitors.Monitor{ID: uuid.New()}, 1, now, true), // nothing
		), "rejected", "rejected", "rejected", "expired", "superseded", "rejected", "rejected", "rejected", "rejected")
	})

	t.Run("expired", func(t *testing.T) {
		var anchor, interval int64
		if err := admin.QueryRow(ctx, `SELECT anchor, interval_seconds FROM monitor_schedule_epochs WHERE monitor_id = $1`, m.ID).Scan(&anchor, &interval); err != nil {
			t.Fatal(err)
		}
		old := anchor + ((time.Now().Unix()-25*3600-anchor)/interval)*interval
		want("25 hours old", send(report(m, 1, old, true)), "expired")
		if n := count(`SELECT count(*) FROM check_results WHERE monitor_id = $1`, m.ID); n != 6 {
			t.Fatalf("rows: %d", n)
		}
	})

	t.Run("a row older than the rollup lookback marks its hour dirty", func(t *testing.T) {
		sl := slot(m, 5*60) // five hours ago at a one-minute interval
		want("late", send(report(m, 1, sl, true)), "recorded")
		if n := count(`SELECT count(*) FROM rollup_dirty_hours WHERE hour = date_trunc('hour', to_timestamp($1), 'UTC')`, sl); n != 1 {
			t.Fatalf("dirty hour marked %d times", n)
		}
		want("late duplicate", send(report(m, 1, sl, true)), "duplicate")
	})

	t.Run("the state machine runs", func(t *testing.T) {
		want("failure", send(report(flaky, 1, slot(flaky, 0), false)), "recorded")
		var status string
		if err := admin.QueryRow(ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, flaky.ID).Scan(&status); err != nil || status != "down" {
			t.Fatalf("status %q %v", status, err)
		}
		if n := count(`SELECT count(*) FROM incidents WHERE monitor_id = $1`, flaky.ID); n != 1 {
			t.Fatalf("incidents %d", n)
		}
	})

	t.Run("late alerts: a replayed outage keeps its real times and is marked late", func(t *testing.T) {
		want("outage", send(
			report(replay, 1, slot(replay, 60), false),
			report(replay, 1, slot(replay, 59), false),
			report(replay, 1, slot(replay, 58), true),
		), "recorded", "recorded", "recorded")
		var started, resolved time.Time
		if err := admin.QueryRow(ctx, `SELECT started_at, resolved_at FROM incidents WHERE monitor_id = $1`, replay.ID).Scan(&started, &resolved); err != nil {
			t.Fatal(err)
		}
		if started.Unix() != slot(replay, 60) || resolved.Unix() != slot(replay, 58) {
			t.Fatalf("incident %v to %v, want the slots %d to %d", started, resolved, slot(replay, 60), slot(replay, 58))
		}
		if n := count(`SELECT count(*) FROM notification_deliveries WHERE monitor_id = $1 AND payload->>'late' = 'true' AND payload ? 'observedAt'
			AND ((kind = 'opened') OR (kind = 'resolved')) `, replay.ID); n != 2 {
			t.Fatalf("late deliveries %d, want one opened and one resolved", n)
		}
		if n := count(`SELECT count(*) FROM incidents WHERE monitor_id = $1 AND opened_late AND resolved_late`, replay.ID); n != 1 {
			t.Fatalf("incidents marked late on both ends: %d, want 1", n)
		}
		// A result of the current slot is live: no late marker.
		want("live", send(report(replay, 1, slot(replay, 0), false)), "recorded")
		if n := count(`SELECT count(*) FROM incidents WHERE monitor_id = $1 AND NOT opened_late AND NOT resolved_late AND resolved_at IS NULL`, replay.ID); n != 1 {
			t.Fatalf("a live incident must not be marked late: %d", n)
		}
		if n := count(`SELECT count(*) FROM notification_deliveries WHERE monitor_id = $1 AND kind = 'opened' AND NOT payload ? 'late'`, replay.ID); n != 1 {
			t.Fatalf("live opened deliveries %d, want 1", n)
		}
	})

	t.Run("late alerts: the threshold is the gateway's setting", func(t *testing.T) {
		// Two days is never reached by a slot the gateway accepts, so nothing is late.
		patient := httptest.NewServer(gateway.New(gateway.Deps{
			Limits: openLimits, LateAlertAfter: 48 * time.Hour,
			Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}))
		t.Cleanup(patient.Close)
		sl := slot(tolerant, 60)
		code, b := call(t, patient, token, "/reports", wire.ReportsRequest{Results: []wire.Report{report(tolerant, 1, sl, false)}})
		if code != http.StatusOK {
			t.Fatalf("reports: %d %s", code, b)
		}
		if n := count(`SELECT count(*) FROM notification_deliveries WHERE monitor_id = $1 AND kind = 'opened'`, tolerant.ID); n != 1 {
			t.Fatalf("opened deliveries %d, want 1", n)
		}
		if n := count(`SELECT count(*) FROM notification_deliveries WHERE monitor_id = $1 AND payload ? 'late'`, tolerant.ID); n != 0 {
			t.Fatalf("a result younger than the threshold was marked late (%d)", n)
		}
	})

	t.Run("a closed epoch takes its own slots only", func(t *testing.T) {
		before := slot(m, 10) // inside epoch 1
		if _, err := mon.Pause(ctx, macts(tenantA), m.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := mon.Resume(ctx, macts(tenantA), m.ID); err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, admin, `UPDATE monitor_schedule_epochs SET active_until = now() - interval '2 minutes' WHERE monitor_id = '`+m.ID.String()+`' AND epoch = 1`)
		var e1until int64
		if err := admin.QueryRow(ctx, `SELECT extract(epoch FROM active_until)::bigint FROM monitor_schedule_epochs WHERE monitor_id = $1 AND epoch = 1`, m.ID).Scan(&e1until); err != nil {
			t.Fatal(err)
		}
		if n := count(`SELECT count(*) FROM monitor_schedule_epochs WHERE monitor_id = $1 AND active_until IS NULL AND epoch = 2`, m.ID); n != 1 {
			t.Fatalf("epoch 2 not open")
		}
		after := slot(m, 0) // after epoch 1 ended, on the old grid
		want("replay of epoch 1", send(
			report(m, 1, before, true),
			report(m, 1, after, true),
			report(m, 2, before, true), // epoch 2 did not exist then
		), "recorded", "superseded", "superseded")
	})

	t.Run("metrics", func(t *testing.T) {
		recorded := metricValue(t, reg, "proreta_specula_reports_total", "outcome", "recorded")
		for _, o := range []string{"recorded", "duplicate", "superseded", "expired", "rejected"} {
			if metricValue(t, reg, "proreta_specula_reports_total", "outcome", o) < 1 {
				t.Errorf("reports_total{outcome=%q} was not counted", o)
			}
		}
		if got := metricValue(t, reg, "proreta_specula_report_age_seconds"); got != recorded {
			t.Errorf("report_age_seconds counted %v reports, %v were recorded", got, recorded)
		}
		// The replayed outage (3) and the five-hour-old row are older than ten minutes; a few more
		// sit right at the threshold, so this is a lower bound.
		if got := metricValue(t, reg, "proreta_specula_late_reports_total"); got < 4 || got >= recorded {
			t.Errorf("late_reports_total = %v of %v recorded, want at least 4 and not all", got, recorded)
		}
		if metricValue(t, reg, "proreta_specula_results_total", "outcome", "recorded") != 0 {
			t.Error("slot reports were counted as pull results")
		}
	})

	t.Run("limits and errors", func(t *testing.T) {
		many := make([]wire.Report, wire.MaxResults+1)
		for i := range many {
			many[i] = report(m, 1, slot(m, 1), true)
		}
		if code, _ := call(t, srv, token, "/reports", wire.ReportsRequest{Results: many}); code != http.StatusRequestEntityTooLarge {
			t.Errorf("101 results: %d", code)
		}
		fat := `{"results":[` + strings.Repeat(" ", 300<<10) + `]}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+wire.PathReports, strings.NewReader(fat))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if code := resp.StatusCode; code != http.StatusRequestEntityTooLarge {
			t.Errorf("fat body: %d", code)
		}
		if code, _ := call(t, srv, token, "/reports", wire.ReportsRequest{}); code != http.StatusBadRequest {
			t.Errorf("empty: %d", code)
		}
		if code, _ := call(t, srv, token, "/reports", map[string]any{"results": []any{}, "extra": 1}); code != http.StatusBadRequest {
			t.Errorf("unknown field: %d", code)
		}
		if code, _ := call(t, srv, "spc_nope", "/reports", wire.ReportsRequest{Results: many[:1]}); code != http.StatusUnauthorized {
			t.Errorf("bad token: %d", code)
		}
		if n := count(`SELECT count(*) FROM check_results WHERE monitor_id = $1`, m.ID); n != 8 {
			t.Errorf("rows after the refused calls: %d", n)
		}
	})

	t.Run("at most one row per monitor and slot", func(t *testing.T) {
		if n := count(`SELECT count(*) FROM (SELECT 1 FROM check_results GROUP BY monitor_id, checked_at, location_id HAVING count(*) > 1) d`); n != 0 {
			t.Fatalf("%d slots with several rows", n)
		}
	})
}
