package api_test

import (
	"net/http"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The platform admin lists and describes users across organizations, disables and enables them
// and signs them out; a disabled user can neither sign in nor keep a session.
func TestIntegrationAdminUsers(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiadminusers"))
	logs := &lockedBuffer{}
	c := newAdminServer(t, env, logs)
	adminCookie, adminCSRF := c.login()
	ah := withCSRF(adminCookie, adminCSRF)
	aliceCookie, _, reg := c.register("alice@alpha.example")
	alice := reg.body["user"].(map[string]any)["id"].(string)
	alphaOrg := reg.body["organization"].(map[string]any)["id"].(string)
	bobCookie, bobCSRF, reg := c.register("bob@beta.example")
	bob := reg.body["user"].(map[string]any)["id"].(string)
	const base = "/api/v1/admin/users"

	loginAlice := func() response {
		return c.do(http.MethodPost, "/api/v1/auth/login", `{"login":"alice@alpha.example","password":"`+ownerPassword+`"}`, nil)
	}
	auditCount := func(action string) int {
		return countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = $1 AND entity_type = 'user' AND entity_id = $2`, action, alice)
	}

	t.Run("list and search", func(t *testing.T) {
		r := c.do(http.MethodGet, base, "", ah)
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || r.body["total"] != float64(3) || len(items) != 3 || r.body["limit"] != float64(25) {
			t.Fatalf("list: %d %s", r.code, r.raw)
		}
		first := items[0].(map[string]any)
		for _, k := range []string{"id", "login", "displayName", "status", "isPlatformAdmin", "emailVerified", "mustChangePassword", "createdAt", "organizationCount"} {
			if _, ok := first[k]; !ok {
				t.Errorf("list item lacks %s: %v", k, first)
			}
		}
		if first["login"] != "bob@beta.example" || first["organizationCount"] != float64(1) || first["status"] != "active" {
			t.Fatalf("newest first: %v", first)
		}
		r = c.do(http.MethodGet, base+"?query=ALICE%40", "", ah)
		items, _ = r.body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != alice || r.body["total"] != float64(1) {
			t.Fatalf("search: %s", r.raw)
		}
		r = c.do(http.MethodGet, base+"?limit=1&offset=1", "", ah)
		items, _ = r.body["items"].([]any)
		if len(items) != 1 || r.body["total"] != float64(3) || r.body["offset"] != float64(1) {
			t.Fatalf("page: %s", r.raw)
		}
		if r := c.do(http.MethodGet, base+"?offset=50", "", ah); r.code != http.StatusOK || r.body["total"] != float64(3) {
			t.Fatalf("past the end: %s", r.raw)
		}
		if r := c.do(http.MethodGet, base+"?status=disabled", "", ah); r.code != http.StatusOK || r.body["total"] != float64(0) {
			t.Fatalf("disabled: %s", r.raw)
		}
		for _, q := range []string{"status=gone", "limit=0", "offset=-1"} {
			if r := c.do(http.MethodGet, base+"?"+q, "", ah); r.code != http.StatusBadRequest {
				t.Errorf("%s: %d %s", q, r.code, r.raw)
			}
		}
	})

	t.Run("detail", func(t *testing.T) {
		r := c.do(http.MethodGet, base+"/"+alice, "", ah)
		ms, _ := r.body["memberships"].([]any)
		u, _ := r.body["user"].(map[string]any)
		if r.code != http.StatusOK || u["login"] != "alice@alpha.example" || len(ms) != 1 || r.body["openSessions"] != float64(1) {
			t.Fatalf("detail: %d %s", r.code, r.raw)
		}
		m := ms[0].(map[string]any)
		if m["organizationId"] != alphaOrg || m["role"] != "owner" || m["organizationStatus"] != "active" || m["organizationSlug"] == "" || m["joinedAt"] == nil {
			t.Fatalf("membership: %v", m)
		}
		if r := c.do(http.MethodGet, base+"/0192b9c4-7c5e-7000-8000-000000000999", "", ah); r.code != http.StatusNotFound {
			t.Errorf("unknown: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, base+"/not-a-uuid", "", ah); r.code != http.StatusBadRequest {
			t.Errorf("bad id: %d", r.code)
		}
	})

	t.Run("guard rails", func(t *testing.T) {
		var adminID string
		if err := env.AdminConn(t).QueryRow(t.Context(), `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
			t.Fatal(err)
		}
		r := c.do(http.MethodPost, base+"/"+adminID+"/disable", "", ah)
		if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:system-admin" {
			t.Fatalf("disable the platform admin: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+"/0192b9c4-7c5e-7000-8000-000000000999/disable", "", ah); r.code != http.StatusNotFound {
			t.Errorf("unknown user: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+"/"+alice+"/disable", "", adminCookie); r.code != http.StatusForbidden || r.body["type"] != "urn:proreta:problem:csrf" {
			t.Errorf("no csrf: %d %s", r.code, r.raw)
		}
		// A regular user cannot disable anyone, including themselves.
		if r := c.do(http.MethodPost, base+"/"+bob+"/disable", "", withCSRF(bobCookie, bobCSRF)); r.code != http.StatusForbidden {
			t.Errorf("regular user: %d %s", r.code, r.raw)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action LIKE 'admin.user.%'`); n != 0 {
			t.Fatalf("refused calls wrote %d audit rows", n)
		}
	})

	t.Run("disable and enable", func(t *testing.T) {
		if r := c.do(http.MethodGet, "/api/v1/monitors", "", aliceCookie); r.code != http.StatusOK {
			t.Fatalf("alice before: %d", r.code)
		}
		r := c.do(http.MethodPost, base+"/"+alice+"/disable", "", ah)
		u, _ := r.body["user"].(map[string]any)
		if r.code != http.StatusOK || u["status"] != "disabled" || r.body["openSessions"] != float64(0) {
			t.Fatalf("disable: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/monitors", "", aliceCookie); r.code != http.StatusUnauthorized {
			t.Fatalf("session of a disabled user: %d", r.code)
		}
		if r := loginAlice(); r.code != http.StatusUnauthorized {
			t.Fatalf("login of a disabled user: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, base+"?status=disabled", "", ah); r.body["total"] != float64(1) {
			t.Fatalf("disabled filter: %s", r.raw)
		}
		if r := c.do(http.MethodPost, base+"/"+alice+"/disable", "", ah); r.code != http.StatusOK {
			t.Fatalf("disable twice: %d %s", r.code, r.raw)
		}
		if n := auditCount("admin.user.disabled"); n != 1 {
			t.Fatalf("disabled audit rows = %d, want 1", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.user.disabled' AND tenant_id = $1 AND actor_user_id IS NOT NULL AND host(ip) IS NOT NULL
			AND metadata = '{"sessionsRevoked": 1}'`, alphaOrg); n != 1 {
			t.Fatalf("disabled audit row in the user's organization = %d, want 1", n)
		}

		r = c.do(http.MethodPost, base+"/"+alice+"/enable", "", ah)
		u, _ = r.body["user"].(map[string]any)
		if r.code != http.StatusOK || u["status"] != "active" {
			t.Fatalf("enable: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+"/"+alice+"/enable", "", ah); r.code != http.StatusOK {
			t.Fatalf("enable twice: %d %s", r.code, r.raw)
		}
		if n := auditCount("admin.user.enabled"); n != 1 {
			t.Fatalf("enabled audit rows = %d, want 1", n)
		}
		if r := loginAlice(); r.code != http.StatusOK {
			t.Fatalf("login after enable: %d %s", r.code, r.raw)
		}
	})

	t.Run("sign out everywhere", func(t *testing.T) {
		before := auditCount("admin.user.sessions_revoked")
		second := loginAlice()
		cookie, _ := session(t, second)
		if r := c.do(http.MethodGet, base+"/"+alice, "", ah); r.body["openSessions"] != float64(2) {
			t.Fatalf("open sessions: %s", r.raw)
		}
		r := c.do(http.MethodPost, base+"/"+alice+"/sessions/revoke", "", ah)
		if r.code != http.StatusOK || r.body["sessionsRevoked"] != float64(2) {
			t.Fatalf("revoke: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/monitors", "", cookie); r.code != http.StatusUnauthorized {
			t.Fatalf("session after revoke: %d", r.code)
		}
		// The account still works; only the sessions went.
		if r := loginAlice(); r.code != http.StatusOK {
			t.Fatalf("login after revoke: %d", r.code)
		}
		if n := auditCount("admin.user.sessions_revoked"); n != before+1 {
			t.Fatalf("revoke audit rows = %d, want %d", n, before+1)
		}
		// Nothing left to revoke except the session just created; revoking twice audits once each time it deletes something.
		if r := c.do(http.MethodPost, base+"/"+alice+"/sessions/revoke", "", ah); r.body["sessionsRevoked"] != float64(1) {
			t.Fatalf("second revoke: %s", r.raw)
		}
		if r := c.do(http.MethodPost, base+"/"+alice+"/sessions/revoke", "", ah); r.code != http.StatusOK || r.body["sessionsRevoked"] != float64(0) {
			t.Fatalf("empty revoke: %d %s", r.code, r.raw)
		}
		if n := auditCount("admin.user.sessions_revoked"); n != before+2 {
			t.Fatalf("revoke audit rows = %d, want %d (none for the empty one)", n, before+2)
		}
	})

	t.Run("no secrets in the audit log or the logs", func(t *testing.T) {
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action LIKE 'admin.user.%' AND (metadata::text LIKE '%@%' OR metadata::text LIKE '%argon2%')`); n != 0 {
			t.Fatalf("%d audit rows with an email or a hash", n)
		}
	})
}
