package migrations_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

type drainEnv struct {
	admin, worker *pgx.Conn
	monitor       string
	hour0         time.Time // start of the open hour
}

func newDrainEnv(t *testing.T, name string) drainEnv {
	t.Helper()
	env := pgtest.New(t, pgtest.DBName(name))
	admin := env.AdminConn(t)
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	var dbNow time.Time
	if err := admin.QueryRow(context.Background(), `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	return drainEnv{admin, env.Conn(t, env.WorkerURL), seedMonitor(t, admin, tenantA, "now() + interval '1 hour'", false, false), dbNow.UTC().Truncate(time.Hour)}
}

func (e drainEnv) rows(t *testing.T, at time.Time, n int) {
	t.Helper()
	for i := range n {
		pgtest.MustExec(t, e.admin, fmt.Sprintf(`INSERT INTO check_results (tenant_id, monitor_id, location_id, checked_at, ok, latency_ms)
			VALUES ('%s', '%s', '%s', '%s', true, 20)`, tenantA, e.monitor, defaultLocation, at.Add(time.Duration(i)*time.Second).Format(time.RFC3339)))
	}
}

func (e drainEnv) drain(t *testing.T, max int) (processed int, remaining int64) {
	t.Helper()
	if err := e.worker.QueryRow(context.Background(), `SELECT processed, remaining FROM rollup_drain_dirty($1)`, max).Scan(&processed, &remaining); err != nil {
		t.Fatalf("rollup_drain_dirty(%d): %v", max, err)
	}
	return
}

func (e drainEnv) rolledTotal(t *testing.T, lo, hi time.Time) (n int) {
	t.Helper()
	if err := e.admin.QueryRow(context.Background(), `SELECT COALESCE(sum(total), 0) FROM result_rollups_hourly WHERE hour >= $1 AND hour < $2`, lo, hi).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return
}

func (e drainEnv) mark(t *testing.T, h time.Time) {
	t.Helper()
	pgtest.MustExec(t, e.admin, fmt.Sprintf(`INSERT INTO rollup_dirty_hours (hour, seq) VALUES ('%s', nextval('rollup_dirty_seq'))`, h.Format(time.RFC3339)))
}

// Late rows in hours the regular run no longer revisits are recomputed from their marks, six
// per run, and the marks are cleared.
func TestIntegrationRollupDrainDirty(t *testing.T) {
	e := newDrainEnv(t, "rollupdrain")
	monthPartition(t, e.admin, monthStart(e.hour0.Add(-10*time.Hour), 0))
	monthPartition(t, e.admin, monthStart(e.hour0.Add(-10*time.Hour), -1))
	if p, r := e.drain(t, 6); p != 0 || r != 0 {
		t.Fatalf("empty drain = (%d, %d), want (0, 0)", p, r)
	}
	first := e.hour0.Add(-30 * time.Hour)
	for i := range 8 {
		h := first.Add(time.Duration(i) * time.Hour)
		e.rows(t, h.Add(time.Minute), 3)
		if i == 0 { // an hour already rolled up whose count grows
			if _, err := e.worker.Exec(context.Background(), `SELECT compute_rollups($1, $2)`, h, h.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			e.rows(t, h.Add(10*time.Minute), 2)
		}
		var ok bool
		if err := e.worker.QueryRow(context.Background(), `SELECT rollup_mark_dirty($1)`, h.Add(5*time.Minute)).Scan(&ok); err != nil || !ok {
			t.Fatalf("mark %d: %t, %v", i, ok, err)
		}
	}
	if got := e.rolledTotal(t, first, e.hour0); got != 3 {
		t.Fatalf("rolled before drain = %d, want 3 (only the first hour, stale)", got)
	}
	if p, r := e.drain(t, 6); p != 6 || r != 2 {
		t.Fatalf("first drain = (%d, %d), want (6, 2)", p, r)
	}
	if p, r := e.drain(t, 6); p != 2 || r != 0 {
		t.Fatalf("second drain = (%d, %d), want (2, 0)", p, r)
	}
	if got, want := e.rolledTotal(t, first, e.hour0), 8*3+2; got != want {
		t.Fatalf("rolled after drain = %d, want %d", got, want)
	}
	if _, err := e.worker.Exec(context.Background(), `SELECT * FROM rollup_drain_dirty(0)`); err == nil {
		t.Error("rollup_drain_dirty(0) succeeded, want an error")
	}
}

// A partition with a dirty hour is kept; after the drain the counts match and it is detached.
func TestIntegrationRetentionKeepsDirtyMonth(t *testing.T) {
	e := newDrainEnv(t, "retentiondirty")
	old := monthStart(e.hour0, -6)
	name := monthPartition(t, e.admin, old)
	h := old.Add(100 * time.Hour)
	e.rows(t, h.Add(time.Minute), 4)
	detach := func() bool {
		t.Helper()
		var ok bool
		if err := e.worker.QueryRow(context.Background(), `SELECT detach_expired_partition($1)`, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if detach() {
		t.Fatal("detached a partition whose rows are not rolled up")
	}
	// The rollup of the hour is stale (4 of 5 rows after the next insert), as when a late row arrived after it.
	if _, err := e.worker.Exec(context.Background(), `SELECT compute_rollups($1, $2)`, h, h.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.rows(t, h.Add(20*time.Minute), 1)
	e.mark(t, h)
	if detach() {
		t.Fatal("detached a partition with a dirty hour")
	}
	if p, r := e.drain(t, 6); p != 1 || r != 0 {
		t.Fatalf("drain = (%d, %d), want (1, 0)", p, r)
	}
	if !detach() {
		t.Fatal("partition not detached after the drain")
	}
}

// A mark made by a transaction that commits while the drain is running survives the drain, and
// the next drain picks up its row.
func TestIntegrationRollupDrainRace(t *testing.T) {
	e := newDrainEnv(t, "rollupdrainrace")
	ctx := context.Background()
	monthPartition(t, e.admin, monthStart(e.hour0.Add(-10*time.Hour), 0))
	monthPartition(t, e.admin, monthStart(e.hour0.Add(-10*time.Hour), -1))
	h := e.hour0.Add(-5 * time.Hour)
	e.rows(t, h.Add(time.Minute), 2)
	e.mark(t, h)

	// The late reporter: a row and a re-mark in a transaction that stays open.
	lateTx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lateTx.Rollback(ctx) }()
	if _, err := lateTx.Exec(ctx, fmt.Sprintf(`INSERT INTO check_results (tenant_id, monitor_id, location_id, checked_at, ok, latency_ms)
		VALUES ('%s', '%s', '%s', '%s', true, 20)`, tenantA, e.monitor, defaultLocation, h.Add(30*time.Minute).Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	if _, err := lateTx.Exec(ctx, `UPDATE rollup_dirty_hours SET seq = nextval('rollup_dirty_seq') WHERE hour = $1`, h); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		var p int
		var r int64
		done <- e.worker.QueryRow(ctx, `SELECT processed, remaining FROM rollup_drain_dirty(6)`).Scan(&p, &r)
	}()
	// The drain computes (without the uncommitted row) and then waits for the row lock of the mark.
	waitFor(t, e.admin, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%rollup_drain_dirty%')`)
	if err := lateTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var left int
	if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM rollup_dirty_hours WHERE hour = $1`, h).Scan(&left); err != nil || left != 1 {
		t.Fatalf("mark after the racing drain: %d, %v; want it kept", left, err)
	}
	if got := e.rolledTotal(t, h, h.Add(time.Hour)); got != 2 {
		t.Fatalf("rolled after the racing drain = %d, want 2 (the late row is not in yet)", got)
	}
	if p, r := e.drain(t, 6); p != 1 || r != 0 {
		t.Fatalf("next drain = (%d, %d), want (1, 0)", p, r)
	}
	if got := e.rolledTotal(t, h, h.Add(time.Hour)); got != 3 {
		t.Fatalf("rolled after the next drain = %d, want 3", got)
	}
}

func waitFor(t *testing.T, c *pgx.Conn, query string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if err := c.QueryRow(context.Background(), query).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition never held: %s", query)
}
