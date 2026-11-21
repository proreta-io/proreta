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

	hello("i2", map[string]int64{"busy": 4}) // a new process
	want("new instance", "busy", 10)
	hello("i2", nil)                         // a plain hello must not forget the last values
	hello("i2", map[string]int64{"busy": 6}) // +2
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
