package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationAdminCreateOrganization(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiadmincreateorg"))
	pgtest.MustExec(t, env.AdminConn(t), pgtest.CappedPlanSQL)
	logs := &lockedBuffer{}
	c := newAdminServer(t, env, logs)
	adminCookie, adminCSRF := c.login()
	ah := withCSRF(adminCookie, adminCSRF)
	aliceCookie, aliceCSRF, regA := c.register("alice@alpha.example")
	alice := withCSRF(aliceCookie, aliceCSRF)
	aliceID := regA.body["user"].(map[string]any)["id"].(string)
	const path = "/api/v1/admin/organizations"
	var adminID string
	if err := env.AdminConn(t).QueryRow(t.Context(), `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	orgOf := func(r response) string { return r.body["organization"].(map[string]any)["id"].(string) }

	t.Run("an existing account becomes the owner", func(t *testing.T) {
		r := c.do(http.MethodPost, path, `{"name":"  Platform team ","slug":"platform","planKey":"capped","ownerEmail":"alice@alpha.example"}`, ah)
		org, _ := r.body["organization"].(map[string]any)
		m, _ := r.body["member"].(map[string]any)
		if r.code != http.StatusCreated || org["slug"] != "platform" || org["name"] != "Platform team" || org["planKey"] != "capped" || org["status"] != "active" ||
			m["userId"] != aliceID || m["role"] != "owner" || r.body["invitation"] != nil || r.body["inviteUrl"] != nil {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		id := orgOf(r)
		if n := countRows(t, env, `SELECT count(*) FROM projects WHERE tenant_id = $1 AND is_default`, id); n != 1 {
			t.Fatalf("default projects: %d", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.organization.created' AND tenant_id = $1 AND entity_type = 'organization'
			AND entity_id = $1::text AND actor_user_id = $2 AND ip IS NOT NULL AND metadata = '{"slug":"platform","planKey":"capped"}'`, id, adminID); n != 1 {
			t.Fatalf("created audit rows: %d", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.member.added' AND tenant_id = $1`, id); n != 1 {
			t.Fatalf("member audit rows: %d", n)
		}
		// The new owner can switch to it.
		orgs := c.do(http.MethodGet, "/api/v1/auth/organizations", "", alice)
		if !strings.Contains(orgs.raw, id) {
			t.Fatalf("alice's organizations: %s", orgs.raw)
		}
	})

	t.Run("without an account the owner gets a one-time link", func(t *testing.T) {
		r := c.do(http.MethodPost, path, `{"name":"Ops","ownerEmail":"newcomer@example.org"}`, ah)
		org, _ := r.body["organization"].(map[string]any)
		inv, _ := r.body["invitation"].(map[string]any)
		link, _ := r.body["inviteUrl"].(string)
		_, token, ok := strings.Cut(link, "#")
		if r.code != http.StatusCreated || !strings.HasPrefix(org["slug"].(string), "t-") || org["planKey"] != "internal" || r.body["member"] != nil ||
			inv["role"] != "owner" || inv["status"] != "open" || !ok || len(token) != 43 {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		if strings.Contains(logs.String(), token) || countRows(t, env, `SELECT count(*) FROM audit_log WHERE metadata::text LIKE '%' || $1 || '%'`, token) != 0 {
			t.Fatal("the invitation token leaked into the logs or an audit row")
		}
		reg := registerWithInvite(c, "newcomer@example.org", ownerPassword, token, "198.51.100.50")
		if reg.code != http.StatusCreated || reg.body["role"] != "owner" {
			t.Fatalf("register with the link: %d %s", reg.code, reg.raw)
		}
	})

	t.Run("validation and conflicts leave nothing behind", func(t *testing.T) {
		before := countRows(t, env, `SELECT count(*) FROM tenants`)
		for name, c2 := range map[string]struct {
			body string
			code int
		}{
			"empty name":       {`{"name":" ","ownerEmail":"alice@alpha.example"}`, http.StatusUnprocessableEntity},
			"bad slug":         {`{"name":"X","slug":"-x","ownerEmail":"alice@alpha.example"}`, http.StatusUnprocessableEntity},
			"bad email":        {`{"name":"X","ownerEmail":"nope"}`, http.StatusUnprocessableEntity},
			"missing email":    {`{"name":"X"}`, http.StatusBadRequest},
			"unknown plan":     {`{"name":"X","planKey":"nope","ownerEmail":"alice@alpha.example"}`, http.StatusUnprocessableEntity},
			"default slug":     {`{"name":"X","slug":"default","ownerEmail":"alice@alpha.example"}`, http.StatusConflict},
			"slug in use":      {`{"name":"X","slug":"platform","ownerEmail":"alice@alpha.example"}`, http.StatusConflict},
			"unknown property": {`{"name":"X","ownerEmail":"alice@alpha.example","status":"suspended"}`, http.StatusBadRequest},
		} {
			if r := c.do(http.MethodPost, path, c2.body, ah); r.code != c2.code {
				t.Errorf("%s: %d %s, want %d", name, r.code, r.raw, c2.code)
			}
		}
		if n := countRows(t, env, `SELECT count(*) FROM tenants`); n != before {
			t.Fatalf("tenants %d -> %d", before, n)
		}
	})

	t.Run("an owner that cannot be added releases its slug", func(t *testing.T) {
		pgtest.MustExec(t, env.AdminConn(t), pgtest.CappedPlanSQL)
		if _, err := env.AdminConn(t).Exec(t.Context(), `UPDATE plans SET limits = limits || '{"max_members":0}' WHERE key = 'capped'`); err != nil {
			t.Fatal(err)
		}
		r := c.do(http.MethodPost, path, `{"name":"Full","slug":"full","planKey":"capped","ownerEmail":"alice@alpha.example"}`, ah)
		if r.code != http.StatusConflict || !strings.Contains(r.raw, "plan-limit-reached") {
			t.Fatalf("over the limit: %d %s", r.code, r.raw)
		}
		if n := countRows(t, env, `SELECT count(*) FROM tenants WHERE slug = 'full'`); n != 0 {
			t.Fatalf("tenants still holding the slug: %d", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM tenants WHERE deleted_at IS NOT NULL AND slug <> 'full' AND name = 'Full'`); n != 1 {
			t.Fatalf("discarded tenant rows: %d", n)
		}
		// The slug is free again for a creation that works.
		pgtest.MustExec(t, env.AdminConn(t), pgtest.CappedPlanSQL)
		if _, err := env.AdminConn(t).Exec(t.Context(), `UPDATE plans SET limits = limits || '{"max_members":3}' WHERE key = 'capped'`); err != nil {
			t.Fatal(err)
		}
		if r := c.do(http.MethodPost, path, `{"name":"Full","slug":"full","planKey":"capped","ownerEmail":"alice@alpha.example"}`, ah); r.code != http.StatusCreated {
			t.Fatalf("retry with the same slug: %d %s", r.code, r.raw)
		}
	})

	t.Run("a disabled account cannot be the only owner", func(t *testing.T) {
		c.register("dormant@alpha.example")
		pgtest.MustExec(t, env.AdminConn(t), `UPDATE users SET status = 'disabled' WHERE email = 'dormant@alpha.example'`)
		r := c.do(http.MethodPost, path, `{"name":"Dormant","slug":"dormant","ownerEmail":"dormant@alpha.example"}`, ah)
		if r.code != http.StatusConflict || !strings.Contains(r.raw, "last-owner") {
			t.Fatalf("disabled owner: %d %s", r.code, r.raw)
		}
		if n := countRows(t, env, `SELECT count(*) FROM tenants WHERE slug = 'dormant'`); n != 0 {
			t.Fatalf("tenants holding the slug: %d", n)
		}
	})

	t.Run("only a platform admin", func(t *testing.T) {
		if r := c.do(http.MethodPost, path, `{"name":"X","ownerEmail":"alice@alpha.example"}`, alice); r.code != http.StatusForbidden {
			t.Fatalf("member: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, path, `{"name":"X","ownerEmail":"alice@alpha.example"}`, nil); r.code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d %s", r.code, r.raw)
		}
	})
}
