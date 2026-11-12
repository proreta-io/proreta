package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/migrations"
)

// TestIntegrationRoleMaintainerMigration puts the pre-rename constraints and rows back, runs
// the migration file as proreta_owner and checks that existing rows follow and that row level
// security is forced again afterwards.
func TestIntegrationRoleMaintainerMigration(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("rolemaintainer"))
	ctx := context.Background()
	admin := env.AdminConn(t)

	pgtest.MustExec(t, admin,
		`ALTER TABLE memberships DROP CONSTRAINT memberships_role_check`,
		`ALTER TABLE invitations DROP CONSTRAINT invitations_role_check`,
		`ALTER TABLE org_auth_domains DROP CONSTRAINT org_auth_domains_default_role_check`,
		`ALTER TABLE memberships ADD CONSTRAINT memberships_role_check CHECK (role IN ('owner', 'admin', 'member', 'viewer'))`,
		`ALTER TABLE invitations ADD CONSTRAINT invitations_role_check CHECK (role IN ('admin', 'member', 'viewer'))`,
		`ALTER TABLE org_auth_domains ADD CONSTRAINT org_auth_domains_default_role_check CHECK (default_role IN ('admin', 'member', 'viewer'))`,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'Alpha')`,
		`INSERT INTO users (id, email) VALUES ('`+userA+`', 'a@example.com'), ('`+userB+`', 'b@example.com')`,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+userA+`', 'owner'), ('`+tenantA+`', '`+userB+`', 'admin')`,
		`INSERT INTO invitations (id, tenant_id, role, token_hash, created_by, created_at, expires_at)
			VALUES (gen_random_uuid(), '`+tenantA+`', 'admin', sha256('x'), '`+userA+`', now() - interval '1 hour', now() + interval '1 day'),
			       (gen_random_uuid(), '`+tenantA+`', 'viewer', sha256('y'), '`+userA+`', now() - interval '1 hour', now() + interval '1 day')`,
		`INSERT INTO org_auth_domains (id, tenant_id, domain, default_role, created_by) VALUES (gen_random_uuid(), '`+tenantA+`', 'example.com', 'admin', '`+userA+`')`,
	)

	body, err := fs.ReadFile(migrations.FS, "20261120090000_role_maintainer.sql")
	if err != nil {
		t.Fatal(err)
	}
	owner := env.Conn(t, env.OwnerURL)
	if _, err := owner.Exec(ctx, string(body)); err != nil {
		t.Fatalf("migration: %v", err)
	}

	count := func(q string) int {
		var n int
		if err := admin.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM memberships WHERE role = 'admin'`:                   0,
		`SELECT count(*) FROM memberships WHERE role = 'maintainer'`:              1,
		`SELECT count(*) FROM memberships WHERE role = 'owner'`:                   1,
		`SELECT count(*) FROM invitations WHERE role = 'maintainer'`:              1,
		`SELECT count(*) FROM invitations WHERE role = 'viewer'`:                  1,
		`SELECT count(*) FROM org_auth_domains WHERE default_role = 'maintainer'`: 1,
	} {
		if got := count(q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	if _, err := admin.Exec(ctx, `UPDATE memberships SET role = 'admin' WHERE user_id = '`+userB+`'`); err == nil {
		t.Error("the old role name is still accepted")
	}
	if v := schemaViolations(t, ctx, admin); len(v) > 0 {
		t.Fatalf("schema lint after the migration: %v", v)
	}
}
