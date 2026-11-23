package specula_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The rows of specula_slot_losses follow the delta rules: the first hello of a process counts
// everything, the next ones the growth, a counter that went down or a new instance id starts at
// zero again, and a hello without counters changes nothing. Another tenant sees no row.
func TestIntegrationHelloRecordsSlotLossDeltas(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculaloss"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal'), ('`+tenantB+`', 'b', 'B', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	api, worker := env.Pool(t, env.APIURL, 4), env.Pool(t, env.WorkerURL, 4)
	svc := specula.NewService(api)
	a := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	hello := func(instance string, counters map[string]int64) []specula.Loss {
		t.Helper()
		_, losses, err := specula.Hello(ctx, worker, s.ID, specula.HelloInput{Version: "v", Hostname: "h", InstanceID: instance, SlotCounters: counters})
		if err != nil {
			t.Fatal(err)
		}
		return losses
	}
	total := func(reason string) (n int64) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT coalesce(sum(count), 0) FROM specula_slot_losses WHERE specula_id = $1 AND reason = $2`, s.ID, reason).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	want := func(step, reason string, n int64) {
		t.Helper()
		if got := total(reason); got != n {
			t.Fatalf("%s: %s = %d, want %d", step, reason, got, n)
		}
	}

	if losses := hello("i1", nil); len(losses) != 0 {
		t.Fatalf("a hello without counters recorded %v", losses)
	}
	want("plain hello", "busy", 0)

	if losses := hello("i1", map[string]int64{"busy": 3, "tooOld": 2}); len(losses) != 2 {
		t.Fatalf("first hello with counters: %v", losses)
	}
	want("first", "busy", 3)
	want("first", "tooOld", 2)

	hello("i1", map[string]int64{"busy": 5, "late": 1, "tooOld": 2})
	want("same instance", "busy", 5)
	want("same instance", "late", 1)
	want("same instance", "tooOld", 2)

	if losses := hello("i1", map[string]int64{"busy": 5, "late": 1, "tooOld": 2}); len(losses) != 0 {
		t.Fatalf("a repeated hello recorded %v", losses)
	}

	hello("i1", map[string]int64{"busy": 1, "late": 1, "tooOld": 2}) // busy went down: counted from zero
	want("decrease", "busy", 6)
	want("decrease", "late", 1)

	// A new process within three minutes of the old one's hello is a duplicate and is not counted;
	// the next hello of the process counts everything it has (6 below), so nothing is lost.
	hello("i2", map[string]int64{"busy": 4})
	want("new instance", "busy", 6)
	hello("i2", nil) // a plain hello must not forget the last values
	hello("i2", map[string]int64{"busy": 6})
	want("plain hello in between", "busy", 12)

	hello("i3", nil)                         // a new process without counters, then counters above the old values
	hello("i3", map[string]int64{"busy": 9}) // counts 9, not 3
	want("instance changed by a plain hello", "busy", 21)

	var rows int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM specula_slot_losses WHERE count <= 0`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows with a count of zero: %d %v", rows, err)
	}

	count := func(tenant string) (n int) {
		err := db.WithTenantTx(ctx, api, uuid.MustParse(tenant), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM specula_slot_losses`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(tenantA) == 0 || count(tenantB) != 0 {
		t.Fatalf("rows seen by the owner %d, by another tenant %d", count(tenantA), count(tenantB))
	}
}

// Two executors on one token flip the instance id on every hello: none of those hellos adds
// anything, however long it goes on.
func TestIntegrationSharedTokenDoesNotInflateSlotLosses(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculashared"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	api, worker := env.Pool(t, env.APIURL, 4), env.Pool(t, env.WorkerURL, 4)
	s, _, err := specula.NewService(api).Create(ctx, specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	hello := func(instance string, busy int64) {
		t.Helper()
		if _, _, err := specula.Hello(ctx, worker, s.ID, specula.HelloInput{Version: "v", Hostname: "h", InstanceID: instance, SlotCounters: map[string]int64{"busy": busy}}); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		hello("e1", 10)
		hello("e2", 10)
	}
	var n int64
	if err := admin.QueryRow(ctx, `SELECT coalesce(sum(count), 0) FROM specula_slot_losses WHERE specula_id = $1`, s.ID).Scan(&n); err != nil || n > 10 {
		t.Fatalf("phantom losses after eight alternating hellos: %d (%v), want at most the 10 of the first hello", n, err)
	}
}

// A counter that is not a whole number in 0..1e15 is ignored without failing the hello, one
// hello adds a bounded amount, and the stored sum saturates instead of overflowing bigint.
func TestIntegrationSlotLossesIgnoreBadCounters(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculabad"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	api, workerConn := env.Pool(t, env.APIURL, 4), env.Conn(t, env.WorkerURL)
	s, _, err := specula.NewService(api).Create(ctx, specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	call := func(raw string) {
		t.Helper()
		if _, err := workerConn.Exec(ctx, `SELECT * FROM specula_record_slot_losses($1, $2::jsonb)`, s.ID, raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	total := func(reason string) (n int64) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT coalesce(sum(count), 0) FROM specula_slot_losses WHERE specula_id = $1 AND reason = $2`, s.ID, reason).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	call(`{"busy": "abc", "late": 1.5, "tooOld": -3, "rejected": null, "bufferSize": 9223372036854775807, "bufferAge": 2e15, "superseded": 4}`)
	for _, r := range []string{"busy", "late", "tooOld", "rejected", "bufferSize", "bufferAge"} {
		if n := total(r); n != 0 {
			t.Errorf("%s: %d recorded from a bad value", r, n)
		}
	}
	if n := total("superseded"); n != 4 {
		t.Errorf("a good counter next to bad ones: %d, want 4", n)
	}
	// One hello adds at most 1e7; the sum stops at 1e15.
	call(`{"unreadable": 1000000000000000}`)
	if n := total("unreadable"); n != 10_000_000 {
		t.Errorf("one huge counter added %d, want 10000000", n)
	}
	pgtest.MustExec(t, admin, `UPDATE specula_slot_losses SET count = 999999999995000 WHERE reason = 'unreadable'`,
		`UPDATE specula_agents SET slot_counters = NULL`)
	call(`{"unreadable": 1000000000000000}`)
	if n := total("unreadable"); n != 1_000_000_000_000_000 {
		t.Errorf("saturated sum = %d, want 1e15", n)
	}
}
