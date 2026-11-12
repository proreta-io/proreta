package migrations_test

import (
	"context"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationPlatformAdminGrant(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("platformadmin_grant"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	api := env.Conn(t, env.APIURL)
	seedPlatform(t, admin)
	const disabled = "01920000-0000-7000-8000-0000000000f2"
	pgtest.MustExec(t, admin, `INSERT INTO users (id, email, status) VALUES ('`+disabled+`', 'gone@example.com', 'disabled')`)

	set := func(actor, user string, grant bool) (bool, error) {
		var changed bool
		err := api.QueryRow(ctx, `SELECT admin_set_platform_admin($1, $2, $3, gen_random_uuid(), '10.0.0.1')`, actor, user, grant).Scan(&changed)
		return changed, err
	}
	flags := func(user string) (platform, system bool) {
		if err := admin.QueryRow(ctx, `SELECT is_platform_admin, is_system_admin FROM users WHERE id = $1`, user).Scan(&platform, &system); err != nil {
			t.Fatal(err)
		}
		return
	}

	if _, err := set(userOwnerA, userOwnerB, true); sqlState(err) != "PRA01" {
		t.Fatalf("grant by a regular user: err = %v, want PRA01", err)
	}
	if _, err := set(userPlatform, "01920000-0000-7000-8000-0000000000f9", true); sqlState(err) != "PRA02" {
		t.Fatalf("unknown user: err = %v, want PRA02", err)
	}
	if _, err := set(userPlatform, disabled, true); sqlState(err) != "PRA15" {
		t.Fatalf("grant a disabled user: err = %v, want PRA15", err)
	}
	if changed, err := set(userPlatform, userPlatform, true); err != nil || changed {
		t.Fatalf("grant an admin: changed=%v err=%v, want a no-op", changed, err)
	}
	if changed, err := set(userPlatform, userOwnerA, false); err != nil || changed {
		t.Fatalf("revoke a non-admin: changed=%v err=%v, want a no-op", changed, err)
	}
	if n := countAudit(t, admin, "admin.user.platform_admin_%"); n != 0 {
		t.Fatalf("%d audit rows after no-ops and refusals", n)
	}

	if changed, err := set(userPlatform, userOwnerA, true); err != nil || !changed {
		t.Fatalf("grant: changed=%v err=%v", changed, err)
	}
	if p, s := flags(userOwnerA); !p || s {
		t.Fatalf("granted flags = %v/%v, want platform only", p, s)
	}
	var tenant, meta, ip string
	if err := admin.QueryRow(ctx, `SELECT tenant_id::text, metadata::text, host(ip) FROM audit_log WHERE action = 'admin.user.platform_admin_granted' AND entity_id = $1`, userOwnerA).Scan(&tenant, &meta, &ip); err != nil ||
		tenant != tenantA || meta != "{}" || ip != "10.0.0.1" {
		t.Fatalf("grant audit: tenant=%s meta=%s ip=%s err=%v", tenant, meta, ip, err)
	}

	// A granted admin works in its own organizations: it may be a member of another one and it
	// can act as an admin itself.
	pgtest.MustExec(t, admin, `INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantB+`', '`+userOwnerA+`', 'viewer')`)
	if _, err := api.Exec(ctx, `SELECT * FROM admin_get_user($1, $2)`, userOwnerA, userOwnerB); err != nil {
		t.Fatalf("granted admin reads a user: %v", err)
	}
	// Its password can still not be reset by another admin; the system admin cannot be disabled,
	// a granted admin can.
	if _, err := api.Exec(ctx, `SELECT * FROM admin_reset_user_password($1, $2, $3, gen_random_uuid(), NULL)`, userPlatform, userOwnerA, fakeHash); sqlState(err) != "PRA07" {
		t.Fatalf("reset a granted admin's password: err = %v, want PRA07", err)
	}
	if _, err := api.Exec(ctx, `SELECT * FROM admin_set_user_status($1, $2, 'disabled', gen_random_uuid(), NULL)`, userOwnerA, userPlatform); sqlState(err) != "PRA07" {
		t.Fatalf("disable the system admin: err = %v, want PRA07", err)
	}

	// A granted admin can revoke another granted admin, but never the system admin.
	if _, err := set(userOwnerA, userPlatform, false); sqlState(err) != "PRA07" {
		t.Fatalf("revoke the system admin: err = %v, want PRA07", err)
	}
	if changed, err := set(userPlatform, userOwnerA, false); err != nil || !changed {
		t.Fatalf("revoke: changed=%v err=%v", changed, err)
	}
	if p, _ := flags(userOwnerA); p {
		t.Fatal("flag still set after revoke")
	}
	if _, err := set(userOwnerA, userOwnerB, true); sqlState(err) != "PRA01" {
		t.Fatalf("revoked admin acts: err = %v, want PRA01", err)
	}
	if n := countAudit(t, admin, "admin.user.platform_admin_revoked"); n != 1 {
		t.Fatalf("%d revoke audit rows, want 1", n)
	}

	// The last active platform admin is protected even when it is not the system admin.
	pgtest.MustExec(t, admin, `UPDATE users SET is_system_admin = false WHERE id = '`+userPlatform+`'`,
		`UPDATE users SET is_platform_admin = true WHERE id = '`+userOwnerB+`'`,
		`UPDATE users SET is_platform_admin = false WHERE id = '`+userPlatform+`'`)
	if _, err := set(userOwnerB, userOwnerB, false); sqlState(err) != "PRA14" {
		t.Fatalf("revoke the last admin: err = %v, want PRA14", err)
	}
	if p, _ := flags(userOwnerB); !p {
		t.Fatal("the last admin lost the flag")
	}

	// The bootstrap keeps granted admins: syncing the system admin again changes nothing for them.
	pgtest.MustExec(t, admin, `UPDATE users SET is_platform_admin = true, is_system_admin = true WHERE id = '`+userPlatform+`'`)
	var n int
	if err := api.QueryRow(ctx, `SELECT auth_sync_platform_admin($1)`, userPlatform).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sync: n=%d err=%v", n, err)
	}
	if p, _ := flags(userOwnerB); !p {
		t.Fatal("sync cleared a granted admin")
	}
	const next = "01920000-0000-7000-8000-0000000000e9"
	pgtest.MustExec(t, admin, `INSERT INTO users (id, email) VALUES ('`+next+`', 'root')`,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('`+tenantDef+`', '`+next+`', 'owner')`)
	if err := api.QueryRow(ctx, `SELECT auth_sync_platform_admin($1)`, next).Scan(&n); err != nil || n != 2 {
		t.Fatalf("sync of the next system admin: n=%d err=%v, want 2", n, err)
	}
	if p, s := flags(userPlatform); p || s {
		t.Fatalf("previous system admin flags = %v/%v, want cleared", p, s)
	}
	if p, s := flags(next); !p || !s {
		t.Fatalf("next system admin flags = %v/%v", p, s)
	}
	if p, s := flags(userOwnerB); !p || s {
		t.Fatalf("granted admin flags after rotation = %v/%v, want platform only", p, s)
	}
}
