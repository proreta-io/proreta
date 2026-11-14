package public_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/internal/statuspages/public"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationHostModeOverTheDatabase runs the renderer in host mode as proreta_statuspage: a
// verified or active domain of a published page serves it at the root; a pending, failed, disabled
// or released domain, an unpublished or deleted page, a suspended organization and an unknown host
// all get one byte-identical 404; the role has no table rights, only the definers.
func TestIntegrationHostModeOverTheDatabase(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("statuspagehost"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	tenant, other, user := uuid.New(), uuid.New(), uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name, limits) VALUES ('`+tenant.String()+`', 'hostmode', 'H', '{"max_status_pages": 10}'), ('`+other.String()+`', 'hostother', 'O', '{}')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'hostmode@example.com')`)
	svc := statuspages.NewService(env.Pool(t, env.APIURL, 2), statuspages.Options{HashParams: &crypto.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	a := statuspages.Actor{TenantID: tenant, Access: authz.Actor{UserID: user, Role: authz.Maintainer}}
	yes := true
	newPage := func(slug string, published bool) uuid.UUID {
		p, err := svc.Create(ctx, a, statuspages.CreateInput{Slug: slug, Title: "Page " + slug})
		if err != nil {
			t.Fatal(err)
		}
		if published {
			if _, err := svc.Update(ctx, a, p.ID, statuspages.UpdateInput{Published: &yes}); err != nil {
				t.Fatal(err)
			}
		}
		return p.ID
	}
	pub, draft, gone := newPage("host-pub", true), newPage("host-draft", false), newPage("host-gone", true)
	otherPage := uuid.New()
	pgtest.MustExec(t, admin, fmt.Sprintf(`INSERT INTO status_pages (id, tenant_id, slug, title, published) VALUES ('%s', '%s', 'host-other', 'Other', true)`, otherPage, other))

	n := 0
	domain := func(tenantID, pageID uuid.UUID, host, state, extra string) {
		t.Helper()
		n++
		cols, vals := "", ""
		if extra != "" { // "column=value"
			c, v, _ := strings.Cut(extra, "=")
			cols, vals = ", "+c, ", "+v
		}
		pgtest.MustExec(t, admin, fmt.Sprintf(
			`INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token%s) VALUES ('%s', '%s', '%s', '%s', '%s', '%032x'%s)`,
			cols, uuid.New(), tenantID, pageID, host, state, n, vals))
	}
	domain(tenant, pub, "verified.acme.com", "verified", "")
	domain(tenant, pub, "active.acme.com", "active", "")
	domain(tenant, pub, "pending.acme.com", "pending", "")
	domain(tenant, pub, "failed.acme.com", "failed", "")
	domain(tenant, pub, "disabled.acme.com", "active", "disabled_at=now()")
	domain(tenant, pub, "released.acme.com", "active", "released_at=now()")
	domain(tenant, draft, "draft.acme.com", "active", "")
	domain(tenant, gone, "deleted.acme.com", "active", "")
	domain(other, otherPage, "suspended.acme.com", "active", "")
	if err := svc.Delete(ctx, a, gone); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `UPDATE tenants SET status = 'suspended' WHERE id = '`+other.String()+`'`)

	// A new renderer per request stands for a cache miss.
	pool := env.Pool(t, env.StatuspageURL, 2)
	serve := func(host, path string) (int, string, http.Header) {
		t.Helper()
		srv := httptest.NewServer(public.NewHandler(public.Config{PublicURL: "https://proreta.example.com", TrustedProxyHops: 1, CookieSecure: true,
			CustomDomains: true, InstallationHosts: []string{"proreta.example.com"}},
			public.Deps{Source: public.DBSource{Pool: pool}, Cookies: public.NewCookieSigner(make([]byte, 32))}))
		defer srv.Close()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = host
		req.Header.Set("X-Forwarded-For", "203.0.113.5")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b), r.Header
	}

	for _, host := range []string{"verified.acme.com", "active.acme.com"} {
		code, body, hd := serve(host, "/")
		if code != http.StatusOK || !strings.Contains(body, "Page host-pub") || !strings.Contains(body, `<link rel="canonical" href="https://`+host+`/">`) {
			t.Fatalf("%s: %d\n%s", host, code, body)
		}
		if hd.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS without a forwarded scheme", host)
		}
		if code, body, _ := serve(host, "/status.json"); code != http.StatusOK || !strings.Contains(body, `"slug":"host-pub"`) {
			t.Errorf("%s json: %d %s", host, code, body)
		}
	}
	// Everything else is the answer an unknown host gets.
	unknownCode, unknownBody, unknownHd := serve("never-existed.acme.com", "/")
	if unknownCode != http.StatusNotFound {
		t.Fatalf("unknown host: %d", unknownCode)
	}
	for _, host := range []string{
		"pending.acme.com", "failed.acme.com", "disabled.acme.com", "released.acme.com", "draft.acme.com", "deleted.acme.com",
		"suspended.acme.com", "proreta.example.com.evil.example", "192.0.2.10",
	} {
		code, body, hd := serve(host, "/")
		if code != unknownCode || body != unknownBody || hd.Get("Cache-Control") != unknownHd.Get("Cache-Control") ||
			hd.Get("Content-Security-Policy") != unknownHd.Get("Content-Security-Policy") {
			t.Errorf("%s answered %d, not the unknown-host 404", host, code)
		}
	}
	// A verified host never serves a slug path of any page, its own included.
	for _, path := range []string{"/status/host-pub", "/status/host-other", "/status/host-pub.json"} {
		if code, body, _ := serve("verified.acme.com", path); code != unknownCode || body != unknownBody {
			t.Errorf("%s: %d", path, code)
		}
	}

	// The renderer's role: only the definers, no table.
	conn := env.Conn(t, env.StatuspageURL)
	if _, err := conn.Exec(ctx, `SELECT 1 FROM status_page_domains`); err == nil {
		t.Fatal("proreta_statuspage can read status_page_domains")
	}
	rows, err := conn.Query(ctx, `SELECT * FROM status_page_domain_lookup('verified.acme.com')`)
	if err != nil {
		t.Fatal(err)
	}
	if cols := rows.FieldDescriptions(); len(cols) != 1 || cols[0].Name != "slug" {
		t.Errorf("lookup returns %d columns", len(cols))
	}
	rows.Close()
}
