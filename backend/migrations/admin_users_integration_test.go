package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The admin user functions refuse anyone but the platform admin, list and describe users across
// organizations, and disable, enable and sign out with the guard rails and audit rows the panel
// relies on.
func TestIntegrationAdminUsers(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("adminusers"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	seedPlatform(t, admin)

	for name, stmt := range map[string]string{
		"list":        `SELECT * FROM admin_list_all_users($1, '', '', 10, 0)`,
		"get":         `SELECT * FROM admin_get_user($1, $1)`,
		"memberships": `SELECT * FROM admin_list_user_memberships($1, $1)`,
		"disable":     `SELECT * FROM admin_set_user_status($1, $1, 'disabled', gen_random_uuid(), NULL)`,
		"enable":      `SELECT * FROM admin_set_user_status($1, $1, 'active', gen_random_uuid(), NULL)`,
		"revoke":      `SELECT admin_revoke_user_sessions($1, $1, gen_random_uuid(), NULL)`,
	} {
		if _, err := api.Exec(ctx, stmt, userOwnerA); sqlState(err) != "PRA01" {
			t.Errorf("%s as a regular user: err = %v, want PRA01", name, err)
		}
	}
	if n := countAudit(t, admin, "admin.user.%"); n != 0 {
		t.Fatalf("refused calls wrote %d audit rows", n)
	}

	t.Run("list and detail", func(t *testing.T) {
		var total int
		var logins []string
		rows, err := api.Query(ctx, `SELECT login, total_count FROM admin_list_all_users($1, 'ALPHA', 'active', 10, 0)`, userPlatform)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var l string
			if err := rows.Scan(&l, &total); err != nil {
				t.Fatal(err)
			}
			logins = append(logins, l)
		}
		rows.Close()
		if len(logins) != 1 || logins[0] != "alice@alpha.example" || total != 1 {
			t.Fatalf("search = %v total %d", logins, total)
		}
		if err := api.QueryRow(ctx, `SELECT count(*) FROM admin_list_all_users($1, '', 'disabled', 10, 0)`, userPlatform).Scan(&total); err != nil || total != 0 {
			t.Fatalf("disabled users = %d (err %v)", total, err)
		}
		if _, err := api.Exec(ctx, `SELECT * FROM admin_list_all_users($1, '', 'gone', 10, 0)`, userPlatform); sqlState(err) != "PRA04" {
			t.Fatalf("bad status: err = %v, want PRA04", err)
		}
		var orgs, sessions int
		if err := api.QueryRow(ctx, `SELECT organization_count, open_sessions FROM admin_get_user($1, $2)`, userPlatform, userOwnerA).Scan(&orgs, &sessions); err != nil || orgs != 1 || sessions != 1 {
			t.Fatalf("detail: orgs %d sessions %d err %v", orgs, sessions, err)
		}
		var slug, role string
		if err := api.QueryRow(ctx, `SELECT organization_slug, role FROM admin_list_user_memberships($1, $2)`, userPlatform, userOwnerA).Scan(&slug, &role); err != nil || slug != "t-aaaa" || role != "owner" {
			t.Fatalf("memberships: %s %s err %v", slug, role, err)
		}
		for _, stmt := range []string{`SELECT * FROM admin_get_user($1, gen_random_uuid())`, `SELECT * FROM admin_list_user_memberships($1, gen_random_uuid())`} {
			if _, err := api.Exec(ctx, stmt, userPlatform); sqlState(err) != "PRA02" {
				t.Errorf("%s: err = %v, want PRA02", stmt, err)
			}
		}
	})

	t.Run("disable, enable and sign out", func(t *testing.T) {
		set := func(user, status string) (changed bool, revoked int) {
			t.Helper()
			if err := api.QueryRow(ctx, `SELECT changed, sessions_revoked FROM admin_set_user_status($1, $2, $3, gen_random_uuid(), NULL)`, userPlatform, user, status).Scan(&changed, &revoked); err != nil {
				t.Fatalf("set %s %s: %v", user, status, err)
			}
			return
		}
		if _, err := api.Exec(ctx, `SELECT * FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), NULL)`, userPlatform, userPlatform); sqlState(err) != "PRA07" {
			t.Fatalf("disable the platform admin: err = %v, want PRA07", err)
		}
		if _, err := api.Exec(ctx, `SELECT * FROM admin_set_user_status($1, gen_random_uuid(), 'disabled', gen_random_uuid(), NULL)`, userPlatform); sqlState(err) != "PRA02" {
			t.Fatalf("unknown user: err = %v, want PRA02", err)
		}
		if _, err := api.Exec(ctx, `SELECT * FROM admin_set_user_status($1, $2, 'deleted', gen_random_uuid(), NULL)`, userPlatform, userOwnerA); sqlState(err) != "PRA04" {
			t.Fatalf("bad status: err = %v, want PRA04", err)
		}
		if changed, _ := set(userOwnerA, "active"); changed {
			t.Fatal("enabling an active user must change nothing")
		}
		if n := countAudit(t, admin, "admin.user.%"); n != 0 {
			t.Fatalf("no-op or refused calls wrote %d audit rows", n)
		}
		if changed, revoked := set(userOwnerA, "disabled"); !changed || revoked != 1 {
			t.Fatalf("disable: changed %v revoked %d", changed, revoked)
		}
		var status string
		var sessions int
		if err := admin.QueryRow(ctx, `SELECT u.status, (SELECT count(*) FROM sessions WHERE user_id = u.id) FROM users u WHERE u.id = $1`, userOwnerA).Scan(&status, &sessions); err != nil || status != "disabled" || sessions != 0 {
			t.Fatalf("after disable: %s, %d sessions, err %v", status, sessions, err)
		}
		if changed, _ := set(userOwnerA, "disabled"); changed {
			t.Fatal("disabling twice must change nothing")
		}
		if changed, _ := set(userOwnerA, "active"); !changed {
			t.Fatal("enable did not change")
		}
		var tenant, meta string
		if err := admin.QueryRow(ctx, `SELECT tenant_id::text, metadata::text FROM audit_log WHERE action = 'admin.user.disabled' AND entity_type = 'user' AND entity_id = $1 AND actor_user_id = $2`,
			userOwnerA, userPlatform).Scan(&tenant, &meta); err != nil || tenant != tenantA || meta != `{"sessionsRevoked": 1}` {
			t.Fatalf("disabled audit: tenant %s meta %s err %v", tenant, meta, err)
		}
		if n := countAudit(t, admin, "admin.user.enabled"); n != 1 {
			t.Fatalf("enabled audit rows = %d, want 1", n)
		}

		// Sign out: nothing to revoke writes no audit row; open sessions are counted and audited.
		revoke := func() (n int) {
			t.Helper()
			if err := api.QueryRow(ctx, `SELECT admin_revoke_user_sessions($1, $2, gen_random_uuid(), NULL)`, userPlatform, userOwnerA).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return
		}
		if n := revoke(); n != 0 || countAudit(t, admin, "admin.user.sessions_revoked") != 0 {
			t.Fatalf("revoke without sessions: %d", n)
		}
		pgtest.MustExec(t, admin, `INSERT INTO sessions (id, user_id, tenant_id, token_hash, csrf_token, expires_at) VALUES
			(gen_random_uuid(), '`+userOwnerA+`', '`+tenantA+`', sha256('s1'::bytea), 'x', now() + interval '1 hour'),
			(gen_random_uuid(), '`+userOwnerA+`', '`+tenantA+`', sha256('s2'::bytea), 'x', now() + interval '1 hour'),
			(gen_random_uuid(), '`+userOwnerA+`', '`+tenantA+`', sha256('s3'::bytea), 'x', now() - interval '1 hour')`)
		if n := revoke(); n != 2 {
			t.Fatalf("revoked %d, want the 2 open sessions", n)
		}
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, userOwnerA).Scan(&sessions); err != nil || sessions != 0 {
			t.Fatalf("sessions left = %d (err %v)", sessions, err)
		}
		if err := admin.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'admin.user.sessions_revoked'`).Scan(&meta); err != nil || meta != `{"sessionsRevoked": 2}` {
			t.Fatalf("revoked audit: %s err %v", meta, err)
		}
	})

	t.Run("a user without a membership is audited in the default tenant", func(t *testing.T) {
		const lone = "01920000-0000-7000-8000-0000000000f1"
		pgtest.MustExec(t, admin, `INSERT INTO users (id, email) VALUES ('`+lone+`', 'lone@example.com')`)
		if _, err := api.Exec(ctx, `SELECT * FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), NULL)`, userPlatform, lone); err != nil {
			t.Fatal(err)
		}
		var tenant string
		if err := admin.QueryRow(ctx, `SELECT tenant_id::text FROM audit_log WHERE action = 'admin.user.disabled' AND entity_id = $1`, lone).Scan(&tenant); err != nil || tenant != tenantDef {
			t.Fatalf("audit tenant = %s (err %v), want the default tenant", tenant, err)
		}
	})
}

func countAudit(t *testing.T, admin *pgx.Conn, like string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action LIKE $1`, like).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
