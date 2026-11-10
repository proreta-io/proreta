package migrations_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationAuthLoginTenant checks that auth_login_tenant returns, for memberships never
// used, the user's default-tenant membership if any, otherwise the oldest membership in a live
// tenant (active before suspended, deleted last) or NULL, and that the api still cannot read
// memberships without the tenant set.
func TestIntegrationAuthLoginTenant(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("logintenant"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	const (
		userOne  = "01920000-0000-7000-8000-0000000000c1"
		userNone = "01920000-0000-7000-8000-0000000000c2"
		tenantC  = "01920000-0000-7000-8000-00000000000c"
	)
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B'), ('`+tenantC+`', 'c', 'C')`,
		`INSERT INTO users (id, email) VALUES ('`+userOne+`', 'one@example.com'), ('`+userNone+`', 'none@example.com')`,
		`INSERT INTO memberships (tenant_id, user_id, role, created_at) VALUES
			('`+tenantB+`', '`+userOne+`', 'owner', now() - interval '2 days'),
			('`+tenantA+`', '`+userOne+`', 'owner', now() - interval '1 day'),
			('`+tenantC+`', '`+userOne+`', 'owner', now())`)

	api := env.Conn(t, env.APIURL)
	loginTenant := func(user string) *uuid.UUID {
		t.Helper()
		var id *uuid.UUID
		if err := api.QueryRow(ctx, `SELECT auth_login_tenant($1)`, user).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if got := loginTenant(userOne); got == nil || got.String() != tenantB {
		t.Fatalf("oldest membership: %v, want %s", got, tenantB)
	}
	pgtest.MustExec(t, admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+tenantB+`'`)
	// An active tenant wins over a suspended one.
	if got := loginTenant(userOne); got == nil || got.String() != tenantA {
		t.Fatalf("suspended tenant skipped for an active one: %v, want %s", got, tenantA)
	}
	pgtest.MustExec(t, admin, `UPDATE tenants SET deleted_at = now() WHERE id = '`+tenantB+`'`)
	if got := loginTenant(userOne); got == nil || got.String() != tenantA {
		t.Fatalf("deleted tenant skipped: %v, want %s", got, tenantA)
	}
	// A member of the default tenant logs in there even with older memberships, unless it is
	// deleted.
	const tenantDefault = "01920000-0000-7000-8000-0000000000de"
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantDefault+`', 'default', 'Default')`,
		`INSERT INTO memberships (tenant_id, user_id, role, created_at) VALUES ('`+tenantDefault+`', '`+userOne+`', 'viewer', now() + interval '1 day')`)
	if got := loginTenant(userOne); got == nil || got.String() != tenantDefault {
		t.Fatalf("default membership: %v, want %s", got, tenantDefault)
	}
	pgtest.MustExec(t, admin, `UPDATE tenants SET deleted_at = now() WHERE id = '`+tenantDefault+`'`)
	if got := loginTenant(userOne); got == nil || got.String() != tenantA {
		t.Fatalf("deleted default skipped: %v, want %s", got, tenantA)
	}
	if got := loginTenant(userNone); got != nil {
		t.Fatalf("user without membership: %v, want NULL", got)
	}
	if got := loginTenant(uuid.NewString()); got != nil {
		t.Fatalf("unknown user: %v, want NULL", got)
	}

	// The owner-only policy does not widen what the runtime roles see.
	var visible int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM memberships`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("api sees %d memberships without a tenant, err=%v", visible, err)
	}
}

// TestIntegrationLoginTenantLastUsed checks that a login enters the last used organization before
// falling back to the order above, and that a suspended one still loses against an active one.
func TestIntegrationLoginTenantLastUsed(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("lastused"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	const older, newer = "01920000-0000-7000-8000-0000000000f1", "01920000-0000-7000-8000-0000000000f2"
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+older+`', 'older', 'Older'), ('`+newer+`', 'newer', 'Newer')`,
		`INSERT INTO users (id, email) VALUES ('`+userA+`', 'a@example.com')`,
		`INSERT INTO memberships (tenant_id, user_id, role, created_at) VALUES
			('`+older+`', '`+userA+`', 'owner', now() - interval '2 days'), ('`+newer+`', '`+userA+`', 'member', now() - interval '1 day')`)
	loginTenant := func() string {
		t.Helper()
		var id string
		if err := api.QueryRow(ctx, `SELECT auth_login_tenant($1)::text`, userA).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if got := loginTenant(); got != older {
		t.Fatalf("never used: login enters %s, want the oldest membership %s", got, older)
	}
	pgtest.MustExec(t, admin, `UPDATE memberships SET last_used_at = now() WHERE tenant_id = '`+newer+`'`)
	if got := loginTenant(); got != newer {
		t.Fatalf("login enters %s, want the last used %s", got, newer)
	}
	pgtest.MustExec(t, admin, `UPDATE memberships SET last_used_at = now() + interval '1 minute' WHERE tenant_id = '`+older+`'`)
	if got := loginTenant(); got != older {
		t.Fatalf("login enters %s, want the last used %s", got, older)
	}
	pgtest.MustExec(t, admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+older+`'`)
	if got := loginTenant(); got != newer {
		t.Fatalf("login enters %s, want the active %s", got, newer)
	}
}
