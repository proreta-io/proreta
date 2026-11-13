package migrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// explain runs EXPLAIN (ANALYZE) of q and returns the plan text and the execution time.
func explain(t *testing.T, conn *pgx.Conn, q string) (string, string) {
	t.Helper()
	rows, err := conn.Query(context.Background(), `EXPLAIN (ANALYZE, BUFFERS) `+q)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	return plan, lines[len(lines)-1]
}

// 20 000 schedule rows, 8 000 of them paused or deleted: the shape the old reset left behind (those
// rows stay due) against the parked one the migration produces. The due query of
// claim_due_monitors must use the index and no longer read the parked rows.
func TestIntegrationDueQueryWithParkedMonitors(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("dueperf"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`CREATE TEMP TABLE seed AS SELECT gen_random_uuid() AS id, g FROM generate_series(1, 20000) AS g`,
		`INSERT INTO monitors (id, tenant_id, name, config, paused, deleted_at, project_id)
		   SELECT id, '`+tenantA+`', 'm', '{"url":"https://example.com/"}', g % 5 = 0, CASE WHEN g % 5 = 1 THEN now() END,
		          `+defaultProject(tenantA)+` FROM seed`,
		`INSERT INTO monitor_locations (tenant_id, monitor_id, location_id) SELECT '`+tenantA+`', id, '`+defaultLocation+`' FROM seed`,
		// The old schedule: running monitors due within the next minute (a few already due), parked
		// ones due for up to an hour, as the old reset left them.
		`INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at)
		   SELECT '`+tenantA+`', id, '`+defaultLocation+`',
		          CASE WHEN g % 5 IN (0, 1) THEN now() - random() * interval '1 hour'
		               ELSE now() + (g % 60) * interval '1 second' - interval '2 seconds' END
		   FROM seed`,
		`DROP INDEX monitor_location_state_due_idx`,
		`CREATE INDEX monitor_location_state_due_idx ON monitor_location_state (next_run_at) WHERE lease_token IS NULL`,
		`ANALYZE monitor_location_state`, `ANALYZE monitors`,
	)
	var code string
	if err := admin.QueryRow(ctx, `SELECT code FROM probe_locations WHERE id = $1`, defaultLocation).Scan(&code); err != nil {
		t.Fatal(err)
	}
	claim := `SELECT count(*) FROM claim_due_monitors(100, '` + code + `')`
	// The due part of claim_due_monitors, as written in the function.
	due := `SELECT s.monitor_id FROM monitor_location_state AS s
		JOIN monitors AS m ON m.tenant_id = s.tenant_id AND m.id = s.monitor_id
		WHERE s.next_run_at <= now() AND (s.lease_token IS NULL OR s.lease_until <= now())
		  AND m.type IN ('http', 'tcp') AND NOT m.paused AND m.deleted_at IS NULL
		ORDER BY s.next_run_at LIMIT 100`

	_, before := explain(t, admin, due)
	beforePlan, _ := explain(t, admin, due)
	t.Logf("before (parked rows due, partial index):\n%s", beforePlan)

	pgtest.MustExec(t, admin,
		`UPDATE monitor_location_state AS s SET next_run_at = 'infinity' FROM monitors AS m
		   WHERE m.tenant_id = s.tenant_id AND m.id = s.monitor_id AND (m.paused OR m.deleted_at IS NOT NULL)`,
		`DROP INDEX monitor_location_state_due_idx`,
		`CREATE INDEX monitor_location_state_due_idx ON monitor_location_state (next_run_at)`,
		`ANALYZE monitor_location_state`)
	afterPlan, after := explain(t, admin, due)
	t.Logf("before: %s\nafter:  %s\nafter plan:\n%s", before, after, afterPlan)
	if !strings.Contains(afterPlan, "monitor_location_state_due_idx") {
		t.Errorf("the due query does not use the due index:\n%s", afterPlan)
	}
	start := time.Now()
	var n int
	if err := admin.QueryRow(ctx, claim).Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("claim_due_monitors(100): %d rows in %s", n, time.Since(start))
	if n == 0 {
		t.Fatal("nothing claimed")
	}
}

// The migration itself parks the rows that exist: a paused or deleted monitor's schedule row is
// infinity after the upgrade. Covered by the reset test for new changes; this checks the claim
// ignores a parked row and still takes a running one.
func TestIntegrationClaimSkipsParkedMonitors(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("claimparked"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	worker := env.Conn(t, env.WorkerURL)
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	live := seedMonitor(t, admin, tenantA, "now() - interval '1 minute'", false, false)
	seedMonitor(t, admin, tenantA, "'infinity'", true, false)
	seedMonitor(t, admin, tenantA, "'infinity'", false, true)
	var code string
	if err := admin.QueryRow(ctx, `SELECT code FROM probe_locations WHERE id = $1`, defaultLocation).Scan(&code); err != nil {
		t.Fatal(err)
	}
	rows, _ := worker.Query(ctx, `SELECT monitor_id::text FROM claim_due_monitors(10, $1)`, code)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) != 1 || ids[0] != live {
		t.Fatalf("claimed %v (err %v), want only %s", ids, err, live)
	}
}

// The completeness check of detach_expired_partition on a partition with realistic row counts
// (50 monitors, one check a minute for 20 days: 1.44 million rows): complete rollups detach it, a
// missing hour or a late row keep it, and the check is timed against the per-hour grouping it
// replaced.
func TestIntegrationDetachCheckOnLargePartition(t *testing.T) {
	if testing.Short() {
		t.Skip("generates 1.4 million rows")
	}
	env := pgtest.New(t, pgtest.DBName("detachbig"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	worker := env.Conn(t, env.WorkerURL)
	pgtest.MustExec(t, admin, `SET TimeZone = 'UTC'`, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	var dbNow time.Time
	if err := admin.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	start := monthStart(dbNow, -3)
	name := monthPartition(t, admin, start)
	var monitors []string
	for range 50 {
		monitors = append(monitors, seedMonitor(t, admin, tenantA, "'infinity'", false, false))
	}
	pgtest.MustExec(t, admin, `INSERT INTO check_results (tenant_id, monitor_id, location_id, checked_at, ok, latency_ms)
		SELECT '`+tenantA+`', m.id, '`+defaultLocation+`', $$`+start.Format(time.RFC3339)+`$$::timestamptz + g * interval '1 minute', true, 30
		FROM monitors AS m, generate_series(0, 28799) AS g WHERE m.tenant_id = '`+tenantA+`'`,
		`ANALYZE check_results`)
	var rowsN int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM `+name).Scan(&rowsN); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Exec(ctx, `SELECT compute_rollups($1, $2)`, start, start.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	detach := func() (bool, time.Duration) {
		t.Helper()
		begin := time.Now()
		var ok bool
		if err := worker.QueryRow(ctx, `SELECT detach_expired_partition($1)`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok, time.Since(begin)
	}

	// What the check used to cost: group the partition by monitor, location and hour.
	begin := time.Now()
	var old bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM (
		SELECT c.monitor_id, c.location_id, date_trunc('hour', c.checked_at) AS hour, count(*) AS n
		FROM check_results AS c WHERE c.checked_at >= $1 AND c.checked_at < $2 GROUP BY 1, 2, 3) AS raw
		LEFT JOIN result_rollups_hourly AS r ON r.monitor_id = raw.monitor_id AND r.location_id = raw.location_id AND r.hour = raw.hour
		WHERE r.total IS DISTINCT FROM raw.n)`, start, start.AddDate(0, 1, 0)).Scan(&old); err != nil || old {
		t.Fatalf("old check: incomplete = %v (err %v), want complete", old, err)
	}
	oldTook := time.Since(begin)

	// A missing hour and a late row keep the partition.
	pgtest.MustExec(t, admin, `DELETE FROM result_rollups_hourly WHERE monitor_id = '`+monitors[0]+`' AND hour = $$`+start.Add(5*time.Hour).Format(time.RFC3339)+`$$`)
	ok, newTook := detach() // the check alone: a kept partition has no DETACH in it
	if ok {
		t.Fatal("partition with a missing rollup hour was detached")
	}
	if _, err := worker.Exec(ctx, `SELECT compute_rollups($1, $2)`, start, start.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `INSERT INTO check_results (tenant_id, monitor_id, location_id, checked_at, ok, latency_ms)
		VALUES ('`+tenantA+`', '`+monitors[1]+`', '`+defaultLocation+`', $$`+start.Add(30*time.Second).Format(time.RFC3339)+`$$, true, 30)`)
	if ok, _ := detach(); ok {
		t.Fatal("partition with a late row was detached")
	}
	if _, err := worker.Exec(ctx, `SELECT compute_rollups($1, $2)`, start, start.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	ok, _ = detach()
	if !ok {
		t.Fatal("complete partition was not detached")
	}
	t.Logf("%d rows: old per-hour check %s, new check %s", rowsN+1, oldTook, newTook)
}
