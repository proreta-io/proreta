package specula_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// Two instance ids saying hello within three minutes are a duplicate; a restart (same id), a
// replaced pod after three minutes and an executor that sends no id are not. The clock offset of
// the latest hello is stored, and a hello without one clears it.
func TestIntegrationHelloDuplicateInstanceAndClockOffset(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculadup"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	svc := specula.NewService(env.Pool(t, env.APIURL, 4))
	worker := env.Pool(t, env.WorkerURL, 4)
	a := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, _, err := svc.Create(ctx, a, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	hello := func(instance string, offset *int64) bool {
		t.Helper()
		dup, _, err := specula.Hello(ctx, worker, s.ID, specula.HelloInput{Version: "v", Hostname: "h", InstanceID: instance, ClockOffsetMs: offset})
		if err != nil {
			t.Fatal(err)
		}
		return dup
	}
	flagged := func() bool {
		t.Helper()
		got, err := svc.Get(ctx, a.TenantID, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Duplicate(time.Now())
	}
	age := func(d string) {
		t.Helper()
		pgtest.MustExec(t, admin, `UPDATE specula_agents SET last_hello_at = now() - interval '`+d+`', duplicate_instance_at = NULL WHERE id = '`+s.ID.String()+`'`)
	}

	off := int64(-45_000)
	if hello("", nil) || hello("aaa", &off) || hello("aaa", &off) || flagged() {
		t.Fatal("first hellos and a restart with the same id must not be a duplicate")
	}
	got, err := svc.Get(ctx, a.TenantID, s.ID)
	if err != nil || got.ClockOffsetMs == nil || *got.ClockOffsetMs != -45_000 {
		t.Fatalf("clock offset %+v %v", got.ClockOffsetMs, err)
	}

	age("4 minutes") // a replaced pod: the old one has been silent for more than 3 minutes
	if hello("bbb", nil) || flagged() {
		t.Fatal("a new id after 3 minutes must not be a duplicate")
	}
	if got, _ = svc.Get(ctx, a.TenantID, s.ID); got.ClockOffsetMs != nil {
		t.Fatal("a hello without an offset must clear it")
	}

	if !hello("ccc", nil) || !flagged() {
		t.Fatal("a different id right after another hello must be flagged")
	}
	if !hello("bbb", nil) {
		t.Fatal("the other executor's next hello is flagged too")
	}

	age("1 minute")
	if hello("bbb", nil) || flagged() {
		t.Fatal("the same id again, with the flag cleared, is quiet")
	}
	pgtest.MustExec(t, admin, `UPDATE specula_agents SET duplicate_instance_at = now() - interval '4 minutes' WHERE id = '`+s.ID.String()+`'`)
	if flagged() {
		t.Fatal("the flag expires after 3 minutes")
	}
}
