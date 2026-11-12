package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationProjectInvitations checks the project column of invitations: the role check
// depends on it, the project must belong to the same tenant, and the replaced lookup function
// returns the project of an open invitation and nothing once the project is deleted.
func TestIntegrationProjectInvitations(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("projectinvites"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	const (
		projA = "01920000-0000-7000-8000-00000000d001"
		projB = "01920000-0000-7000-8000-00000000d002"
	)
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'Alpha'), ('`+tenantB+`', 'b', 'Beta')`,
		`INSERT INTO users (id, email) VALUES ('`+userA+`', 'a@example.com')`,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+userA+`', 'owner')`,
		`INSERT INTO projects (id, tenant_id, name, slug) VALUES ('`+projA+`', '`+tenantA+`', 'Shop', 'shop'), ('`+projB+`', '`+tenantB+`', 'Other', 'other')`,
	)
	insert := func(token, role, project string) error {
		return inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			var p any
			if project != "" {
				p = project
			}
			_, err := tx.Exec(ctx, `INSERT INTO invitations (id, tenant_id, role, token_hash, created_by, created_at, expires_at, project_id)
				VALUES (gen_random_uuid(), $1, $2, $3, $4, now() - interval '1 hour', now() + interval '7 days', $5)`,
				tenantA, role, tokenHash(token), userA, p)
			return err
		})
	}

	t.Run("role check follows the project", func(t *testing.T) {
		if err := insert("org-owner", "owner", ""); err == nil || !strings.Contains(err.Error(), "invitations_role_check") {
			t.Fatalf("owner organization invitation: %v", err)
		}
		if err := insert("project-owner", "owner", projA); err != nil {
			t.Fatalf("owner project invitation: %v", err)
		}
		if err := insert("project-bad", "admin", projA); err == nil || !strings.Contains(err.Error(), "invitations_role_check") {
			t.Fatalf("unknown role: %v", err)
		}
	})

	t.Run("project of another tenant", func(t *testing.T) {
		if err := insert("cross", "member", projB); err == nil || !strings.Contains(err.Error(), "invitations_project_fk") {
			t.Fatalf("cross-tenant project: %v", err)
		}
	})

	t.Run("lookup", func(t *testing.T) {
		look := func(token string) (name *string, n int) {
			rows, err := api.Query(ctx, `SELECT project_name FROM auth_invitation_lookup($1)`, tokenHash(token))
			if err != nil {
				t.Fatal(err)
			}
			got, err := pgx.CollectRows(rows, pgx.RowTo[*string])
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 1 {
				name = got[0]
			}
			return name, len(got)
		}
		if err := insert("org", "member", ""); err != nil {
			t.Fatal(err)
		}
		if name, n := look("org"); n != 1 || name != nil {
			t.Fatalf("organization invitation: %v %d", name, n)
		}
		if name, n := look("project-owner"); n != 1 || name == nil || *name != "Shop" {
			t.Fatalf("project invitation: %v %d", name, n)
		}
		pgtest.MustExec(t, admin, `UPDATE projects SET deleted_at = now() WHERE id = '`+projA+`'`)
		if _, n := look("project-owner"); n != 0 {
			t.Fatalf("invitation into a deleted project is found")
		}
		if _, n := look("org"); n != 1 {
			t.Fatalf("organization invitation lost")
		}
	})
}
