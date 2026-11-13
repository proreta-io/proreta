package jobs_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationSpeculaOffline walks one Specula through silent, back, silent and revoked.
func TestIntegrationSpeculaOffline(t *testing.T) {
	e := newEnv(t, "jobsspeculaoffline")
	reg := prometheus.NewRegistry()
	m := jobs.NewMetrics(reg)
	log := slog.New(slog.NewJSONHandler(e, nil))
	pool := e.Pool(t, e.WorkerURL, 2)
	sched := scheduler.New()
	tasks := jobs.DefaultTasks(m, log, monitors.NewService(pool, sched, incidents.NewService(sched)))
	h := jobs.NewHousekeeper(pool, tasks, m, log, nil)
	var task jobs.Task
	for _, tk := range tasks {
		if tk.Name == "specula_mark_unreachable" {
			task = tk
		}
	}
	d := e.deliverer(prometheus.NewRegistry(), nil)

	s, _, err := specula.NewService(e.Pool(t, e.APIURL, 2)).Create(e.ctx,
		specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: uuid.MustParse(userID)}, specula.CreateInput{Name: "Office <net>", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	mon := e.monitor(tenantA, "Printer")
	pgtest.MustExec(t, e.admin,
		fmt.Sprintf(`INSERT INTO monitor_locations (tenant_id, monitor_id, location_id) VALUES ('%s', '%s', '%s')`, tenantA, mon, s.LocationID),
		`UPDATE notification_channels SET events = ARRAY['agent_offline', 'agent_online'] WHERE tenant_id = '`+tenantA+`'`)
	seen := func(interval string) {
		pgtest.MustExec(t, e.admin, fmt.Sprintf(`UPDATE specula_agents SET last_seen_at = now() - interval '%s' WHERE id = '%s'`, interval, s.ID))
	}
	run := func() {
		t.Helper()
		if got := h.RunTask(e.ctx, task); got != "ok" {
			t.Fatalf("task: %s", got)
		}
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := e.admin.QueryRow(e.ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const open = `SELECT count(*) FROM incidents WHERE specula_id = $1 AND resolved_at IS NULL AND monitor_id IS NULL AND reason = 'agent_offline'`

	run() // never connected: nothing to report
	if count(`SELECT count(*) FROM incidents WHERE specula_id = $1`, s.ID) != 0 {
		t.Fatal("a Specula that never connected opened an incident")
	}

	seen("10 minutes")
	run()
	run() // idempotent: one open incident, one delivery
	if count(open, s.ID) != 1 || count(`SELECT count(*) FROM notification_deliveries WHERE specula_id = $1 AND kind = 'agent_offline'`, s.ID) != 1 {
		t.Fatal("silent Specula: want one open incident and one agent_offline delivery")
	}
	if count(`SELECT count(*) FROM incident_monitors WHERE monitor_id = $1`, mon) != 1 {
		t.Fatal("the incident does not list the monitor of the Specula")
	}
	if got := metric(t, reg, "proreta_jobs_specula_agents", "offline"); got != 1 {
		t.Fatalf("offline gauge = %v", got)
	}
	if n := d.RunOnce(e.ctx); n != 1 {
		t.Fatalf("claimed %d", n)
	}
	p := e.slack.sent()
	if len(p) != 1 || !strings.HasPrefix(p[0].text, ":red_circle: Specula *Office &lt;net&gt;* is offline\n1 monitor shows unknown") ||
		!strings.Contains(p[0].text, "<https://proreta.example.com/specula|Open in Proreta>") {
		t.Fatalf("offline post = %+v", p)
	}

	seen("0 seconds")
	run()
	if count(open, s.ID) != 0 || count(`SELECT count(*) FROM incidents WHERE specula_id = $1 AND resolve_reason = 'recovered'`, s.ID) != 1 {
		t.Fatal("a Specula that is back did not resolve its incident")
	}
	if n := d.RunOnce(e.ctx); n != 1 {
		t.Fatalf("claimed %d", n)
	}
	if p := e.slack.sent(); len(p) != 2 || !strings.HasPrefix(p[1].text, ":large_green_circle: Specula *Office &lt;net&gt;* is back online after ") {
		t.Fatalf("online post = %+v", p)
	}
	if got := metric(t, reg, "proreta_jobs_specula_incidents_total", "offline"); got != 1 {
		t.Fatalf("offline counter = %v", got)
	}
	if got := metric(t, reg, "proreta_jobs_specula_incidents_total", "online"); got != 1 {
		t.Fatalf("online counter = %v", got)
	}

	seen("10 minutes")
	run()
	pgtest.MustExec(t, e.admin, fmt.Sprintf(`UPDATE specula_agents SET revoked_at = now() WHERE id = '%s'`, s.ID))
	run() // revoking is deliberate: the incident ends without an agent_online message
	if count(open, s.ID) != 0 || count(`SELECT count(*) FROM incidents WHERE specula_id = $1 AND resolve_reason = 'revoked'`, s.ID) != 1 ||
		count(`SELECT count(*) FROM notification_deliveries WHERE specula_id = $1 AND kind = 'agent_online'`, s.ID) != 1 {
		t.Fatal("revoked Specula: incident not resolved as revoked, or an extra agent_online was queued")
	}
}
