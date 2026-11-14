package migrations_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

type epochRow struct {
	epoch    int
	anchor   int64
	interval int
	open     bool
}

func epochsOf(t *testing.T, c *pgx.Conn, monitor, location string) []epochRow {
	t.Helper()
	rows, _ := c.Query(context.Background(), `SELECT epoch, anchor, interval_seconds, active_until IS NULL
		FROM monitor_schedule_epochs WHERE monitor_id = $1 AND location_id = $2 ORDER BY epoch`, monitor, location)
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (epochRow, error) {
		var e epochRow
		return e, r.Scan(&e.epoch, &e.anchor, &e.interval, &e.open)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// seedSpecula creates a Specula of the tenant with its own location of kind agent.
func seedSpecula(t *testing.T, admin *pgx.Conn, tenant string) (specula, location string) {
	t.Helper()
	specula, location = uuid.NewString(), uuid.NewString()
	pgtest.MustExec(t, admin,
		fmt.Sprintf(`INSERT INTO probe_locations (id, code, name, provider, region, kind) VALUES ('%s', 'sp-%s', 'sp', 'specula', 'x', 'agent')`, location, location[:8]),
		fmt.Sprintf(`INSERT INTO specula_agents (tenant_id, id, name, token_hash, location_id) VALUES ('%s', '%s', 'sp-%s', decode(md5('%s') || md5('%s'), 'hex'), '%s')`,
			tenant, specula, specula[:8], specula, location, location))
	return specula, location
}

// placeAt puts a monitor of the default location at the location the way monitor_set_location
// does: the state of the other locations goes, a state row for the new one appears.
func placeAt(t *testing.T, admin *pgx.Conn, tenant, monitor, location string) {
	t.Helper()
	pgtest.MustExec(t, admin,
		fmt.Sprintf(`DELETE FROM monitor_location_state WHERE monitor_id = '%s' AND location_id <> '%s'`, monitor, location),
		fmt.Sprintf(`INSERT INTO monitor_locations (tenant_id, monitor_id, location_id) VALUES ('%s', '%s', '%s') ON CONFLICT DO NOTHING`, tenant, monitor, location),
		fmt.Sprintf(`INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at) VALUES ('%s', '%s', '%s', now()) ON CONFLICT DO NOTHING`, tenant, monitor, location))
}

// Pause, resume, an edit that changes the check, a move and a delete keep the epochs of a monitor
// at a Specula in step; the cloud location never gets one.
func TestIntegrationScheduleEpochs(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("scheduleepochs"))
	admin := env.AdminConn(t)
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	_, loc := seedSpecula(t, admin, tenantA)
	_, loc2 := seedSpecula(t, admin, tenantA)
	m := seedMonitor(t, admin, tenantA, "now()", false, false)
	ctx := context.Background()

	if got := epochsOf(t, admin, m, defaultLocation); len(got) != 0 {
		t.Fatalf("the cloud location got epochs: %v", got)
	}
	placeAt(t, admin, tenantA, m, loc)
	e := epochsOf(t, admin, m, loc)
	if len(e) != 1 || e[0].epoch != 1 || !e[0].open || e[0].interval != 60 {
		t.Fatalf("after placing: %v", e)
	}
	first := e[0].anchor
	var phase, now int64
	if err := admin.QueryRow(ctx, `SELECT $1::bigint - ceil(extract(epoch FROM now()))::bigint, ceil(extract(epoch FROM now()))::bigint`, first).Scan(&phase, &now); err != nil {
		t.Fatal(err)
	}
	if phase < 0 || phase >= 60 {
		t.Fatalf("anchor is %d seconds after the start, want 0..59", phase)
	}
	// Stable: the same monitor and location always get the same phase.
	var again int64
	if err := admin.QueryRow(ctx, `SELECT specula_epoch_open('`+tenantA+`', $1, $2)`, m, loc).Scan(&again); err != nil || again != 1 {
		t.Fatalf("a repeated open = %d, %v; want the open epoch 1", again, err)
	}

	// An edit that does not change the check keeps the epoch; one that does replaces it.
	pgtest.MustExec(t, admin, `UPDATE monitors SET name = 'renamed', failure_threshold = 2 WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); len(e) != 1 {
		t.Fatalf("a rename changed the epochs: %v", e)
	}
	pgtest.MustExec(t, admin, `UPDATE monitors SET config = jsonb_set(config, '{url}', '"https://example.com/new"') WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); len(e) != 2 || e[0].open || !e[1].open || e[1].epoch != 2 {
		t.Fatalf("after a new url: %v", e)
	}

	// Pause closes, an edit of a paused monitor changes nothing, resume opens a new epoch.
	pgtest.MustExec(t, admin, `UPDATE monitors SET paused = true WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); e[1].open {
		t.Fatalf("pause left an epoch open: %v", e)
	}
	pgtest.MustExec(t, admin, `UPDATE monitors SET config = jsonb_set(config, '{url}', '"https://example.com/paused"') WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); len(e) != 2 || e[1].open {
		t.Fatalf("an edit while paused: %v", e)
	}
	pgtest.MustExec(t, admin, `UPDATE monitors SET paused = false WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); len(e) != 3 || !e[2].open || e[2].epoch != 3 {
		t.Fatalf("after resume: %v", e)
	}

	// Closed epochs end before or when the next one starts.
	var overlap int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM monitor_schedule_epochs a JOIN monitor_schedule_epochs b
		ON a.monitor_id = b.monitor_id AND a.location_id = b.location_id AND a.epoch + 1 = b.epoch AND a.active_until > b.active_from`).Scan(&overlap); err != nil || overlap != 0 {
		t.Fatalf("overlapping epochs: %d, %v", overlap, err)
	}

	// A move closes the epoch at the old location and opens one at the new.
	placeAt(t, admin, tenantA, m, loc2)
	if e = epochsOf(t, admin, m, loc); e[len(e)-1].open {
		t.Fatalf("the epoch at the old location is still open: %v", e)
	}
	if e = epochsOf(t, admin, m, loc2); len(e) != 1 || !e[0].open {
		t.Fatalf("the new location: %v", e)
	}
	// Back to the cloud location: no epoch there, the Specula's is closed.
	placeAt(t, admin, tenantA, m, defaultLocation)
	if e = epochsOf(t, admin, m, loc2); e[0].open {
		t.Fatalf("moved to the cloud, epoch still open: %v", e)
	}
	if e = epochsOf(t, admin, m, defaultLocation); len(e) != 0 {
		t.Fatalf("cloud epochs: %v", e)
	}

	// Delete closes, and a deleted monitor is not reopened by an edit.
	placeAt(t, admin, tenantA, m, loc)
	pgtest.MustExec(t, admin, `UPDATE monitors SET deleted_at = now() WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); e[len(e)-1].open {
		t.Fatalf("delete left an epoch open: %v", e)
	}
	pgtest.MustExec(t, admin, `UPDATE monitors SET config = jsonb_set(config, '{url}', '"https://example.com/gone"') WHERE id = '`+m+`'`)
	if e = epochsOf(t, admin, m, loc); e[len(e)-1].open {
		t.Fatalf("an edit reopened a deleted monitor: %v", e)
	}

	// A heartbeat is never scheduled at a Specula.
	hb := seedMonitor(t, admin, tenantA, "now()", false, false)
	pgtest.MustExec(t, admin, `UPDATE monitors SET type = 'heartbeat', interval_seconds = 120, failure_threshold = 1, config = '{"graceSeconds":60}' WHERE id = '`+hb+`'`)
	placeAt(t, admin, tenantA, hb, loc)
	if e = epochsOf(t, admin, hb, loc); len(e) != 0 {
		t.Fatalf("heartbeat epochs: %v", e)
	}
}

// Epochs follow tenant isolation, and the runtime roles can read but never write or delete them.
func TestIntegrationScheduleEpochGrants(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("scheduleepochgrants"))
	admin := env.AdminConn(t)
	ctx := context.Background()
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`)
	_, loc := seedSpecula(t, admin, tenantA)
	m := seedMonitor(t, admin, tenantA, "now()", false, false)
	placeAt(t, admin, tenantA, m, loc)

	worker := env.Conn(t, env.WorkerURL)
	count := func(tenant string) int {
		tx, err := worker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM monitor_schedule_epochs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if a, b := count(tenantA), count(tenantB); a != 1 || b != 0 {
		t.Fatalf("worker sees %d epochs of its tenant and %d of the other", a, b)
	}
	for _, role := range []struct {
		name string
		conn *pgx.Conn
	}{{"proreta_worker", worker}, {"proreta_api", env.Conn(t, env.APIURL)}} {
		for _, stmt := range []string{
			`DELETE FROM monitor_schedule_epochs`,
			`UPDATE monitor_schedule_epochs SET active_until = now()`,
			`INSERT INTO monitor_schedule_epochs (tenant_id, monitor_id, location_id, epoch, anchor, interval_seconds, active_from) VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 9, 1, 60, now())`,
			`SELECT hour FROM rollup_dirty_hours`,
			`INSERT INTO rollup_dirty_hours (hour, seq) VALUES (date_trunc('hour', now() - interval '3 hours'), 1)`,
		} {
			if _, err := role.conn.Exec(ctx, stmt); err == nil {
				t.Errorf("%s may run %q", role.name, stmt)
			}
		}
	}
	if _, err := worker.Exec(ctx, `SELECT epoch FROM monitor_schedule_epochs`); err != nil {
		t.Errorf("the worker cannot read epochs: %v", err)
	}
}

// specula_schedule lists the open epochs of one Specula under the conditions of the claim.
func TestIntegrationSpeculaSchedule(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculaschedule"))
	admin := env.AdminConn(t)
	ctx := context.Background()
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`)
	sp, loc := seedSpecula(t, admin, tenantA)
	spB, _ := seedSpecula(t, admin, tenantB)
	m1 := seedMonitor(t, admin, tenantA, "now()", false, false)
	m2 := seedMonitor(t, admin, tenantA, "now()", false, false)
	pgtest.MustExec(t, admin, `UPDATE monitors SET type = 'tcp', config = '{"host":"db.internal","port":5432}' WHERE id = '`+m2+`'`)
	placeAt(t, admin, tenantA, m1, loc)
	placeAt(t, admin, tenantA, m2, loc)

	list := func(tenant, specula string) map[string]string {
		rows, _ := admin.Query(ctx, `SELECT monitor_id::text, monitor_type || ' ' || url || ' ' || host || ':' || port FROM specula_schedule($1, $2)`, tenant, specula)
		out := map[string]string{}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
			var kv [2]string
			return kv, r.Scan(&kv[0], &kv[1])
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, kv := range got {
			out[kv[0]] = kv[1]
		}
		return out
	}
	if got := list(tenantA, sp); len(got) != 2 || got[m2] != "tcp  db.internal:5432" {
		t.Fatalf("schedule = %v", got)
	}
	if got := list(tenantB, sp); len(got) != 0 {
		t.Fatalf("another tenant read the schedule: %v", got)
	}
	if got := list(tenantA, spB); len(got) != 0 {
		t.Fatalf("a Specula of another tenant: %v", got)
	}
	pgtest.MustExec(t, admin, `UPDATE monitors SET paused = true WHERE id = '`+m1+`'`)
	if got := list(tenantA, sp); len(got) != 1 {
		t.Fatalf("a paused monitor is scheduled: %v", got)
	}
	pgtest.MustExec(t, admin, `UPDATE specula_agents SET revoked_at = now() WHERE id = '`+sp+`'`)
	if got := list(tenantA, sp); len(got) != 0 {
		t.Fatalf("a revoked Specula got a schedule: %v", got)
	}
}

// The claim leaves a Specula in schedule mode alone and keeps tcp from one that did not announce it.
func TestIntegrationClaimByCapability(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("claimcapability"))
	admin := env.AdminConn(t)
	ctx := context.Background()
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`)
	sp, loc := seedSpecula(t, admin, tenantA)
	httpM := seedMonitor(t, admin, tenantA, "now()", false, false)
	tcpM := seedMonitor(t, admin, tenantA, "now()", false, false)
	pgtest.MustExec(t, admin, `UPDATE monitors SET type = 'tcp', config = '{"host":"db.internal","port":5432}' WHERE id = '`+tcpM+`'`)
	placeAt(t, admin, tenantA, httpM, loc)
	placeAt(t, admin, tenantA, tcpM, loc)
	var code string
	if err := admin.QueryRow(ctx, `SELECT code FROM probe_locations WHERE id = $1`, loc).Scan(&code); err != nil {
		t.Fatal(err)
	}
	worker := env.Conn(t, env.WorkerURL)
	claimed := func() map[string]bool {
		pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET lease_token = NULL, lease_until = NULL, next_run_at = now() WHERE location_id = '`+loc+`'`)
		rows, _ := worker.Query(ctx, `SELECT monitor_id::text FROM claim_due_monitors(10, $1)`, code)
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, id := range ids {
			out[id] = true
		}
		return out
	}
	set := func(caps string) {
		pgtest.MustExec(t, admin, `UPDATE specula_agents SET capabilities = '`+caps+`' WHERE id = '`+sp+`'`)
	}
	for _, c := range []struct {
		caps      string
		http, tcp bool
	}{
		{"{}", true, false},                // an older executor: http only
		{"{tcp}", true, true},              // pull mode with tcp
		{"{schedule/1}", false, false},     // schedule mode: nothing to claim
		{"{tcp,schedule/1}", false, false}, // also with tcp
	} {
		set(c.caps)
		got := claimed()
		if got[httpM] != c.http || got[tcpM] != c.tcp {
			t.Errorf("capabilities %s: claimed http=%t tcp=%t, want http=%t tcp=%t", c.caps, got[httpM], got[tcpM], c.http, c.tcp)
		}
	}
}

// Dirty hours: only a closed hour of the last 7 days is marked, a second mark changes seq.
func TestIntegrationRollupMarkDirty(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("rollupdirty"))
	admin := env.AdminConn(t)
	ctx := context.Background()
	worker := env.Conn(t, env.WorkerURL)
	mark := func(expr string) bool {
		var ok bool
		if err := worker.QueryRow(ctx, `SELECT rollup_mark_dirty(`+expr+`)`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for expr, want := range map[string]bool{
		"now()":                      false, // the open hour
		"now() + interval '5 hours'": false,
		"now() - interval '8 days'":  false,
		"NULL":                       false,
		"date_trunc('hour', now()) - interval '5 hours' + interval '7 min'": true,
		"date_trunc('hour', now()) - interval '5 hours' + interval '9 min'": true, // the same hour
		"now() - interval '30 hours'":                                       true,
	} {
		if got := mark(expr); got != want {
			t.Errorf("rollup_mark_dirty(%s) = %t, want %t", expr, got, want)
		}
	}
	var n int
	var minSeq, maxSeq int64
	if err := admin.QueryRow(ctx, `SELECT count(*), min(seq), max(seq) FROM rollup_dirty_hours`).Scan(&n, &minSeq, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if n != 2 || maxSeq <= minSeq {
		t.Fatalf("dirty hours: %d rows, seq %d..%d; want 2 rows, the repeated hour with the newer seq", n, minSeq, maxSeq)
	}
	var seq1, seq2 int64
	q := `SELECT seq FROM rollup_dirty_hours WHERE hour = date_trunc('hour', now(), 'UTC') - interval '5 hours'`
	if err := admin.QueryRow(ctx, q).Scan(&seq1); err != nil {
		t.Fatal(err)
	}
	mark("date_trunc('hour', now()) - interval '5 hours' + interval '1 min'")
	if err := admin.QueryRow(ctx, q).Scan(&seq2); err != nil || seq2 <= seq1 {
		t.Fatalf("a repeated mark kept seq %d (was %d), %v", seq2, seq1, err)
	}
}
