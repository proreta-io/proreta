package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationAdminOrganizationSettings(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiadminsettings"))
	c := newAdminServer(t, env, &lockedBuffer{})
	ctx := context.Background()
	cookie, csrf := c.login()
	ah := withCSRF(cookie, csrf)

	ownerCookie, ownerCSRF, reg := c.register("owner@alpha.example")
	oh := withCSRF(ownerCookie, ownerCSRF)
	owner := reg.body["user"].(map[string]any)["id"].(string)
	tenant := reg.body["organization"].(map[string]any)["id"].(string)
	_, _, regB := c.register("owner@beta.example")
	other := regB.body["organization"].(map[string]any)["id"].(string)
	var adminID string
	if err := env.AdminConn(t).QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	// Domains are claimed with the owner's Google proof, which the test does not have: seed rows.
	pgtest.MustExec(t, env.AdminConn(t), fmt.Sprintf(
		`INSERT INTO org_auth_domains (id, tenant_id, domain, default_role, created_by) VALUES
		 ('0192b9c4-7c5e-7000-8000-0000000000a1', '%[1]s', 'alpha.example', 'viewer', '%[3]s'),
		 ('0192b9c4-7c5e-7000-8000-0000000000b1', '%[2]s', 'beta.example', 'viewer', '%[3]s')`, tenant, other, owner))
	base := "/api/v1/admin/organizations/" + tenant
	domainID := "0192b9c4-7c5e-7000-8000-0000000000a1"
	audit := func(action string) int {
		return countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = $1 AND tenant_id = $2 AND actor_user_id = $3`, action, tenant, adminID)
	}

	t.Run("reads show what the owner sees", func(t *testing.T) {
		ch := c.do(http.MethodPost, "/api/v1/channels", `{"name":"ops","webhookUrl":"`+slackURL+`"}`, oh)
		if ch.code != http.StatusCreated {
			t.Fatalf("owner channel: %d %s", ch.code, ch.raw)
		}
		if r := c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"alpha-status","title":"Alpha"}`, oh); r.code != http.StatusCreated {
			t.Fatalf("owner status page: %d %s", r.code, r.raw)
		}
		r := c.do(http.MethodGet, base+"/usage", "", ah)
		if r.code != http.StatusOK || r.body["plan"] == nil || r.body["limits"] == nil || r.body["usage"].(map[string]any)["channels"] != float64(1) {
			t.Fatalf("usage: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodGet, base+"/channels", "", ah)
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["name"] != "ops" || strings.Contains(r.raw, "SECRET") {
			t.Fatalf("channels (the webhook stays write-only): %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodGet, base+"/status-pages", "", ah)
		items, _ = r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["slug"] != "alpha-status" {
			t.Fatalf("status pages: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodGet, base+"/auth-domains", "", ah)
		items, _ = r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["domain"] != "alpha.example" {
			t.Fatalf("domains: %d %s", r.code, r.raw)
		}
		for _, p := range []string{"/usage", "/channels", "/status-pages", "/auth-domains", "/specula"} {
			if r := c.do(http.MethodGet, "/api/v1/admin/organizations/0192b9c4-7c5e-7000-8000-000000000999"+p, "", ah); r.code != http.StatusNotFound {
				t.Errorf("unknown organization %s: %d", p, r.code)
			}
			if r := c.do(http.MethodGet, base+p, "", oh); r.code != http.StatusForbidden {
				t.Errorf("owner %s: %d", p, r.code)
			}
		}
	})

	t.Run("sign-in domains", func(t *testing.T) {
		r := c.do(http.MethodPatch, base+"/auth-domains/"+domainID, `{"defaultRole":"member","enabled":false}`, ah)
		if r.code != http.StatusOK || r.body["defaultRole"] != "member" || r.body["enabled"] != false {
			t.Fatalf("update: %d %s", r.code, r.raw)
		}
		if audit("admin.auth_domain.updated") != 1 || countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'organization.auth_domain_changed'`) != 0 {
			t.Fatalf("audit: admin %d", audit("admin.auth_domain.updated"))
		}
		if r := c.do(http.MethodPatch, base+"/auth-domains/"+domainID, `{"defaultRole":"owner"}`, ah); r.code != http.StatusBadRequest && r.code != http.StatusUnprocessableEntity {
			t.Fatalf("bad role: %d", r.code)
		}
		// A domain of another organization is not found under this one.
		for _, m := range []string{http.MethodPatch, http.MethodDelete} {
			body := ""
			if m == http.MethodPatch {
				body = `{"enabled":true}`
			}
			if r := c.do(m, base+"/auth-domains/0192b9c4-7c5e-7000-8000-0000000000b1", body, ah); r.code != http.StatusNotFound {
				t.Fatalf("%s foreign domain: %d %s", m, r.code, r.raw)
			}
		}
		if r := c.do(http.MethodDelete, base+"/auth-domains/"+domainID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", r.code, r.raw)
		}
		if audit("admin.auth_domain.deleted") != 1 {
			t.Fatalf("delete audit = %d", audit("admin.auth_domain.deleted"))
		}
		if n := countRows(t, env, `SELECT count(*) FROM org_auth_domains`); n != 1 {
			t.Fatalf("domains left = %d, want only the other organization's", n)
		}
		// The owner cannot reach the admin path, and the admin cannot claim a domain.
		if r := c.do(http.MethodPost, base+"/auth-domains", `{"domain":"x.example"}`, ah); r.code != http.StatusMethodNotAllowed && r.code != http.StatusNotFound {
			t.Fatalf("admin claim: %d", r.code)
		}
	})

	t.Run("specula", func(t *testing.T) {
		r := c.do(http.MethodPost, base+"/specula", `{"name":"Prod cluster"}`, ah)
		token, _ := r.body["token"].(string)
		sp, _ := r.body["specula"].(map[string]any)
		if r.code != http.StatusCreated || !strings.HasPrefix(token, "spc_") || sp["name"] != "Prod cluster" {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		id := sp["id"].(string)
		// The plan limit of the organization applies to the admin too (Free: one Specula).
		if r := c.do(http.MethodPost, base+"/specula", `{"name":"Second"}`, ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached" {
			t.Fatalf("plan limit: %d %s", r.code, r.raw)
		}
		// The owner sees it too, and the list never returns the token.
		if r := c.do(http.MethodGet, "/api/v1/specula", "", oh); r.code != http.StatusOK || !strings.Contains(r.raw, "Prod cluster") {
			t.Fatalf("owner list: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodPatch, base+"/specula/"+id, `{"name":"Prod EU"}`, ah)
		if r.code != http.StatusOK || r.body["name"] != "Prod EU" {
			t.Fatalf("update: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPatch, "/api/v1/admin/organizations/"+other+"/specula/"+id, `{"name":"x"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("update under another organization: %d", r.code)
		}
		if r := c.do(http.MethodDelete, "/api/v1/admin/organizations/"+other+"/specula/"+id, "", ah); r.code != http.StatusNotFound {
			t.Fatalf("revoke under another organization: %d", r.code)
		}
		if r := c.do(http.MethodDelete, base+"/specula/"+id, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, base+"/specula/"+id, "", ah); r.code != http.StatusNotFound {
			t.Fatalf("revoke twice: %d", r.code)
		}
		r = c.do(http.MethodGet, base+"/specula", "", ah)
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["status"] != "revoked" || strings.Contains(r.raw, token) {
			t.Fatalf("list keeps the revoked one: %d %s", r.code, r.raw)
		}
		if audit("admin.specula.created") != 1 || audit("admin.specula.updated") != 1 || audit("admin.specula.revoked") != 1 ||
			countRows(t, env, `SELECT count(*) FROM audit_log WHERE action LIKE 'specula.%'`) != 0 {
			t.Fatalf("audit created/updated/revoked = %d/%d/%d", audit("admin.specula.created"), audit("admin.specula.updated"), audit("admin.specula.revoked"))
		}
	})

	t.Run("deleted organization is read-only", func(t *testing.T) {
		pgtest.MustExec(t, env.AdminConn(t), fmt.Sprintf(`UPDATE tenants SET deleted_at = now() WHERE id = '%s'`, other))
		o := "/api/v1/admin/organizations/" + other
		if r := c.do(http.MethodGet, o+"/channels", "", ah); r.code != http.StatusOK {
			t.Fatalf("read: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, o+"/usage", "", ah); r.code != http.StatusConflict {
			t.Fatalf("usage of a deleted organization: %d %s", r.code, r.raw)
		}
		for _, call := range [][3]string{
			{http.MethodPost, o + "/specula", `{"name":"x"}`},
			{http.MethodPatch, o + "/auth-domains/0192b9c4-7c5e-7000-8000-0000000000b1", `{"enabled":false}`},
			{http.MethodDelete, o + "/auth-domains/0192b9c4-7c5e-7000-8000-0000000000b1", ""},
		} {
			if r := c.do(call[0], call[2:][0][:0]+call[1], call[2], ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:organization-deleted" {
				t.Errorf("%s %s: %d %s", call[0], call[1], r.code, r.raw)
			}
		}
	})
}
