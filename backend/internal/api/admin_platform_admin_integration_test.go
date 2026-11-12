package api_test

import (
	"net/http"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The platform admin grants and revokes the platform-admin flag of other users. The flag is read
// on every request, so a revoked admin loses access at once and keeps its session.
func TestIntegrationAdminPlatformAdmin(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiplatformadmin"))
	logs := &lockedBuffer{}
	c := newAdminServer(t, env, logs)
	adminCookie, adminCSRF := c.login()
	ah := withCSRF(adminCookie, adminCSRF)
	aliceCookie, aliceCSRF, reg := c.register("alice@alpha.example")
	alice := reg.body["user"].(map[string]any)["id"].(string)
	alphaOrg := reg.body["organization"].(map[string]any)["id"].(string)
	alicePrincipal := withCSRF(aliceCookie, aliceCSRF)
	bobCookie, bobCSRF, reg := c.register("bob@beta.example")
	bob := reg.body["user"].(map[string]any)["id"].(string)
	bobOrg := reg.body["organization"].(map[string]any)["id"].(string)
	var adminID string
	if err := env.AdminConn(t).QueryRow(t.Context(), `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	const base = "/api/v1/admin/users/"
	put := func(id, body string, hdr map[string]string) response {
		return c.do(http.MethodPut, base+id+"/platform-admin", body, hdr)
	}
	audit := func(action string) int {
		return countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = $1 AND entity_type = 'user' AND entity_id = $2`, action, alice)
	}

	t.Run("refused", func(t *testing.T) {
		if r := c.do(http.MethodPut, base+alice+"/platform-admin", `{"enabled":true}`, nil); r.code != http.StatusUnauthorized {
			t.Errorf("no session: %d %s", r.code, r.raw)
		}
		if r := put(alice, `{"enabled":true}`, adminCookie); r.code != http.StatusForbidden || r.body["type"] != "urn:proreta:problem:csrf" {
			t.Errorf("no csrf: %d %s", r.code, r.raw)
		}
		if r := put(alice, `{"enabled":true}`, withCSRF(bobCookie, bobCSRF)); r.code != http.StatusForbidden || r.body["type"] != "urn:proreta:problem:forbidden" {
			t.Errorf("regular user: %d %s", r.code, r.raw)
		}
		if r := put(alice, `{}`, ah); r.code != http.StatusBadRequest {
			t.Errorf("no body field: %d %s", r.code, r.raw)
		}
		if r := put("0192b9c4-7c5e-7000-8000-000000000999", `{"enabled":true}`, ah); r.code != http.StatusNotFound {
			t.Errorf("unknown user: %d %s", r.code, r.raw)
		}
		if r := put(adminID, `{"enabled":false}`, ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:system-admin" {
			t.Errorf("revoke the system admin: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/admin/stats", "", aliceCookie); r.code != http.StatusForbidden {
			t.Errorf("alice before the grant: %d", r.code)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action LIKE 'admin.user.platform_admin_%'`); n != 0 {
			t.Fatalf("refused calls wrote %d audit rows", n)
		}
	})

	t.Run("grant", func(t *testing.T) {
		r := put(alice, `{"enabled":true}`, ah)
		u, _ := r.body["user"].(map[string]any)
		if r.code != http.StatusOK || u["isPlatformAdmin"] != true || u["isSystemAdmin"] != false || len(r.body["memberships"].([]any)) != 1 {
			t.Fatalf("grant: %d %s", r.code, r.raw)
		}
		if r := put(alice, `{"enabled":true}`, ah); r.code != http.StatusOK {
			t.Fatalf("grant twice: %d %s", r.code, r.raw)
		}
		if n := audit("admin.user.platform_admin_granted"); n != 1 {
			t.Fatalf("granted audit rows = %d, want 1", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.user.platform_admin_granted' AND tenant_id = $1 AND actor_user_id = $2 AND metadata = '{}'`, alphaOrg, adminID); n != 1 {
			t.Fatalf("granted audit row in the user's organization = %d, want 1", n)
		}
		// The existing session of the new admin sees the admin area at once.
		if r := c.do(http.MethodGet, "/api/v1/admin/stats", "", aliceCookie); r.code != http.StatusOK {
			t.Fatalf("alice after the grant: %d %s", r.code, r.raw)
		}
		me := c.do(http.MethodGet, "/api/v1/auth/me", "", aliceCookie)
		if user, _ := me.body["user"].(map[string]any); me.code != http.StatusOK || user["isPlatformAdmin"] != true || user["isSystemAdmin"] != false {
			t.Fatalf("me: %d %s", me.code, me.raw)
		}
		list := c.do(http.MethodGet, "/api/v1/admin/users?query=alice", "", ah)
		if items, _ := list.body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["isPlatformAdmin"] != true {
			t.Fatalf("list: %s", list.raw)
		}
	})

	t.Run("a granted admin works in organizations", func(t *testing.T) {
		// Added to another organization (the system admin cannot be) and still an owner of its own.
		if r := c.do(http.MethodPut, "/api/v1/admin/organizations/"+bobOrg+"/members/"+alice, `{"role":"member"}`, ah); r.code != http.StatusOK && r.code != http.StatusCreated {
			t.Fatalf("add a granted admin to another organization: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/members", "", alicePrincipal); r.code != http.StatusOK {
			t.Fatalf("members as a granted admin: %d %s", r.code, r.raw)
		}
		members := c.do(http.MethodGet, "/api/v1/members", "", withCSRF(bobCookie, bobCSRF))
		found := false
		for _, m := range members.body["items"].([]any) {
			if m := m.(map[string]any); m["userId"] == alice {
				found = true
				if m["isPlatformAdmin"] != true || m["isSystemAdmin"] != false {
					t.Fatalf("member flags: %v", m)
				}
			}
		}
		if !found {
			t.Fatalf("alice is not listed: %s", members.raw)
		}
		// Its password still cannot be reset in the panel.
		if r := c.do(http.MethodPost, base+alice+"/reset-password", "", ah); r.code != http.StatusConflict {
			t.Errorf("reset a granted admin's password: %d %s", r.code, r.raw)
		}
		// The password reset by link and the profile stay available: only the system admin is excluded.
		if r := c.do(http.MethodPut, "/api/v1/admin/organizations/"+bobOrg+"/members/"+adminID, `{"role":"member"}`, ah); r.code != http.StatusConflict {
			t.Errorf("add the system admin to another organization: %d %s", r.code, r.raw)
		}
	})

	t.Run("a granted admin revokes another one", func(t *testing.T) {
		if r := put(bob, `{"enabled":true}`, alicePrincipal); r.code != http.StatusOK {
			t.Fatalf("grant by a granted admin: %d %s", r.code, r.raw)
		}
		if r := put(bob, `{"enabled":false}`, alicePrincipal); r.code != http.StatusOK {
			t.Fatalf("revoke by a granted admin: %d %s", r.code, r.raw)
		}
		if r := put(bob, `{"enabled":false}`, alicePrincipal); r.code != http.StatusOK {
			t.Fatalf("revoke a non-admin: %d %s", r.code, r.raw)
		}
		if r := put(adminID, `{"enabled":false}`, alicePrincipal); r.code != http.StatusConflict {
			t.Fatalf("revoke the system admin: %d %s", r.code, r.raw)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		r := put(alice, `{"enabled":false}`, ah)
		u, _ := r.body["user"].(map[string]any)
		if r.code != http.StatusOK || u["isPlatformAdmin"] != false {
			t.Fatalf("revoke: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/admin/stats", "", aliceCookie); r.code != http.StatusForbidden {
			t.Fatalf("revoked admin: %d, want 403 at once", r.code)
		}
		if r := c.do(http.MethodGet, "/api/v1/members", "", aliceCookie); r.code != http.StatusOK {
			t.Fatalf("revoked admin keeps its session: %d", r.code)
		}
		me := c.do(http.MethodGet, "/api/v1/auth/me", "", aliceCookie)
		if user, _ := me.body["user"].(map[string]any); user["isPlatformAdmin"] != false {
			t.Fatalf("me after revoke: %s", me.raw)
		}
		if n := audit("admin.user.platform_admin_revoked"); n != 1 {
			t.Fatalf("revoked audit rows = %d, want 1", n)
		}
	})

	t.Run("the last platform admin", func(t *testing.T) {
		conn := env.AdminConn(t)
		pgtest.MustExec(t, conn, `UPDATE users SET is_system_admin = false WHERE email = 'admin'`)
		r := put(adminID, `{"enabled":false}`, ah)
		if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:last-platform-admin" {
			t.Fatalf("revoke the last admin: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/admin/stats", "", adminCookie); r.code != http.StatusOK {
			t.Fatalf("admin after the refusal: %d", r.code)
		}
	})

	t.Run("a disabled user cannot be granted", func(t *testing.T) {
		pgtest.MustExec(t, env.AdminConn(t), `UPDATE users SET status = 'disabled' WHERE id = '`+bob+`'`)
		r := put(bob, `{"enabled":true}`, ah)
		fe, _ := r.body["fieldErrors"].([]any)
		if r.code != http.StatusUnprocessableEntity || len(fe) == 0 || fe[0].(map[string]any)["field"] != "enabled" {
			t.Fatalf("grant a disabled user: %d %s", r.code, r.raw)
		}
	})
}
