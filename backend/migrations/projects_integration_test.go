package migrations_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationProjects checks the Default project every tenant gets from the trigger, monitors
// and channels in it, isolation and grants of the project tables, and the routing rule of
// enqueue_incident_reminders.
func TestIntegrationProjects(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("projects"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	worker := env.Conn(t, env.WorkerURL)

	pgtest.MustExec(t, admin, `INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`)
	monA := seedMonitor(t, admin, tenantA, "now()", false, false)
	monGone := seedMonitor(t, admin, tenantA, "now()", false, true)
	monB := seedMonitor(t, admin, tenantB, "now()", false, false)
	chanA, chanGone := uuid.NewString(), uuid.NewString()
	pgtest.MustExec(t, admin,
		`INSERT INTO notification_channels (id, tenant_id, type, config_encrypted, project_id) VALUES ('`+chanA+`', '`+tenantA+`', 'slack_webhook', '\x01', `+defaultProject(tenantA)+`)`,
		`INSERT INTO notification_channels (id, tenant_id, type, deleted_at, status, project_id) VALUES ('`+chanGone+`', '`+tenantA+`', 'slack_webhook', now(), 'failed', `+defaultProject(tenantA)+`)`,
		`INSERT INTO monitor_channels (tenant_id, monitor_id, channel_id) VALUES ('`+tenantA+`', '`+monA+`', '`+chanA+`')`)

	defaultOf := func(tenant string) string {
		t.Helper()
		var id string
		if err := admin.QueryRow(ctx, `SELECT id::text FROM projects WHERE tenant_id = $1 AND is_default AND all_members AND slug = 'default' AND name = 'Default'`, tenant).Scan(&id); err != nil {
			t.Fatalf("default project of %s: %v", tenant, err)
		}
		return id
	}
	projectOf := func(table, id string) string {
		t.Helper()
		var p *string
		if err := admin.QueryRow(ctx, `SELECT project_id::text FROM `+table+` WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		if p == nil {
			return "NULL"
		}
		return *p
	}

	t.Run("default projects", func(t *testing.T) {
		defA, defB := defaultOf(tenantA), defaultOf(tenantB)
		for _, c := range []struct{ table, id, want string }{
			{"monitors", monA, defA}, {"monitors", monGone, defA}, {"monitors", monB, defB},
			{"notification_channels", chanA, defA}, {"notification_channels", chanGone, defA},
		} {
			if got := projectOf(c.table, c.id); got != c.want {
				t.Errorf("%s %s project = %s, want %s", c.table, c.id, got, c.want)
			}
		}
		var version int
		if err := admin.QueryRow(ctx, `SELECT get_byte(uuid_send($1::uuid), 6) >> 4`, defA).Scan(&version); err != nil || version != 7 {
			t.Fatalf("project id version = %d (%v), want 7", version, err)
		}
		var limits string
		if err := admin.QueryRow(ctx, `SELECT string_agg(key || '=' || (limits->>'max_projects'), ',' ORDER BY key) FROM plans`).Scan(&limits); err != nil || limits != "community=2147483647,free=50,internal=50" {
			t.Fatalf("max_projects = %s (%v)", limits, err)
		}
	})

	t.Run("new tenants and organization-wide channels", func(t *testing.T) {
		// auth_create_tenant (and a direct insert) gives the new tenant its Default project.
		var created uuid.UUID
		newID := uuid.New()
		if err := api.QueryRow(ctx, `SELECT auth_create_tenant($1, 'fresh', 'Fresh')`, newID).Scan(&created); err != nil || created != newID {
			t.Fatalf("auth_create_tenant: %v %v", created, err)
		}
		defaultOf(newID.String())
		// A channel without a project is organization-wide (by design). A monitor always has a
		// project (TestIntegrationMonitorProjectRequired).
		var ch string
		err := inTenant(t, api, newID.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO notification_channels (id, tenant_id, type, config_encrypted) VALUES (gen_random_uuid(), $1, 'slack_webhook', '\x01') RETURNING id::text`, newID).Scan(&ch)
		})
		if err != nil || projectOf("notification_channels", ch) != "NULL" {
			t.Fatalf("organization-wide channel: %v %s", err, projectOf("notification_channels", ch))
		}
	})

	t.Run("constraints and isolation", func(t *testing.T) {
		defA, defB := defaultOf(tenantA), defaultOf(tenantB)
		// A monitor cannot point at another tenant's project (composite key).
		err := inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE monitors SET project_id = $1 WHERE id = $2`, defB, monA)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "foreign key") {
			t.Fatalf("cross-tenant project accepted: %v", err)
		}
		// One Default project per tenant, unique live slugs, the Default project cannot be deleted.
		for _, stmt := range []string{
			`INSERT INTO projects (id, tenant_id, name, slug, is_default) VALUES (gen_random_uuid(), $1, 'Second', 'second', true)`,
			`INSERT INTO projects (id, tenant_id, name, slug) VALUES (gen_random_uuid(), $1, 'Dup', 'default')`,
			`UPDATE projects SET deleted_at = now() WHERE tenant_id = $1 AND is_default`,
			`INSERT INTO projects (id, tenant_id, name, slug) VALUES (gen_random_uuid(), $1, 'Bad', 'Bad Slug')`,
		} {
			err := inTenant(t, api, tenantA, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, stmt, tenantA); return err })
			if err == nil {
				t.Errorf("accepted: %s", stmt)
			}
		}
		// A soft-deleted project frees its slug.
		err = inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			for _, s := range []string{
				`INSERT INTO projects (id, tenant_id, name, slug, deleted_at) VALUES (gen_random_uuid(), $1, 'Old', 'web', now())`,
				`INSERT INTO projects (id, tenant_id, name, slug) VALUES (gen_random_uuid(), $1, 'Web', 'web')`,
			} {
				if _, err := tx.Exec(ctx, s, tenantA); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := countInTenant(t, api, tenantA, `SELECT count(*) FROM projects WHERE tenant_id <> '`+tenantA+`'`); n != 0 {
			t.Fatalf("api sees %d projects of other tenants", n)
		}
		// The api cannot hard-delete projects; the worker has no access at all.
		err = inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = $1`, defA)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("api deleted a project: %v", err)
		}
		for _, table := range []string{"projects", "project_members"} {
			if _, err := worker.Exec(ctx, `SELECT count(*) FROM `+table); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("worker reads %s: %v", table, err)
			}
		}
		// project_members: role override member or viewer only, removed with the membership.
		user := uuid.NewString()
		pgtest.MustExec(t, admin, `INSERT INTO users (id, email) VALUES ('`+user+`', 'pm@example.com')`,
			`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+user+`', 'member')`)
		err = inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO project_members (tenant_id, project_id, user_id, role) VALUES ($1, $2, $3, 'maintainer')`, tenantA, defA, user)
			return err
		})
		if err == nil {
			t.Fatal("project role maintainer accepted")
		}
		err = inTenant(t, api, tenantA, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO project_members (tenant_id, project_id, user_id, role) VALUES ($1, $2, $3, 'viewer')`, tenantA, defA, user)
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenantA, user)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := countInTenant(t, api, tenantA, `SELECT count(*) FROM project_members`); n != 0 {
			t.Fatalf("%d project_members rows survive the membership", n)
		}
	})

	t.Run("reminders follow the project", func(t *testing.T) {
		defA := defaultOf(tenantA)
		other := uuid.NewString()
		pgtest.MustExec(t, admin, `INSERT INTO projects (id, tenant_id, name, slug) VALUES ('`+other+`', '`+tenantA+`', 'Other', 'other')`)
		channel := func(project string) string {
			id := uuid.NewString()
			pgtest.MustExec(t, admin, fmt.Sprintf(`INSERT INTO notification_channels (id, tenant_id, type, config_encrypted, reminder_minutes, project_id)
				VALUES ('%s', '%s', 'slack_webhook', '\x01', 5, %s)`, id, tenantA, project))
			return id
		}
		inDefault, inOther, orgWide := channel("'"+defA+"'"), channel("'"+other+"'"), channel("NULL")
		// No selection: the channels of the monitor's own project only.
		fallback := seedMonitor(t, admin, tenantA, "now()", false, false)
		// Selection: the selected channels that are organization-wide or of its project; a stray
		// link to another project's channel (never created by the api) is ignored.
		selecting := seedMonitor(t, admin, tenantA, "now()", false, false)
		pgtest.MustExec(t, admin,
			`INSERT INTO monitor_channels (tenant_id, monitor_id, channel_id) VALUES ('`+tenantA+`', '`+selecting+`', '`+orgWide+`'), ('`+tenantA+`', '`+selecting+`', '`+inOther+`')`)
		// Only a stray link to another project's channel (possible by direct SQL only): not a valid
		// selection, so the project fallback applies.
		stray := seedMonitor(t, admin, tenantA, "now()", false, false)
		pgtest.MustExec(t, admin,
			`INSERT INTO monitor_channels (tenant_id, monitor_id, channel_id) VALUES ('`+tenantA+`', '`+stray+`', '`+inOther+`')`)
		incidents := map[string]string{}
		for _, m := range []string{fallback, selecting, stray} {
			id := uuid.NewString()
			incidents[m] = id
			pgtest.MustExec(t, admin, `INSERT INTO incidents (id, tenant_id, monitor_id, started_at, reason) VALUES ('`+id+`', '`+tenantA+`', '`+m+`', now() - interval '1 hour', 'timeout')`)
		}
		if _, err := worker.Exec(ctx, `SELECT enqueue_incident_reminders(100)`); err != nil {
			t.Fatal(err)
		}
		rows, err := admin.Query(ctx, `SELECT monitor_id::text || '>' || channel_id::text FROM notification_deliveries WHERE kind = 'reminder' ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		// chanA (Default project) has no reminders configured.
		want := map[string]bool{fallback + ">" + inDefault: true, selecting + ">" + orgWide: true, stray + ">" + inDefault: true}
		if len(got) != len(want) {
			t.Fatalf("reminders %v, want %v", got, want)
		}
		for _, g := range got {
			if !want[g] {
				t.Fatalf("unexpected reminder %s (all: %v)", g, got)
			}
		}
	})
}
