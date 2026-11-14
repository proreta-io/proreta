package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/api"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationAdminStatusPageDomains(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiadmindomains"))
	c := newAdminServerWith(t, env, &lockedBuffer{}, func(cfg *api.Config) { cfg.StatusPageCustomDomains = true })
	ctx := context.Background()
	cookie, csrf := c.login()
	ah := withCSRF(cookie, csrf)
	adminConn := env.AdminConn(t)
	public := env.Conn(t, env.StatuspageURL)

	ownerCookie, ownerCSRF, regA := c.register("owner@alpha.example")
	oh := withCSRF(ownerCookie, ownerCSRF)
	tenantA := regA.body["organization"].(map[string]any)["id"].(string)
	_, _, regB := c.register("owner@beta.example")
	tenantB := regB.body["organization"].(map[string]any)["id"].(string)
	var adminID string
	if err := adminConn.QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	var orgSlugA string
	if err := adminConn.QueryRow(ctx, `SELECT slug FROM tenants WHERE id = $1`, tenantA).Scan(&orgSlugA); err != nil {
		t.Fatal(err)
	}
	pageA, pageB := uuid.NewString(), uuid.NewString()
	pgtest.MustExec(t, adminConn, fmt.Sprintf(
		`INSERT INTO status_pages (id, tenant_id, slug, title, published) VALUES ('%s', '%s', 'alpha-status', 'Alpha', true), ('%s', '%s', 'beta-status', 'Beta', true)`,
		pageA, tenantA, pageB, tenantB))
	add := func(tenant, page, host, state string, set ...string) string {
		id := uuid.NewString()
		pgtest.MustExec(t, adminConn, fmt.Sprintf(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token) VALUES ('%s', '%s', '%s', '%s', '%s', '%s')`,
			id, tenant, page, host, state, strings.Repeat("a", 32)))
		for _, s := range set {
			pgtest.MustExec(t, adminConn, `UPDATE status_page_domains SET `+s+` WHERE id = '`+id+`'`)
		}
		return id
	}
	// Oldest first, so the newest-first list is d4, d3, d2, d1.
	d1 := add(tenantA, pageA, "status.alpha.com", "active", "verified_at = now()")
	d2 := add(tenantA, pageA, "wait.alpha.com", "pending")
	d3 := add(tenantB, pageB, "status.beta.io", "verified", "verified_at = now()")
	d4 := add(tenantB, pageB, "old.beta.io", "failed", "last_error = 'txt-missing'")
	add(tenantB, pageB, "released.beta.io", "verified", "released_at = now()")
	served := func(host string) (lookup, allowed bool) {
		var slug string
		rows, err := public.Query(ctx, `SELECT slug FROM status_page_domain_lookup($1)`, host)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			_ = rows.Scan(&slug)
			lookup = true
		}
		rows.Close()
		if err := public.QueryRow(ctx, `SELECT status_page_domain_allowed($1)`, host).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		return lookup, allowed
	}
	list := func(q string) response {
		return c.do(http.MethodGet, "/api/v1/admin/status-page-domains"+q, "", ah)
	}
	ids := func(r response) []string {
		var out []string
		for _, it := range r.body["items"].([]any) {
			out = append(out, it.(map[string]any)["id"].(string))
		}
		return out
	}
	equal := func(got, want []string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }

	t.Run("list filters and pages", func(t *testing.T) {
		r := list("")
		if r.code != http.StatusOK || r.body["total"] != float64(4) || !equal(ids(r), []string{d4, d3, d2, d1}) {
			t.Fatalf("all: %d %s (released ones are not listed, newest first)", r.code, r.raw)
		}
		first := r.body["items"].([]any)[3].(map[string]any)
		if first["host"] != "status.alpha.com" || first["state"] != "active" || first["organizationSlug"] != orgSlugA || first["statusPageSlug"] != "alpha-status" ||
			first["disabled"] != false || first["verifiedAt"] == nil || strings.Contains(r.raw, strings.Repeat("a", 32)) {
			t.Fatalf("item: %v (the token never leaves)", first)
		}
		if r := list("?state=active,verified"); !equal(ids(r), []string{d3, d1}) || r.body["total"] != float64(2) {
			t.Fatalf("state filter: %s", r.raw)
		}
		if r := list("?state=failed"); !equal(ids(r), []string{d4}) || r.body["items"].([]any)[0].(map[string]any)["lastError"] != "txt-missing" {
			t.Fatalf("failed: %s", r.raw)
		}
		if r := list("?state=bogus"); r.code != http.StatusBadRequest && r.code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown state: %d", r.code)
		}
		if r := list("?query=BETA.io"); !equal(ids(r), []string{d4, d3}) {
			t.Fatalf("search by host: %s", r.raw)
		}
		if r := list("?query=" + url.QueryEscape(orgSlugA)); !equal(ids(r), []string{d2, d1}) {
			t.Fatalf("search by organization: %s", r.raw)
		}
		if r := list("?query=nothing-like-this"); len(ids(r)) != 0 || r.body["total"] != float64(0) {
			t.Fatalf("no match: %s", r.raw)
		}
		if r := list("?limit=2&offset=2"); !equal(ids(r), []string{d2, d1}) || r.body["total"] != float64(4) || r.body["limit"] != float64(2) || r.body["offset"] != float64(2) {
			t.Fatalf("second page: %s", r.raw)
		}
		if r := list("?limit=2&offset=10"); len(ids(r)) != 0 || r.body["total"] != float64(4) {
			t.Fatalf("past the end keeps the total: %s", r.raw)
		}
	})

	t.Run("only a platform admin", func(t *testing.T) {
		for _, m := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/admin/status-page-domains", ""},
			{http.MethodPost, "/api/v1/admin/status-page-domains/" + d1 + "/disable", `{"reason":"x"}`},
			{http.MethodPost, "/api/v1/admin/status-page-domains/" + d1 + "/enable", `{"reason":"x"}`},
		} {
			if r := c.do(m.method, m.path, m.body, oh); r.code != http.StatusForbidden {
				t.Errorf("owner %s %s: %d", m.method, m.path, r.code)
			}
			if r := c.do(m.method, m.path, m.body, nil); r.code != http.StatusUnauthorized {
				t.Errorf("anonymous %s %s: %d", m.method, m.path, r.code)
			}
		}
	})

	t.Run("disable stops serving, enable restores it, both are audited", func(t *testing.T) {
		if l, a := served("status.alpha.com"); !l || !a {
			t.Fatalf("before: lookup %v allowed %v", l, a)
		}
		base := "/api/v1/admin/status-page-domains/" + d1
		r := c.do(http.MethodPost, base+"/disable", `{"reason":"phishing report 4711"}`, ah)
		if r.code != http.StatusOK || r.body["disabled"] != true || r.body["disabledAt"] == nil || r.body["id"] != d1 {
			t.Fatalf("disable: %d %s", r.code, r.raw)
		}
		if l, a := served("status.alpha.com"); l || a {
			t.Fatalf("disabled: lookup %v allowed %v, want neither", l, a)
		}
		var meta string
		if err := adminConn.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'admin.status_page_domain.disabled' AND tenant_id = $1 AND actor_user_id = $2 AND entity_id = $3`,
			tenantA, adminID, d1).Scan(&meta); err != nil {
			t.Fatalf("audit row: %v", err)
		}
		if !strings.Contains(meta, `"reason": "phishing report 4711"`) || !strings.Contains(meta, `"host": "status.alpha.com"`) || !strings.Contains(meta, `"pageId"`) {
			t.Fatalf("audit metadata = %s", meta)
		}
		// Again: the answer is the same and nothing more is written.
		if r := c.do(http.MethodPost, base+"/disable", `{"reason":"again"}`, ah); r.code != http.StatusOK || r.body["disabled"] != true {
			t.Fatalf("disable twice: %d %s", r.code, r.raw)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.status_page_domain.disabled'`); n != 1 {
			t.Fatalf("disabled audit rows = %d, want 1", n)
		}
		if r := list("?state=active"); r.body["items"].([]any)[0].(map[string]any)["disabled"] != true {
			t.Fatalf("the list shows it disabled: %s", r.raw)
		}
		r = c.do(http.MethodPost, base+"/enable", `{"reason":"false alarm"}`, ah)
		if r.code != http.StatusOK || r.body["disabled"] != false || r.body["disabledAt"] != nil {
			t.Fatalf("enable: %d %s", r.code, r.raw)
		}
		if l, a := served("status.alpha.com"); !l || !a {
			t.Fatalf("enabled: lookup %v allowed %v, want both", l, a)
		}
		if r := c.do(http.MethodPost, base+"/enable", `{"reason":"again"}`, ah); r.code != http.StatusOK {
			t.Fatalf("enable twice: %d", r.code)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.status_page_domain.enabled' AND tenant_id = $1`, tenantA); n != 1 {
			t.Fatalf("enabled audit rows = %d, want 1", n)
		}
	})

	t.Run("a reason is required and nothing changes without one", func(t *testing.T) {
		base := "/api/v1/admin/status-page-domains/" + d3
		for name, body := range map[string]string{"empty": `{"reason":""}`, "blank": `{"reason":"   "}`, "missing": `{}`, "too long": `{"reason":"` + strings.Repeat("x", 501) + `"}`} {
			if r := c.do(http.MethodPost, base+"/disable", body, ah); r.code != http.StatusBadRequest && r.code != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		if l, _ := served("status.beta.io"); !l {
			t.Fatal("the host stopped being served without a valid reason")
		}
		if r := c.do(http.MethodPost, base+"/disable", `{"reason":"`+strings.Repeat("x", 500)+`"}`, ah); r.code != http.StatusOK {
			t.Fatalf("a 500 character reason: %d %s", r.code, r.raw)
		}
	})

	t.Run("unknown, released and deleted organizations", func(t *testing.T) {
		missing := "/api/v1/admin/status-page-domains/" + uuid.NewString()
		var released string
		if err := adminConn.QueryRow(ctx, `SELECT id::text FROM status_page_domains WHERE host = 'released.beta.io'`).Scan(&released); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{missing, "/api/v1/admin/status-page-domains/" + released} {
			if r := c.do(http.MethodPost, p+"/disable", `{"reason":"x"}`, ah); r.code != http.StatusNotFound {
				t.Errorf("%s: %d", p, r.code)
			}
		}
		// A deleted organization is read-only: the domain no longer serves, and cannot be changed.
		if r := c.do(http.MethodDelete, "/api/v1/admin/organizations/"+tenantB, "", ah); r.code != http.StatusNoContent && r.code != http.StatusOK {
			t.Fatalf("delete organization: %d %s", r.code, r.raw)
		}
		r := c.do(http.MethodPost, "/api/v1/admin/status-page-domains/"+d4+"/disable", `{"reason":"x"}`, ah)
		if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:organization-deleted" {
			t.Fatalf("deleted organization: %d %s", r.code, r.raw)
		}
		if l, a := served("status.beta.io"); l || a {
			t.Fatalf("a deleted organization's host is served: lookup %v allowed %v", l, a)
		}
		if r := list("?query=beta.io"); r.code != http.StatusOK || r.body["total"] != float64(2) {
			t.Fatalf("the list still shows the deleted organization's domains: %s", r.raw)
		}
	})

	t.Run("with the feature off the operations answer 404", func(t *testing.T) {
		off := newAdminServer(t, pgtest.New(t, pgtest.DBName("apiadmindomainsoff")), &lockedBuffer{})
		cookie, csrf := off.login()
		h := withCSRF(cookie, csrf)
		for _, m := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/admin/status-page-domains", ""},
			{http.MethodPost, "/api/v1/admin/status-page-domains/" + uuid.NewString() + "/disable", `{"reason":"x"}`},
		} {
			if r := off.do(m.method, m.path, m.body, h); r.code != http.StatusNotFound || r.body["type"] != "urn:proreta:problem:custom-domains-disabled" {
				t.Errorf("%s: %d %s", m.path, r.code, r.raw)
			}
		}
	})
}
