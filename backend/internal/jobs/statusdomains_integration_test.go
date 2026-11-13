package jobs_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	domTokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	domTokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	domTarget = "customers.proreta.example.net"
)

// countingResolver counts the TXT lookups per name on top of the fake.
type countingResolver struct {
	*domains.Fake
	mu  sync.Mutex
	txt map[string]int
}

func (c *countingResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	c.mu.Lock()
	c.txt[name]++
	c.mu.Unlock()
	return c.Fake.LookupTXT(ctx, name)
}

type domEnv struct {
	*env
	pageA, pageB string
	fake         *countingResolver
	reg          *prometheus.Registry
	task         jobs.Task
	h            *jobs.Housekeeper
}

func newDomEnv(t *testing.T, name string, enabled bool) *domEnv {
	e := &domEnv{env: newEnv(t, name), pageA: uuid.NewString(), pageB: uuid.NewString(),
		fake: &countingResolver{Fake: &domains.Fake{TXT: map[string][]string{}, CNAME: map[string]string{}, Fail: map[string]bool{}}, txt: map[string]int{}}}
	pgtest.MustExec(t, e.admin,
		`INSERT INTO status_pages (id, tenant_id, slug, title, published) VALUES
			('`+e.pageA+`', '`+tenantA+`', 'acme', 'Acme', true), ('`+e.pageB+`', '`+tenantB+`', 'beta', 'Beta', true)`)
	e.reg = prometheus.NewRegistry()
	m := jobs.NewMetrics(e.reg)
	e.task = jobs.CheckStatusPageDomains(jobs.StatusDomainsConfig{Enabled: enabled, CNAMETarget: domTarget, Resolver: e.fake, Log: slog.New(slog.NewJSONHandler(e, nil))}, m)
	e.h = jobs.NewHousekeeper(e.Pool(t, e.WorkerURL, 4), []jobs.Task{e.task}, m, slog.New(slog.NewJSONHandler(e, nil)), nil)
	return e
}

// add inserts a domain through the admin connection; set is extra assignments ("created_at = ...").
func (e *domEnv) add(tenant, host, state string, set ...string) string {
	e.t.Helper()
	id, page, token := uuid.NewString(), e.pageA, domTokenA
	if tenant == tenantB {
		page, token = e.pageB, domTokenB
	}
	pgtest.MustExec(e.t, e.admin, fmt.Sprintf(`INSERT INTO status_page_domains (id, tenant_id, page_id, host, state, token) VALUES ('%s', '%s', '%s', '%s', '%s', '%s')`, id, tenant, page, host, state, token))
	for _, s := range set {
		pgtest.MustExec(e.t, e.admin, `UPDATE status_page_domains SET `+s+` WHERE id = '`+id+`'`)
	}
	return id
}

// proof publishes the TXT proof of host's token (and the CNAME to the target when cname).
func (e *domEnv) proof(host, token string, cname bool) {
	e.fake.TXT[domains.TXTName(host)] = []string{domains.TXTValue(token)}
	if cname {
		e.fake.CNAME[host] = domTarget
	}
}

func (e *domEnv) run() string { e.t.Helper(); return e.h.RunTask(e.ctx, e.task) }

type domRow struct {
	state, lastError string
	failures         int
	checked          bool
	released         bool
}

func (e *domEnv) row(id string) domRow {
	e.t.Helper()
	var r domRow
	var le *string
	if err := e.admin.QueryRow(e.ctx, `SELECT state, last_error, check_failures, last_checked_at IS NOT NULL, released_at IS NOT NULL FROM status_page_domains WHERE id = $1`, id).
		Scan(&r.state, &le, &r.failures, &r.checked, &r.released); err != nil {
		e.t.Fatal(err)
	}
	if le != nil {
		r.lastError = *le
	}
	return r
}

func (e *domEnv) audits(action string) int {
	e.t.Helper()
	var n int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestIntegrationCheckStatusPageDomains(t *testing.T) {
	e := newDomEnv(t, "jobsdomains", true)

	t.Run("verifies and activates, audits in the tenant, counts and gauges", func(t *testing.T) {
		both := e.add(tenantA, "both.acme.com", "pending")
		txtOnly := e.add(tenantB, "txt.beta.com", "pending")
		none := e.add(tenantA, "none.acme.com", "pending")
		e.proof("both.acme.com", domTokenA, true)
		e.proof("txt.beta.com", domTokenB, false)
		if got := e.run(); got != "ok" {
			t.Fatalf("run: %s", got)
		}
		if r := e.row(both); r.state != "active" || r.lastError != "" || !r.checked {
			t.Fatalf("both: %+v", r)
		}
		if r := e.row(txtOnly); r.state != "verified" || r.lastError != "cname-missing" {
			t.Fatalf("txt only: %+v", r)
		}
		if r := e.row(none); r.state != "pending" || r.lastError != "txt-missing" {
			t.Fatalf("none: %+v", r)
		}
		if e.audits("status_page.domain_verified") != 2 || e.audits("status_page.domain_activated") != 1 {
			t.Fatalf("audit rows: verified %d activated %d", e.audits("status_page.domain_verified"), e.audits("status_page.domain_activated"))
		}
		var tenant, pageID, host string
		if err := e.admin.QueryRow(e.ctx, `SELECT tenant_id::text, metadata->>'pageId', metadata->>'host' FROM audit_log WHERE action = 'status_page.domain_activated' AND entity_id = $1`, both).Scan(&tenant, &pageID, &host); err != nil ||
			tenant != tenantA || pageID != e.pageA || host != "both.acme.com" {
			t.Fatalf("audit row: %v %s %s %s", err, tenant, pageID, host)
		}
		for res, want := range map[string]float64{"ok": 1, "cname-missing": 1, "txt-missing": 1, "dns-error": 0} {
			if got := metric(t, e.reg, "proreta_status_page_domains_checks_total", res); got != want {
				t.Errorf("checks_total{%s} = %v, want %v", res, got, want)
			}
		}
		for state, want := range map[string]float64{"active": 1, "verified": 1, "pending": 1, "failed": 0} {
			if got := metric(t, e.reg, "proreta_status_page_domains", state); got != want {
				t.Errorf("domains{%s} = %v, want %v", state, got, want)
			}
		}
		// Checked just now: not due again.
		before := len(e.fake.txt)
		e.run()
		if e.fake.txt["_proreta-status.both.acme.com"] != 1 || len(e.fake.txt) != before {
			t.Fatalf("a checked domain was looked up again: %v", e.fake.txt)
		}
	})

	t.Run("cadence: pending every 10 minutes, young or old", func(t *testing.T) {
		young := e.add(tenantA, "young.acme.com", "pending", "last_checked_at = now() - interval '5 minutes'")
		youngDue := e.add(tenantA, "youngdue.acme.com", "pending", "last_checked_at = now() - interval '11 minutes'")
		oldPending := e.add(tenantA, "oldpending.acme.com", "pending", "created_at = now() - interval '3 days', last_checked_at = now() - interval '30 minutes'")
		active := e.add(tenantA, "active.acme.com", "active", "last_checked_at = now() - interval '5 hours'")
		activeDue := e.add(tenantA, "activedue.acme.com", "active", "last_checked_at = now() - interval '7 hours'")
		e.proof("activedue.acme.com", domTokenA, true)
		e.run()
		var checkedAt = func(id string) bool {
			var fresh bool
			if err := e.admin.QueryRow(e.ctx, `SELECT last_checked_at > now() - interval '1 minute' FROM status_page_domains WHERE id = $1`, id).Scan(&fresh); err != nil {
				t.Fatal(err)
			}
			return fresh
		}
		for id, want := range map[string]bool{young: false, youngDue: true, oldPending: false, active: false, activeDue: true} {
			if got := checkedAt(id); got != want {
				t.Errorf("domain %s checked now = %v, want %v", id, got, want)
			}
		}
	})

	t.Run("a DNS error is unknown and never fails a domain", func(t *testing.T) {
		id := e.add(tenantA, "flaky.acme.com", "verified", "last_proof_at = now() - interval '30 days', check_failures = 5, last_checked_at = now() - interval '7 hours'")
		e.fake.Fail["_proreta-status.flaky.acme.com"] = true
		e.run()
		if r := e.row(id); r.state != "verified" || r.failures != 5 || r.lastError != "dns-error" {
			t.Fatalf("after a DNS error: %+v", r)
		}
		if e.audits("status_page.domain_failed") != 0 {
			t.Fatal("a failed audit row after a DNS error")
		}
		if got := metric(t, e.reg, "proreta_status_page_domains_checks_total", "dns-error"); got != 1 {
			t.Fatalf("dns-error count %v", got)
		}
	})

	t.Run("a timeout is unknown too", func(t *testing.T) {
		slow := newDomEnv(t, "jobsdomainsslow", true)
		slow.fake.Delay = 30 * time.Second
		id := slow.add(tenantA, "slow.acme.com", "verified", "last_proof_at = now() - interval '30 days', check_failures = 5")
		start := time.Now()
		slow.run()
		if time.Since(start) > 20*time.Second {
			t.Fatalf("a run with an unanswering resolver took %v", time.Since(start))
		}
		if r := slow.row(id); r.state != "verified" || r.failures != 5 || r.lastError != "dns-error" {
			t.Fatalf("after a timeout: %+v", r)
		}
	})

	t.Run("failure grace: three checks and seven days, then failed with an audit row", func(t *testing.T) {
		recent := e.add(tenantA, "recent.acme.com", "verified", "last_proof_at = now() - interval '2 days'")
		lost := e.add(tenantB, "lost.beta.com", "verified", "last_proof_at = now() - interval '8 days'")
		due := func() {
			pgtest.MustExec(t, e.admin, `UPDATE status_page_domains SET last_checked_at = now() - interval '7 hours' WHERE host IN ('recent.acme.com', 'lost.beta.com')`)
		}
		for i := 1; i <= 2; i++ {
			due()
			e.run()
			if r := e.row(lost); r.state != "verified" || r.failures != i {
				t.Fatalf("check %d: %+v (two checks are not enough)", i, r)
			}
		}
		due()
		e.run()
		if r := e.row(lost); r.state != "failed" || r.failures != 3 {
			t.Fatalf("third check: %+v", r)
		}
		if r := e.row(recent); r.state != "verified" || r.failures != 3 {
			t.Fatalf("seven days not over: %+v", r)
		}
		if e.audits("status_page.domain_failed") != 1 {
			t.Fatalf("failed audit rows: %d", e.audits("status_page.domain_failed"))
		}
		var tenant string
		if err := e.admin.QueryRow(e.ctx, `SELECT tenant_id::text FROM audit_log WHERE action = 'status_page.domain_failed' AND entity_id = $1`, lost).Scan(&tenant); err != nil || tenant != tenantB {
			t.Fatalf("failed audit row tenant: %v %s", err, tenant)
		}
		if got := metric(t, e.reg, "proreta_status_page_domains", "failed"); got != 1 {
			t.Fatalf("failed gauge %v", got)
		}
		// The proof coming back recovers it to verified.
		e.proof("lost.beta.com", domTokenB, false)
		due()
		pgtest.MustExec(t, e.admin, `UPDATE status_page_domains SET last_checked_at = now() - interval '7 hours' WHERE id = '`+lost+`'`)
		e.run()
		if r := e.row(lost); r.state != "verified" || r.failures != 0 {
			t.Fatalf("recovery: %+v", r)
		}
	})

	t.Run("an old pending domain expires at seven days", func(t *testing.T) {
		old := e.add(tenantA, "old.acme.com", "pending", "created_at = now() - interval '8 days'")
		fresh := e.add(tenantA, "fresh.acme.com", "pending", "created_at = now() - interval '6 days'")
		e.run()
		if r := e.row(old); !r.released {
			t.Fatalf("expired: %+v", r)
		}
		if r := e.row(fresh); r.released {
			t.Fatalf("not yet expired: %+v", r)
		}
		if e.audits("status_page.domain_removed") != 1 {
			t.Fatalf("removed audit rows: %d", e.audits("status_page.domain_removed"))
		}
	})

	t.Run("a host another organization holds stays pending and does not stop the batch", func(t *testing.T) {
		e.add(tenantB, "contested.com", "active")
		claim := e.add(tenantA, "contested.com", "pending")
		other := e.add(tenantA, "fine.acme.com", "pending")
		e.proof("contested.com", domTokenA, false)
		e.proof("fine.acme.com", domTokenA, true)
		if got := e.run(); got != "ok" {
			t.Fatalf("run: %s", got)
		}
		if r := e.row(claim); r.state != "pending" || r.lastError != "txt-mismatch" || !r.checked {
			t.Fatalf("claim: %+v", r)
		}
		if r := e.row(other); r.state != "active" {
			t.Fatalf("the other domain of the batch: %+v", r)
		}
	})
}

func TestIntegrationCheckStatusPageDomainsOffIsNoop(t *testing.T) {
	e := newDomEnv(t, "jobsdomainsoff", false)
	id := e.add(tenantA, "off.acme.com", "pending")
	e.proof("off.acme.com", domTokenA, true)
	if got := e.run(); got != "ok" {
		t.Fatalf("run: %s", got)
	}
	if r := e.row(id); r.state != "pending" || r.checked {
		t.Fatalf("with the flag off the domain was checked: %+v", r)
	}
	if len(e.fake.txt) != 0 {
		t.Fatalf("DNS was asked with the flag off: %v", e.fake.txt)
	}
}

// Two jobs replicas at once (the task body is called directly, past the advisory lock): every
// domain is looked up exactly once, because the batch transaction holds the rows it checks.
func TestIntegrationCheckStatusPageDomainsConcurrentRuns(t *testing.T) {
	e := newDomEnv(t, "jobsdomainsconc", true)
	e.fake.Delay = 200 * time.Millisecond
	const n = 60
	for i := range n {
		host := fmt.Sprintf("d%02d.acme.com", i)
		e.add(tenantA, host, "pending")
		e.proof(host, domTokenA, i%2 == 0)
	}
	pool := e.Pool(t, e.WorkerURL, 6)
	var wg sync.WaitGroup
	totals := make([]int, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := e.task.RunOutsideTx(e.ctx, pool, time.Now())
			if err != nil {
				t.Errorf("replica %d: %v", i, err)
			}
			totals[i] = c
		}()
	}
	wg.Wait()
	if totals[0]+totals[1] != n || totals[0] == 0 || totals[1] == 0 {
		t.Fatalf("checked %v, want %d split between both replicas", totals, n)
	}
	for host, k := range e.fake.txt {
		if k != 1 {
			t.Errorf("%s looked up %d times", host, k)
		}
	}
	var left int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*) FROM status_page_domains WHERE last_checked_at IS NULL`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("unchecked domains left: %d %v", left, err)
	}
}
