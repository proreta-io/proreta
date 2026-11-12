package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/migrations"
)

const (
	userC = "01920000-0000-7000-8000-0000000000c1"
	userD = "01920000-0000-7000-8000-0000000000d1"
)

// TestIntegrationTwoLevelRolesMigration puts the override-era schema back, runs the migration file
// as proreta_owner and checks that project rows keep the effective role they had, that an
// organization role may be NULL, and that row level security is forced again afterwards.
func TestIntegrationTwoLevelRolesMigration(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("twolevelroles"))
	ctx := context.Background()
	admin := env.AdminConn(t)

	pgtest.MustExec(t, admin,
		`ALTER TABLE memberships ALTER COLUMN role SET NOT NULL`,
		`ALTER TABLE project_members ALTER COLUMN role DROP NOT NULL`,
		`ALTER TABLE project_members DROP CONSTRAINT project_members_role_check`,
		`ALTER TABLE project_members ADD CONSTRAINT project_members_role_check CHECK (role IN ('member', 'viewer'))`,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'Alpha')`,
		`INSERT INTO users (id, email) VALUES ('`+userA+`', 'a@example.com'), ('`+userB+`', 'b@example.com'),
			('`+userC+`', 'c@example.com'), ('`+userD+`', 'd@example.com')`,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+userA+`', 'owner'),
			('`+tenantA+`', '`+userB+`', 'member'), ('`+tenantA+`', '`+userC+`', 'viewer'), ('`+tenantA+`', '`+userD+`', 'maintainer')`,
		// member without override, member lowered to viewer, viewer with a (useless) member override, maintainer with an override.
		`INSERT INTO project_members (tenant_id, project_id, user_id, role)
			SELECT tenant_id, id, `+"'"+userB+"'"+`, NULL FROM projects WHERE tenant_id = '`+tenantA+`' AND is_default`,
		`INSERT INTO project_members (tenant_id, project_id, user_id, role)
			SELECT tenant_id, id, `+"'"+userA+"'"+`, 'viewer' FROM projects WHERE tenant_id = '`+tenantA+`' AND is_default`,
		`INSERT INTO project_members (tenant_id, project_id, user_id, role)
			SELECT tenant_id, id, `+"'"+userC+"'"+`, 'member' FROM projects WHERE tenant_id = '`+tenantA+`' AND is_default`,
		`INSERT INTO project_members (tenant_id, project_id, user_id, role)
			SELECT tenant_id, id, `+"'"+userD+"'"+`, NULL FROM projects WHERE tenant_id = '`+tenantA+`' AND is_default`,
	)

	body, err := fs.ReadFile(migrations.FS, "20261121090000_two_level_roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	owner := env.Conn(t, env.OwnerURL)
	if _, err := owner.Exec(ctx, string(body)); err != nil {
		t.Fatalf("migration: %v", err)
	}

	role := func(user string) string {
		var r string
		if err := admin.QueryRow(ctx, `SELECT role FROM project_members WHERE user_id = $1`, user).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for user, want := range map[string]string{
		userB: "member", // no override
		userA: "viewer", // owner lowered by an override stays a harmless row
		userC: "viewer", // an organization viewer must not be raised by the old member override
		userD: "member", // NULL override of a maintainer
	} {
		if got := role(user); got != want {
			t.Errorf("project role of %s = %q, want %q", user, got, want)
		}
	}

	// A person without an organization role, and a direct role per project.
	if _, err := admin.Exec(ctx, `UPDATE memberships SET role = NULL WHERE user_id = $1`, userB); err != nil {
		t.Errorf("a NULL organization role is refused: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE memberships SET role = 'root' WHERE user_id = $1`, userC); err == nil {
		t.Error("an unknown organization role is accepted")
	}
	for _, r := range []string{"owner", "maintainer", "member", "viewer"} {
		if _, err := admin.Exec(ctx, `UPDATE project_members SET role = $1 WHERE user_id = $2`, r, userB); err != nil {
			t.Errorf("project role %s refused: %v", r, err)
		}
	}
	if _, err := admin.Exec(ctx, `UPDATE project_members SET role = NULL WHERE user_id = $1`, userB); err == nil {
		t.Error("a project role may still be NULL")
	}
	if _, err := admin.Exec(ctx, `UPDATE project_members SET role = 'admin' WHERE user_id = $1`, userB); err == nil {
		t.Error("an unknown project role is accepted")
	}
	if v := schemaViolations(t, ctx, admin); len(v) > 0 {
		t.Fatalf("schema lint after the migration: %v", v)
	}
}
