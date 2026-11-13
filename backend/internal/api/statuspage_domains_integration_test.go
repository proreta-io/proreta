package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/api"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const domainCNAMETarget = "customers.proreta.example.net"

// domainWorld is a projectWorld with custom domains turned on, a fake DNS and a fake clock (for the
// verify rate limits).
type domainWorld struct {
	*projectWorld
	sp   *statusPages
	dns  *domains.Fake
	clk  *clock.Fake
	hdrO map[string]string
	// spread moves the fake clock past the verify rate limit after every verify call.
	spread bool
	adm    *pgx.Conn
}

// admin is one superuser connection for the whole test (every AdminConn stays open until the end).
func (w *domainWorld) admin(t *testing.T) *pgx.Conn {
	t.Helper()
	if w.adm == nil {
		w.adm = w.env.AdminConn(t)
	}
	return w.adm
}

func newDomainWorld(t *testing.T, name string, domainLimit int) *domainWorld {
	t.Helper()
	dw := &domainWorld{
		spread: true,
		dns:    &domains.Fake{TXT: map[string][]string{}, CNAME: map[string]string{}, Fail: map[string]bool{}},
		clk:    clock.NewFake(time.Now()),
	}
	domainsHook = func(cfg *api.Config, o *statuspages.Options) {
		cfg.StatusPageCustomDomains = true
		o.Clock = dw.clk
		o.Domains = statuspages.DomainOptions{Resolver: dw.dns, Rules: domains.NewRules([]string{"proreta.example.com"}, []string{"example.org"}), CNAMETarget: domainCNAMETarget}
	}
	t.Cleanup(func() { domainsHook = nil })
	dw.projectWorld = newProjectWorld(t, name)
	dw.sp = dw.addStatusPages(t)
	dw.limit(t, domainLimit)
	dw.hdrO = dw.hdr("owner")
	return dw
}

func (w *domainWorld) limit(t *testing.T, n int) {
	t.Helper()
	pgtest.MustExec(t, w.env.AdminConn(t), fmt.Sprintf(`UPDATE tenants SET limits = '{"max_status_pages": 50, "max_status_page_domains": %d}'`, n))
}

func (w *domainWorld) domainsPath(page string) string {
	return "/api/v1/status-pages/" + page + "/domains"
}

func (w *domainWorld) add(t *testing.T, page, host string, hdr map[string]string) response {
	t.Helper()
	return w.c.do(http.MethodPost, w.domainsPath(page), `{"host":"`+host+`"}`, hdr)
}

// publish puts the TXT proof of a domain (the tokens of the given add responses) in the fake DNS.
func (w *domainWorld) publish(host string, adds ...response) {
	var v []string
	for _, a := range adds {
		v = append(v, a.body["verification"].(map[string]any)["txtValue"].(string))
	}
	w.dns.TXT[domains.TXTName(host)] = v
}

func (w *domainWorld) verify(page, id string, hdr map[string]string) response {
	r := w.c.do(http.MethodPost, w.domainsPath(page)+"/"+id+"/verify", "", hdr)
	if w.spread {
		w.clk.Advance(61 * time.Second)
	}
	return r
}

func (w *domainWorld) audits(t *testing.T, like string) []string {
	t.Helper()
	rows, err := w.admin(t).Query(context.Background(), `SELECT action || ' ' || metadata::text FROM audit_log WHERE action LIKE $1 ORDER BY created_at, id`, like)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (w *domainWorld) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.admin(t).QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seed stores a domain on a page straight in the database, as the owner would have added it.
func (w *domainWorld) seed(t *testing.T, page, host, state string, released bool) string {
	t.Helper()
	var id string
	err := w.admin(t).QueryRow(context.Background(), `INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token, released_at)
		SELECT gen_random_uuid(), tenant_id, id, $2, $3, md5(random()::text), CASE WHEN $4 THEN now() END FROM status_pages WHERE id = $1 RETURNING id::text`,
		page, host, state, released).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func hasAudit(list []string, want ...string) bool {
	for _, a := range list {
		ok := true
		for _, p := range want {
			ok = ok && strings.Contains(a, p)
		}
		if ok {
			return true
		}
	}
	return false
}

// TestIntegrationStatusPageDomainsLifecycle covers adding (forbidden hosts, normalization, DNS
// records), every verify outcome including a DNS error, primary switching, removal, the audit rows,
// the usage count and the release of domains by a page deletion.
func TestIntegrationStatusPageDomainsLifecycle(t *testing.T) {
	w := newDomainWorld(t, "domainlife", 10)
	page := w.sp.byProject["alpha"]
	owner := w.hdrO

	// Forbidden hosts are 422 domain-forbidden with the reason in fieldErrors.
	for _, host := range []string{"acme.com", "10.0.0.1", "proreta.example.com", "x.example.org", "https://status.acme.com", "status.acme.com:443", "*.acme.com", "localhost", "com", "stätus.acme.com"} {
		r := w.add(t, page, host, owner)
		if r.code != http.StatusUnprocessableEntity || r.body["type"] != "urn:proreta:problem:domain-forbidden" || !strings.Contains(r.raw, `"host"`) {
			t.Fatalf("add %q: %d %s", host, r.code, r.raw)
		}
	}

	// A valid host is normalized and comes with the records to publish.
	a := w.add(t, page, "Status.Acme.com.", owner)
	v, _ := a.body["verification"].(map[string]any)
	if a.code != http.StatusCreated || a.body["host"] != "status.acme.com" || a.body["state"] != "pending" || a.body["primary"] != false ||
		v["txtName"] != "_proreta-status.status.acme.com" || !strings.HasPrefix(v["txtValue"].(string), "proreta-verify=") || v["cnameTarget"] != domainCNAMETarget ||
		a.body["expiresAt"] == nil || a.body["lastError"] != nil || a.body["verifiedAt"] != nil {
		t.Fatalf("add: %d %s", a.code, a.raw)
	}
	id := a.body["id"].(string)
	if r := w.add(t, page, "status.acme.com", owner); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "already added") {
		t.Fatalf("duplicate: %d %s", r.code, r.raw)
	}
	if r := w.add(t, w.sp.byProject["beta"], "status.acme.com", owner); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate on another page of the organization: %d %s", r.code, r.raw)
	}

	// Nothing in DNS: pending, txt-missing.
	r := w.verify(page, id, owner)
	if r.code != http.StatusAccepted || r.body["state"] != "pending" || r.body["lastError"] != "txt-missing" || r.body["lastCheckedAt"] == nil {
		t.Fatalf("verify, no records: %d %s", r.code, r.raw)
	}
	// A DNS error is not a missing record.
	w.dns.Fail[domains.TXTName("status.acme.com")] = true
	r = w.verify(page, id, owner)
	if r.code != http.StatusAccepted || r.body["state"] != "pending" || r.body["lastError"] != "dns-error" {
		t.Fatalf("verify, dns error: %d %s", r.code, r.raw)
	}
	delete(w.dns.Fail, domains.TXTName("status.acme.com"))
	// A CNAME alone never verifies.
	w.dns.CNAME["status.acme.com"] = domainCNAMETarget
	if r = w.verify(page, id, owner); r.body["state"] != "pending" || r.body["lastError"] != "txt-missing" {
		t.Fatalf("verify, CNAME only: %d %s", r.code, r.raw)
	}
	delete(w.dns.CNAME, "status.acme.com")
	// A wrong token in the TXT record is not the proof.
	w.dns.TXT[domains.TXTName("status.acme.com")] = []string{"proreta-verify=00000000000000000000000000000000"}
	if r = w.verify(page, id, owner); r.body["state"] != "pending" {
		t.Fatalf("verify, wrong token: %d %s", r.code, r.raw)
	}

	// The TXT record: verified, served, and the first serving domain becomes the primary one.
	w.publish("status.acme.com", a)
	r = w.verify(page, id, owner)
	if r.code != http.StatusAccepted || r.body["state"] != "verified" || r.body["lastError"] != "cname-missing" || r.body["primary"] != true ||
		r.body["verifiedAt"] == nil || r.body["expiresAt"] != nil {
		t.Fatalf("verify, TXT: %d %s", r.code, r.raw)
	}
	// The CNAME: active.
	w.dns.CNAME["status.acme.com"] = domainCNAMETarget
	if r = w.verify(page, id, owner); r.body["state"] != "active" || r.body["lastError"] != nil {
		t.Fatalf("verify, CNAME: %d %s", r.code, r.raw)
	}
	// A DNS error on a serving domain changes nothing and counts no failure.
	w.dns.Fail["status.acme.com"] = true
	if r = w.verify(page, id, owner); r.body["state"] != "active" || r.body["lastError"] != "dns-error" {
		t.Fatalf("verify active, dns error: %d %s", r.code, r.raw)
	}
	delete(w.dns.Fail, "status.acme.com")
	if n := w.count(t, `SELECT check_failures FROM status_page_domains WHERE id = $1`, id); n != 0 {
		t.Fatalf("check_failures after a DNS error: %d", n)
	}
	// Both records gone: a failure is counted but the host serves until the grace period ends.
	delete(w.dns.CNAME, "status.acme.com")
	delete(w.dns.TXT, domains.TXTName("status.acme.com"))
	if r = w.verify(page, id, owner); r.body["state"] != "active" || r.body["lastError"] != "txt-missing" {
		t.Fatalf("verify active, records gone: %d %s", r.code, r.raw)
	}
	if n := w.count(t, `SELECT check_failures FROM status_page_domains WHERE id = $1`, id); n != 1 {
		t.Fatalf("check_failures: %d", n)
	}
	w.publish("status.acme.com", a)
	w.dns.CNAME["status.acme.com"] = domainCNAMETarget

	// A second domain; only a serving one can become primary.
	b := w.add(t, page, "status2.acme.com", owner)
	bid := b.body["id"].(string)
	patch := func(domainID string) response {
		return w.c.do(http.MethodPatch, w.domainsPath(page)+"/"+domainID, `{"primary":true}`, owner)
	}
	if r = patch(bid); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:domain-unavailable" {
		t.Fatalf("primary of a pending domain: %d %s", r.code, r.raw)
	}
	w.publish("status2.acme.com", b)
	if r = w.verify(page, bid, owner); r.body["state"] != "verified" || r.body["primary"] != false {
		t.Fatalf("verify second: %d %s", r.code, r.raw)
	}
	if r = patch(bid); r.code != http.StatusOK || r.body["primary"] != true {
		t.Fatalf("set primary: %d %s", r.code, r.raw)
	}
	list := w.c.do(http.MethodGet, w.domainsPath(page), "", w.hdr("viewer"))
	items, _ := list.body["items"].([]any)
	if list.code != http.StatusOK || len(items) != 2 || items[0].(map[string]any)["host"] != "status2.acme.com" || items[0].(map[string]any)["primary"] != true || items[1].(map[string]any)["primary"] != false {
		t.Fatalf("list: %d %s", list.code, list.raw)
	}
	// The usage count comes from the same rows.
	if u := w.c.do(http.MethodGet, "/api/v1/usage", "", owner); u.body["usage"].(map[string]any)["statusPageDomains"] != float64(2) {
		t.Fatalf("usage: %s", u.raw)
	}

	// Removing the primary hands the role to the oldest serving domain; the host is free again.
	if r = w.c.do(http.MethodDelete, w.domainsPath(page)+"/"+bid, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", r.code, r.raw)
	}
	list = w.c.do(http.MethodGet, w.domainsPath(page), "", owner)
	items, _ = list.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["host"] != "status.acme.com" || items[0].(map[string]any)["primary"] != true {
		t.Fatalf("list after remove: %s", list.raw)
	}
	if r = w.c.do(http.MethodDelete, w.domainsPath(page)+"/"+bid, "", owner); r.code != http.StatusNotFound {
		t.Fatalf("remove again: %d %s", r.code, r.raw)
	}
	if n := w.count(t, `SELECT count(*) FROM status_page_domains WHERE id = $1 AND released_at IS NOT NULL AND NOT is_primary`, bid); n != 1 {
		t.Fatal("the removed domain is not soft-released")
	}

	// Deleting the page releases its domains in the same transaction.
	if r = w.c.do(http.MethodDelete, "/api/v1/status-pages/"+page, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("delete page: %d %s", r.code, r.raw)
	}
	if n := w.count(t, `SELECT count(*) FROM status_page_domains WHERE page_id = $1 AND released_at IS NULL`, page); n != 0 {
		t.Fatalf("%d domains survive their page", n)
	}
	if r = w.c.do(http.MethodGet, w.domainsPath(page), "", owner); r.code != http.StatusNotFound {
		t.Fatalf("list of a deleted page: %d %s", r.code, r.raw)
	}
	if u := w.c.do(http.MethodGet, "/api/v1/usage", "", owner); u.body["usage"].(map[string]any)["statusPageDomains"] != float64(0) {
		t.Fatalf("usage after the page is gone: %s", u.raw)
	}

	// Audit rows, one dot and past tense, written with the change.
	got := w.audits(t, "status_page.domain_%")
	for _, want := range [][]string{
		{"status_page.domain_added", `"host": "status.acme.com"`},
		{"status_page.domain_verified", `"takeover": false`},
		{"status_page.domain_activated", `"host": "status.acme.com"`},
		{"status_page.domain_primary_changed", `"host": "status2.acme.com"`},
		{"status_page.domain_removed", `"by": "user"`},
		{"status_page.domain_removed", `"by": "page"`},
	} {
		if !hasAudit(got, want...) {
			t.Errorf("no audit row %v in %v", want, got)
		}
	}
}

// TestIntegrationStatusPageDomainsTakeover: a stale CNAME never takes a host another organization
// holds; the host moves only when the holder's own token is gone from DNS and the claimant's is there.
func TestIntegrationStatusPageDomainsTakeover(t *testing.T) {
	w := newDomainWorld(t, "domaintakeover", 10)
	pageA := w.sp.byProject["alpha"]
	const host = "status.acme.com"

	reg := w.c.do(http.MethodPost, "/api/v1/auth/register", registerBody("domain-b@example.com", ownerPassword), map[string]string{"X-Forwarded-For": "198.51.100.77"})
	other, csrf := session(t, reg)
	other = withCSRF(other, csrf)
	w.limit(t, 10)
	rp := w.c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"B"}`, other)
	if rp.code != http.StatusCreated {
		t.Fatalf("page of B: %d %s", rp.code, rp.raw)
	}
	pageB := rp.body["id"].(string)

	a := w.add(t, pageA, host, w.hdrO)
	w.publish(host, a)
	w.dns.CNAME[host] = domainCNAMETarget
	if r := w.verify(pageA, a.body["id"].(string), w.hdrO); r.body["state"] != "active" {
		t.Fatalf("A verify: %d %s", r.code, r.raw)
	}

	// B may add the host (it is not refused up front) but cannot verify it with A's token and a stale CNAME.
	b := w.add(t, pageB, host, other)
	if b.code != http.StatusCreated {
		t.Fatalf("B add: %d %s", b.code, b.raw)
	}
	bid := b.body["id"].(string)
	r := w.verify(pageB, bid, other)
	if r.code != http.StatusAccepted || r.body["state"] != "pending" || r.body["lastError"] != "txt-missing" {
		t.Fatalf("B verify with a stale CNAME: %d %s", r.code, r.raw)
	}
	holders := func() int {
		return w.count(t, `SELECT count(*) FROM status_page_domains WHERE host = $1 AND state IN ('verified','active') AND released_at IS NULL AND page_id = $2`, host, pageA)
	}
	if holders() != 1 {
		t.Fatal("A lost the host to a stale CNAME")
	}

	// Both tokens in DNS: the DNS owner authorised both, A keeps it, B gets the one generic answer.
	w.publish(host, a, b)
	r = w.verify(pageB, bid, other)
	if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:domain-unavailable" || strings.Contains(r.raw, "acme") {
		t.Fatalf("B verify with both tokens: %d %s", r.code, r.raw)
	}
	if holders() != 1 {
		t.Fatal("A lost the host while its token is in DNS")
	}

	// Only B's token: the host moves, A is told through its audit log.
	w.publish(host, b)
	r = w.verify(pageB, bid, other)
	if r.code != http.StatusAccepted || r.body["state"] != "active" || r.body["primary"] != true {
		t.Fatalf("B takeover: %d %s", r.code, r.raw)
	}
	if holders() != 0 {
		t.Fatal("A still holds the host")
	}
	list := w.c.do(http.MethodGet, w.domainsPath(pageA), "", w.hdrO)
	if items, _ := list.body["items"].([]any); list.code != http.StatusOK || len(items) != 0 {
		t.Fatalf("A's list after the takeover: %s", list.raw)
	}
	got := w.audits(t, "status_page.domain_%")
	if !hasAudit(got, "status_page.domain_removed", `"by": "takeover"`) || !hasAudit(got, "status_page.domain_verified", `"takeover": true`) {
		t.Fatalf("takeover audit rows: %v", got)
	}
}

// TestIntegrationStatusPageDomainsLimits: the plan limit and the cap of three per page hold under
// concurrent adds, and an organization adds at most ten a day.
func TestIntegrationStatusPageDomainsLimits(t *testing.T) {
	w := newDomainWorld(t, "domainlimits", 2)
	owner := w.hdrO

	// Concurrent adds on three pages never exceed the plan limit of two.
	var wg sync.WaitGroup
	var created, refused atomic.Int32
	for i, key := range []string{"def", "alpha", "beta", "def", "alpha", "beta"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := w.add(t, w.sp.byProject[key], fmt.Sprintf("c%d.acme.com", i), owner)
			switch {
			case r.code == http.StatusCreated:
				created.Add(1)
			case r.code == http.StatusConflict && r.body["type"] == "urn:proreta:problem:plan-limit-reached" && r.body["limit"] == "statusPageDomains" && r.body["max"] == float64(2):
				refused.Add(1)
			default:
				t.Errorf("concurrent add: %d %s", r.code, r.raw)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 2 || refused.Load() != 4 {
		t.Fatalf("created %d, refused %d", created.Load(), refused.Load())
	}
	if n := w.count(t, `SELECT count(*) FROM status_page_domains WHERE released_at IS NULL`); n != 2 {
		t.Fatalf("%d domains stored", n)
	}

	// Three per page.
	w.limit(t, 100)
	page := w.sp.byProject["gamma"]
	for i := range 3 {
		if r := w.add(t, page, fmt.Sprintf("g%d.acme.com", i), owner); r.code != http.StatusCreated {
			t.Fatalf("add %d: %d %s", i, r.code, r.raw)
		}
	}
	if r := w.add(t, page, "g3.acme.com", owner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached" ||
		r.body["limit"] != "statusPageDomains" || r.body["max"] != float64(3) {
		t.Fatalf("fourth domain: %d %s", r.code, r.raw)
	}

	// Ten a day, released rows included: five released ones on top of the five above make ten.
	if n := w.count(t, `SELECT count(*) FROM status_page_domains`); n != 5 {
		t.Fatalf("%d rows before the day's limit", n)
	}
	for i := range 5 {
		w.seed(t, w.sp.byProject["alpha"], fmt.Sprintf("old%d.acme.com", i), "pending", true)
	}
	r := w.add(t, w.sp.byProject["alpha"], "late.acme.com", owner)
	if r.code != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("eleventh domain of the day: %d %s", r.code, r.raw)
	}
	w.clk.Advance(25 * time.Hour)
	if r = w.add(t, w.sp.byProject["alpha"], "late.acme.com", owner); r.code != http.StatusCreated {
		t.Fatalf("a day later: %d %s", r.code, r.raw)
	}
}

// TestIntegrationStatusPageDomainsVerifyRateLimit: 6 verifications a minute and 30 an hour per organization.
func TestIntegrationStatusPageDomainsVerifyRateLimit(t *testing.T) {
	w := newDomainWorld(t, "domainrate", 10)
	page := w.sp.byProject["alpha"]
	id := w.add(t, page, "rate.acme.com", w.hdrO).body["id"].(string)
	w.spread = false

	call := func() response { return w.verify(page, id, w.hdrO) }
	// A caller who may not verify (no role on that page) does not spend the budget.
	foreign := w.seed(t, w.sp.byProject["beta"], "foreign.acme.com", "pending", false)
	for range 10 {
		if r := w.verify(w.sp.byProject["beta"], foreign, w.hdr("aOwner")); r.code != http.StatusNotFound {
			t.Fatalf("aOwner verify on beta: %d %s", r.code, r.raw)
		}
	}
	for i := range 6 {
		if r := call(); r.code != http.StatusAccepted {
			t.Fatalf("verify %d: %d %s", i, r.code, r.raw)
		}
	}
	r := call()
	if r.code != http.StatusTooManyRequests || r.body["type"] != "urn:proreta:problem:rate-limited" || r.header.Get("Retry-After") == "" {
		t.Fatalf("seventh verify: %d %s", r.code, r.raw)
	}
	// Four more windows of six use up the 30 of the hour; the thirty-first is refused although its minute is fresh.
	for batch := range 4 {
		w.clk.Advance(61 * time.Second)
		for i := range 6 {
			if r := call(); r.code != http.StatusAccepted {
				t.Fatalf("batch %d verify %d: %d %s", batch, i, r.code, r.raw)
			}
		}
	}
	w.clk.Advance(61 * time.Second)
	if r := call(); r.code != http.StatusTooManyRequests {
		t.Fatalf("thirty-first verify of the hour: %d %s", r.code, r.raw)
	}
	w.clk.Advance(time.Hour)
	if r := call(); r.code != http.StatusAccepted {
		t.Fatalf("after the hour: %d %s", r.code, r.raw)
	}
}

// TestIntegrationStatusPageDomainsRoles runs the five operations as every role at both levels against
// every project, with ids of projects the person cannot see and of pages that do not exist.
func TestIntegrationStatusPageDomainsRoles(t *testing.T) {
	w := newDomainWorld(t, "domainroles", 500)
	var seq atomic.Int64
	admin := w.admin(t)
	reset := func(response) {
		pgtest.MustExec(t, admin, `DELETE FROM status_page_domains`)
		w.clk.Advance(2 * time.Hour)
	}
	// seed stores a verified domain on the page of the project, as the owner would have.
	seed := func(p roleProject) (page, id string) {
		if p.key == "unknown" {
			return p.id, p.id
		}
		page = w.sp.byProject[p.key]
		return page, w.seed(t, page, fmt.Sprintf("s%d.acme.com", seq.Add(1)), "verified", false)
	}
	op := func(name string, min authz.Role, ok int, method string, withID bool, suffix, body string) roleOp {
		return roleOp{name, min, http.StatusNotFound, ok, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			page, id := seed(p)
			path := "/api/v1/status-pages/" + page + "/domains"
			if withID {
				path += "/" + id + suffix
			}
			if method == http.MethodPost && !withID {
				body = fmt.Sprintf(`{"host":"n%d.acme.com"}`, seq.Add(1))
			}
			return roleCall{method, path, body}, reset
		}}
	}
	ops := []roleOp{
		op("list domains", authz.Viewer, http.StatusOK, http.MethodGet, false, "", ""),
		op("add domain", authz.Maintainer, http.StatusCreated, http.MethodPost, false, "", ""),
		op("verify domain", authz.Maintainer, http.StatusAccepted, http.MethodPost, true, "/verify", ""),
		op("set primary domain", authz.Maintainer, http.StatusOK, http.MethodPatch, true, "", `{"primary":true}`),
		op("remove domain", authz.Maintainer, http.StatusNoContent, http.MethodDelete, true, "", ""),
	}
	unknownID := "0192b9c4-7c5e-7000-8000-0000000000ee"
	projects := []roleProject{
		{"def", w.def, w.mDef, w.hbDef, w.chDef},
		{"alpha", w.alpha, w.mAlpha, w.hbAlpha, w.chAlpha},
		{"beta", w.beta, w.mBeta, w.hbBeta, w.chBeta},
		{"gamma", w.gamma, w.mGam, w.hbGamma, w.chGamma},
		{"unknown", unknownID, unknownID, unknownID, unknownID},
	}
	for _, a := range roleActors {
		hdr := w.hdr(a.name)
		for _, p := range projects {
			for _, o := range ops {
				call, cleanup := o.call(w.projectWorld, p)
				r := w.c.do(call.method, call.path, call.body, hdr)
				cleanup(r)
				if want := wantStatus(a, o, p); r.code != want {
					t.Errorf("%s: %s in %s: %d %s, want %d", a.name, o.name, p.key, r.code, r.raw, want)
				}
			}
		}
	}

	// The organization-level page: organization owners and maintainers manage, other organization
	// roles read, people with project roles only do neither.
	for _, tc := range []struct {
		who          string
		list, add    int
		verifyDelete int
	}{
		{"owner", 200, 201, 204}, {"maintainer", 200, 201, 204}, {"member", 200, 403, 403}, {"viewer", 200, 403, 403},
		{"aOwner", 404, 404, 404}, {"aViewer", 404, 403, 403}, {"none", 403, 403, 403},
	} {
		hdr := w.hdr(tc.who)
		reset(response{})
		page := w.sp.org
		id := w.seed(t, page, fmt.Sprintf("s%d.acme.com", seq.Add(1)), "verified", false)
		base := "/api/v1/status-pages/" + page + "/domains"
		if r := w.c.do(http.MethodGet, base, "", hdr); r.code != tc.list {
			t.Errorf("%s list org page: %d %s", tc.who, r.code, r.raw)
		}
		if r := w.c.do(http.MethodPost, base, fmt.Sprintf(`{"host":"org%d.acme.com"}`, seq.Add(1)), hdr); r.code != tc.add {
			t.Errorf("%s add to org page: %d %s", tc.who, r.code, r.raw)
		}
		if r := w.c.do(http.MethodDelete, base+"/"+id, "", hdr); r.code != tc.verifyDelete {
			t.Errorf("%s remove from org page: %d %s", tc.who, r.code, r.raw)
		}
	}
}

// TestIntegrationStatusPageDomainsFlagOff: with PRORETA_STATUS_PAGE_CUSTOM_DOMAINS off every domain
// operation answers 404 custom-domains-disabled and nothing is written.
func TestIntegrationStatusPageDomainsFlagOff(t *testing.T) {
	w := newProjectWorld(t, "domainflagoff")
	sp := w.addStatusPages(t)
	owner := w.hdr("owner")
	page := sp.byProject["alpha"]
	base := "/api/v1/status-pages/" + page + "/domains"
	id := "0192b9c4-7c5e-7000-8000-0000000000aa"
	for _, c := range []roleCall{
		{http.MethodGet, base, ""}, {http.MethodPost, base, `{"host":"status.acme.com"}`}, {http.MethodPost, base + "/" + id + "/verify", ""},
		{http.MethodPatch, base + "/" + id, `{"primary":true}`}, {http.MethodDelete, base + "/" + id, ""},
	} {
		r := w.c.do(c.method, c.path, c.body, owner)
		if r.code != http.StatusNotFound || r.body["type"] != "urn:proreta:problem:custom-domains-disabled" {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.code, r.raw)
		}
	}
	var n int
	if err := w.env.AdminConn(t).QueryRow(context.Background(), `SELECT count(*) FROM status_page_domains`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows with the switch off: %d %v", n, err)
	}
}
