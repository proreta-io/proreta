package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationStatusPageProject checks the nullable project of a status page: a page without
// one stays valid, the project must be of the same tenant, and tenant isolation still holds.
func TestIntegrationStatusPageProject(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("statuspageproject"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`)

	projectOf := func(tenant string) string {
		t.Helper()
		var id string
		if err := admin.QueryRow(ctx, `SELECT id::text FROM projects WHERE tenant_id = $1 AND is_default`, tenant).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	defA, defB := projectOf(tenantA), projectOf(tenantB)

	insert := func(tenant, slug string, project *string) error {
		return inTenant(t, api, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO status_pages (id, tenant_id, project_id, slug, title) VALUES ($1, $2, $3, $4, 'T')`,
				uuid.NewString(), tenant, project, slug)
			return err
		})
	}
	if err := insert(tenantA, "org-level", nil); err != nil {
		t.Fatalf("page without a project: %v", err)
	}
	if err := insert(tenantA, "in-project", &defA); err != nil {
		t.Fatalf("page in a project: %v", err)
	}
	if err := insert(tenantA, "foreign-project", &defB); err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("a page in another tenant's project: %v", err)
	}
	// Tenant B sees none of tenant A's pages, whatever their project.
	err := inTenant(t, api, tenantB, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM status_pages WHERE project_id IS NOT NULL OR slug = 'org-level'`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("tenant B sees %d pages of tenant A", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
