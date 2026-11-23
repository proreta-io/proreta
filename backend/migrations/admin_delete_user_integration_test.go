package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	userVictim  = "01920000-0000-7000-8000-0000000000f5"
	victimEmail = "victim@example.com"
)

// admin_delete_user erases the person, keeps the row as a tombstone and refuses the dangerous
// cases, each with its own error code.
func TestIntegrationAdminDeleteUser(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("admindelete"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	seedPlatform(t, admin)
	pgtest.MustExec(t, admin,
		`INSERT INTO probe_locations (id, code, name, provider, region, kind) VALUES (gen_random_uuid(), 'del-lab', 'Lab', 'self', 'lab', 'platform')`,
		`INSERT INTO users (id, email, display_name, password_hash, configured_password_hash, email_verified_at, password_changed_at, password_must_change)
		 VALUES ('`+userVictim+`', '`+victimEmail+`', 'Vic Tim', '`+fakeHash+`', '`+fakeHash+`', now(), now(), true)`,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+userVictim+`', 'owner'), ('`+tenantB+`', '`+userVictim+`', 'maintainer')`,
		`INSERT INTO project_members (tenant_id, project_id, user_id, role)
		 SELECT tenant_id, id, '`+userVictim+`', 'member' FROM projects WHERE tenant_id = '`+tenantB+`' LIMIT 1`,
		`INSERT INTO sessions (id, user_id, tenant_id, token_hash, csrf_token, expires_at) VALUES
		 (gen_random_uuid(), '`+userVictim+`', '`+tenantA+`', sha256('v1'::bytea), 'x', now() + interval '1 hour'),
		 (gen_random_uuid(), '`+userVictim+`', '`+tenantB+`', sha256('v2'::bytea), 'x', now() + interval '1 hour')`,
		`INSERT INTO auth_identities (user_id, provider, subject, email) VALUES ('`+userVictim+`', 'google', 'victim-sub', '`+victimEmail+`')`,
		`INSERT INTO email_verifications (token_hash, user_id, email, expires_at) VALUES (sha256('ev'::bytea), '`+userVictim+`', '`+victimEmail+`', now() + interval '1 day')`,
		`INSERT INTO password_resets (token_hash, user_id, email, expires_at) VALUES (sha256('pr'::bytea), '`+userVictim+`', '`+victimEmail+`', now() + interval '1 day')`,
		// The victim created things that must keep resolving, and accepted an invitation in an
		// organization it is no longer a member of (the label and the bound address's hash name the person).
		`INSERT INTO invitations (id, tenant_id, role, token_hash, created_by, created_at, expires_at) VALUES
		 (gen_random_uuid(), '`+tenantA+`', 'viewer', sha256('i1'::bytea), '`+userVictim+`', now() - interval '1 hour', now() + interval '1 day')`,
		// An open link of the victim in an organization it does not belong to must stop working as well.
		`INSERT INTO invitations (id, tenant_id, role, token_hash, created_by, created_at, expires_at) VALUES
		 (gen_random_uuid(), '`+tenantDef+`', 'maintainer', sha256('i3'::bytea), '`+userVictim+`', now() - interval '1 hour', now() + interval '1 day')`,
		`INSERT INTO invitations (id, tenant_id, role, token_hash, created_by, created_at, expires_at, accepted_at, accepted_by, label, email_hash)
		 VALUES (gen_random_uuid(), '`+tenantDef+`', 'viewer', sha256('i2'::bytea), '`+userPlatform+`', now() - interval '2 hours', now() + interval '1 day', now(), '`+userVictim+`', 'Vic Tim', sha256('victim'::bytea))`,
		`INSERT INTO org_auth_domains (id, tenant_id, domain, created_by) VALUES (gen_random_uuid(), '`+tenantA+`', 'victim.example', '`+userVictim+`')`,
		`INSERT INTO specula_agents (tenant_id, id, name, token_hash, location_id, created_by)
		 VALUES ('`+tenantA+`', gen_random_uuid(), 'Lab', decode(repeat('cd', 32), 'hex'), (SELECT id FROM probe_locations WHERE code = 'del-lab'), '`+userVictim+`')`,
		// Second owner of A, so the victim is not its only one.
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantA+`', '`+userOwnerB+`', 'owner')`)

	del := func(actor, user string) (changed bool, sessions, members, identities int, err error) {
		err = api.QueryRow(ctx, `SELECT changed, sessions_revoked, memberships_removed, identities_removed FROM admin_delete_user($1, $2, gen_random_uuid(), '10.0.0.9')`,
			actor, user).Scan(&changed, &sessions, &members, &identities)
		return
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}

	t.Run("guards", func(t *testing.T) {
		for name, tc := range map[string]struct{ actor, user, code string }{
			"not a platform admin": {userOwnerA, userVictim, "PRA01"},
			"unknown user":         {userPlatform, "01920000-0000-7000-8000-0000000000ff", "PRA02"},
			"system admin":         {userPlatform, userPlatform, "PRA07"},
		} {
			if _, _, _, _, err := del(tc.actor, tc.user); sqlState(err) != tc.code {
				t.Errorf("%s: err = %v, want %s", name, err, tc.code)
			}
		}
		// Another platform admin (not the system admin) is refused: PRA36, which also covers oneself.
		const other = "01920000-0000-7000-8000-0000000000f6"
		pgtest.MustExec(t, admin, `INSERT INTO users (id, email, is_platform_admin) VALUES ('`+other+`', 'second-admin@example.com', true)`)
		for _, actor := range []string{userPlatform, other} {
			if _, _, _, _, err := del(actor, other); sqlState(err) != "PRA36" {
				t.Errorf("platform admin %s: err = %v, want PRA36", actor, err)
			}
		}
		pgtest.MustExec(t, admin, `DELETE FROM users WHERE id = '`+other+`'`)
		// The only active owner of an organization: userOwnerB is the second owner of A but its
		// own organization B has no other owner except the victim (maintainer), so B refuses.
		if _, _, _, _, err := del(userPlatform, userOwnerB); sqlState(err) != "PRA35" {
			t.Errorf("only owner of B: err = %v, want PRA35", err)
		}
		// Owners who are disabled do not count as another owner of A.
		pgtest.MustExec(t, admin, `UPDATE users SET status = 'disabled' WHERE id IN ('`+userOwnerA+`', '`+userOwnerB+`')`)
		if _, _, _, _, err := del(userPlatform, userVictim); sqlState(err) != "PRA35" {
			t.Errorf("owner next to disabled owners: err = %v, want PRA35", err)
		}
		pgtest.MustExec(t, admin, `UPDATE users SET status = 'active' WHERE id IN ('`+userOwnerA+`', '`+userOwnerB+`')`)
		if n := countAudit(t, admin, "admin.user.deleted"); n != 0 {
			t.Fatalf("refused calls wrote %d audit rows", n)
		}
		if n := count(`SELECT count(*) FROM users WHERE status = 'deleted'`); n != 0 {
			t.Fatalf("refused calls deleted %d users", n)
		}
	})

	t.Run("erases the person and keeps a tombstone", func(t *testing.T) {
		// One owner of A may go while another active owner stays (userOwnerA and userOwnerB remain).
		changed, sessions, members, identities, err := del(userPlatform, userVictim)
		if err != nil || !changed || sessions != 2 || members != 2 || identities != 1 {
			t.Fatalf("delete: changed %v sessions %d members %d identities %d err %v", changed, sessions, members, identities, err)
		}
		var email, name, status string
		var hash, cfg, verified, changedAt, deletedAt bool
		var must bool
		if err := admin.QueryRow(ctx, `SELECT email::text, display_name, status, password_hash IS NULL, configured_password_hash IS NULL,
			email_verified_at IS NULL, password_changed_at IS NULL, password_must_change, deleted_at IS NOT NULL FROM users WHERE id = $1`, userVictim).
			Scan(&email, &name, &status, &hash, &cfg, &verified, &changedAt, &must, &deletedAt); err != nil {
			t.Fatal(err)
		}
		if email != "deleted-"+userVictim+"@deleted.invalid" || name != "" || status != "deleted" || !hash || !cfg || !verified || !changedAt || must || !deletedAt {
			t.Fatalf("tombstone: %s %q %s hash-null=%v cfg-null=%v ver-null=%v chg-null=%v must=%v deleted_at=%v", email, name, status, hash, cfg, verified, changedAt, must, deletedAt)
		}
		for _, tbl := range []string{"memberships", "project_members", "sessions", "auth_identities", "email_verifications", "password_resets"} {
			if n := count(`SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, userVictim); n != 0 {
				t.Errorf("%s still has %d rows of the user", tbl, n)
			}
		}
		if n := count(`SELECT count(*) FROM invitations WHERE accepted_by = $1 AND label = '' AND email_hash IS NULL`, userVictim); n != 1 {
			t.Errorf("accepted invitation with a cleared label and address hash: %d, want 1", n)
		}
		// The links the victim handed out are revoked, in every organization.
		for _, tok := range []string{"i1", "i3"} {
			if n := count(`SELECT count(*) FROM auth_invitation_lookup(sha256($1::bytea))`, tok); n != 0 {
				t.Errorf("open invitation %s of the deleted user still resolves", tok)
			}
		}
		if n := count(`SELECT count(*) FROM invitations WHERE created_by = $1 AND revoked_at IS NOT NULL`, userVictim); n != 2 {
			t.Errorf("revoked invitations of the user = %d, want 2", n)
		}
		// Every reference still resolves to the tombstone.
		for name, tc := range map[string]struct {
			q    string
			want int
		}{
			"invitation":  {`SELECT count(*) FROM invitations i JOIN users u ON u.id = i.created_by WHERE u.id = $1`, 2},
			"auth domain": {`SELECT count(*) FROM org_auth_domains d JOIN users u ON u.id = d.created_by WHERE u.id = $1`, 1},
			"specula":     {`SELECT count(*) FROM specula_agents a JOIN users u ON u.id = a.created_by WHERE u.id = $1`, 1},
		} {
			if n := count(tc.q, userVictim); n != tc.want {
				t.Errorf("%s created by the deleted user resolves %d times, want %d", name, n, tc.want)
			}
		}
		var tenant, meta, ip string
		if err := admin.QueryRow(ctx, `SELECT tenant_id::text, metadata::text, host(ip) FROM audit_log WHERE action = 'admin.user.deleted' AND entity_id = $1 AND actor_user_id = $2`,
			userVictim, userPlatform).Scan(&tenant, &meta, &ip); err != nil {
			t.Fatal(err)
		}
		if tenant != tenantA || ip != "10.0.0.9" || meta != `{"sessionsRevoked": 2, "identitiesRemoved": 1, "membershipsRemoved": 2}` || strings.Contains(meta, "@") {
			t.Fatalf("audit: tenant %s ip %s meta %s", tenant, ip, meta)
		}
		// The tenant id of the connection is restored.
		var cur *string
		if err := api.QueryRow(ctx, `SELECT current_setting('app.tenant_id', true)`).Scan(&cur); err != nil || (cur != nil && *cur != "") {
			t.Fatalf("app.tenant_id after the call = %v (err %v)", cur, err)
		}
	})

	t.Run("a second delete changes nothing", func(t *testing.T) {
		if changed, _, _, _, err := del(userPlatform, userVictim); err != nil || changed {
			t.Fatalf("second delete: changed %v err %v", changed, err)
		}
		if n := countAudit(t, admin, "admin.user.deleted"); n != 1 {
			t.Fatalf("audit rows = %d, want 1", n)
		}
	})

	t.Run("listing, enabling and re-registering", func(t *testing.T) {
		list := func(status string) (n int) {
			t.Helper()
			if err := api.QueryRow(ctx, `SELECT count(*) FROM admin_list_all_users($1, '', $2, 100, 0) WHERE id = $3`, userPlatform, status, userVictim).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return
		}
		if list("") != 0 || list("active") != 0 || list("deleted") != 1 {
			t.Fatal("the tombstone must be hidden by default and listed with status=deleted")
		}
		if _, err := api.Exec(ctx, `SELECT * FROM admin_set_user_status($1, $2, 'active', gen_random_uuid(), NULL)`, userPlatform, userVictim); sqlState(err) != "PRA02" {
			t.Fatalf("enable a tombstone: err = %v, want PRA02", err)
		}
		// The address is free: it registers again as a new account with nothing of the old one.
		const again = "01920000-0000-7000-8000-0000000000f7"
		if _, err := api.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`, again, victimEmail); err != nil {
			t.Fatalf("register again: %v", err)
		}
		if n := count(`SELECT count(*) FROM memberships WHERE user_id = $1`, again); n != 0 {
			t.Fatalf("new account has %d memberships", n)
		}
		// The identity is released too: the same provider subject links to the new account.
		if _, err := api.Exec(ctx, `INSERT INTO auth_identities (user_id, provider, subject) VALUES ($1, 'google', 'victim-sub')`, again); err != nil {
			t.Fatalf("link the provider again: %v", err)
		}
		// A tombstone cannot be made a platform admin.
		if _, err := api.Exec(ctx, `SELECT admin_set_platform_admin($1, $2, true, gen_random_uuid(), NULL)`, userPlatform, userVictim); sqlState(err) != "PRA15" {
			t.Fatalf("grant to a tombstone: err = %v, want PRA15", err)
		}
		// The pair constraint holds in both directions.
		if _, err := admin.Exec(ctx, `UPDATE users SET status = 'deleted' WHERE id = $1`, again); err == nil {
			t.Fatal("status deleted without deleted_at must be refused")
		}
	})
}
