package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationMonitorProjectRequired checks that every monitor names a project of its own
// tenant: the database neither fills a missing project in nor accepts one of another tenant.
func TestIntegrationMonitorProjectRequired(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("monitorproject"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`)
	const cfg = `'{"url":"https://example.com","expectedStatus":"2xx"}'`
	insert := func(project string) error {
		return inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO monitors (id, tenant_id, name, config, project_id) VALUES (gen_random_uuid(), $1, 'm', `+cfg+`, `+project+`)`, tenantA)
			return err
		})
	}
	if err := insert("NULL"); err == nil || !strings.Contains(err.Error(), "null value") {
		t.Fatalf("monitor without a project: %v", err)
	}
	// Read as the superuser: tenant A's transaction cannot see tenant B's projects at all.
	var projectB string
	if err := admin.QueryRow(ctx, `SELECT id::text FROM projects WHERE tenant_id = $1`, tenantB).Scan(&projectB); err != nil {
		t.Fatal(err)
	}
	if err := insert("'" + projectB + "'"); err == nil || !strings.Contains(err.Error(), "monitors_project_fkey") {
		t.Fatalf("monitor in another tenant's project: %v", err)
	}
	if err := insert(defaultProject(tenantA)); err != nil {
		t.Fatalf("monitor in its Default project: %v", err)
	}
	var triggers int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgrelid = 'monitors'::regclass AND NOT tgisinternal`).Scan(&triggers); err != nil || triggers != 0 {
		t.Fatalf("triggers on monitors: %d (%v), want none", triggers, err)
	}
}
