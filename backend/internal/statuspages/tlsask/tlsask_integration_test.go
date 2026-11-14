package tlsask_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
	"github.com/proreta-io/proreta/backend/internal/statuspages/tlsask"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	tenant   = "01920000-0000-7000-8000-0000000000a1"
	pageLive = "01920000-0000-7000-8000-0000000000a2"
	pageDraf = "01920000-0000-7000-8000-0000000000a3"
	tenantS  = "01920000-0000-7000-8000-0000000000b1"
	pageS    = "01920000-0000-7000-8000-0000000000b2"
	token    = "0123456789abcdef0123456789abcdef"
)

// The endpoint asks the database through the real proreta_statuspage role, which has no table
// rights, so the answer comes from status_page_domain_allowed alone.
func TestIntegrationAskThroughTheStatuspageRole(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("tlsask"))
	admin := env.AdminConn(t)
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenant+`', 'acme', 'Acme')`,
		`INSERT INTO tenants (id, slug, name, status) VALUES ('`+tenantS+`', 'sus', 'Sus', 'suspended')`,
		`INSERT INTO status_pages (id, tenant_id, slug, title, published) VALUES
		 ('`+pageLive+`', '`+tenant+`', 'acme-live', 'Live', true), ('`+pageDraf+`', '`+tenant+`', 'acme-draft', 'Draft', false), ('`+pageS+`', '`+tenantS+`', 'sus-page', 'Sus', true)`)
	n := 0
	add := func(tenantID, page, host, state, extra string) {
		n++
		pgtest.MustExec(t, admin, fmt.Sprintf(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token) VALUES ('01920000-0000-7000-8000-00000000c0%02d', '%s', '%s', '%s', '%s', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
			n, tenantID, page, host, state))
		if extra != "" {
			pgtest.MustExec(t, admin, fmt.Sprintf(`UPDATE status_page_domains SET %s WHERE host = '%s'`, extra, host))
		}
	}
	add(tenant, pageLive, "active.acme.com", "active", "")
	add(tenant, pageLive, "verified.acme.com", "verified", "")
	add(tenant, pageDraf, "draft.acme.com", "verified", "") // an unpublished page may still get a certificate
	add(tenant, pageLive, "pending.acme.com", "pending", "")
	add(tenant, pageLive, "failed.acme.com", "failed", "")
	add(tenant, pageLive, "disabled.acme.com", "active", "disabled_at = now()")
	add(tenant, pageLive, "released.acme.com", "active", "released_at = now()")
	add(tenantS, pageS, "suspended.acme.com", "active", "")

	h, err := tlsask.New(tlsask.DBAllower{Pool: env.Pool(t, env.StatuspageURL, 2)}, tlsask.Options{Token: token, Rules: domains.NewRules(nil, nil), CacheTTL: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	ask := func(host string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tlsask.Path+"?token="+token+"&domain="+host, nil))
		if rec.Body.Len() != 0 {
			t.Errorf("%s: body %q, want none", host, rec.Body.String())
		}
		return rec.Code
	}
	for host, want := range map[string]int{
		"active.acme.com": 200, "verified.acme.com": 200, "Verified.Acme.com.": 200, "draft.acme.com": 200,
		"pending.acme.com": 404, "failed.acme.com": 404, "disabled.acme.com": 404, "released.acme.com": 404,
		"suspended.acme.com": 404, "unknown.acme.com": 404,
	} {
		if got := ask(host); got != want {
			t.Errorf("%s: %d, want %d", host, got, want)
		}
	}
	// Disabling takes effect on the next lookup (the cache is one nanosecond here).
	pgtest.MustExec(t, admin, `UPDATE status_page_domains SET disabled_at = now() WHERE host = 'active.acme.com'`)
	time.Sleep(time.Millisecond)
	if got := ask("active.acme.com"); got != 404 {
		t.Errorf("after disabling: %d, want 404", got)
	}
}
