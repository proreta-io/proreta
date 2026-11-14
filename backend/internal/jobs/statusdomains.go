package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

const lockStatusDomains int64 = 0x7072_6f72_6574_000e

// Limits of one check_status_page_domains run. The worker role's idle_in_transaction_session_timeout
// is 30 s and both the task's lock transaction and a batch's transaction sit idle while DNS
// answers, so the whole run and one batch's lookups stay well below it.
const (
	// StatusDomainsEvery is how often the task looks for due domains; the cadences themselves
	// (10 minutes pending, hourly, 6 hours) are in status_page_domains_due.
	StatusDomainsEvery = time.Minute
	// StatusDomainsBatch is how many domains one transaction locks and checks, all at once.
	StatusDomainsBatch = 25
	// StatusDomainsMaxPerRun caps the domains one run checks; the rest follows at the next run.
	StatusDomainsMaxPerRun = 500
	// StatusDomainsBudget stops starting batches after this long.
	StatusDomainsBudget = 15 * time.Second
	// statusDomainsBatchLookups bounds the lookups of one batch.
	statusDomainsBatchLookups = 10 * time.Second
)

// Check results, the label of proreta_status_page_domains_checks_total and the last_error values.
const (
	checkOK          = "ok"
	checkTXTMissing  = "txt-missing"
	checkCNAMEMiss   = "cname-missing"
	checkTXTMismatch = "txt-mismatch"
	checkDNSError    = "dns-error"
)

var checkResults = []string{checkOK, checkTXTMissing, checkCNAMEMiss, checkTXTMismatch, checkDNSError}

var domainStates = []string{"pending", "verified", "active", "failed"}

// StatusDomainsConfig configures CheckStatusPageDomains.
type StatusDomainsConfig struct {
	// Enabled is PRORETA_STATUS_PAGE_CUSTOM_DOMAINS; false makes the task do nothing.
	Enabled bool
	// CNAMETarget is where a host's CNAME must end (PRORETA_STATUSPAGE_CNAME_TARGET).
	CNAMETarget string
	Resolver    domains.DomainResolver
	Log         *slog.Logger
}

// CheckStatusPageDomains returns the housekeeping task that verifies the DNS of the status page
// domains that are due. Every batch is one transaction: status_page_domains_due locks its rows
// (FOR UPDATE SKIP LOCKED), the lookups run, status_page_domain_record_check applies each result
// and the audit rows of the state changes are written, all committed together, so two replicas
// never check the same row (the advisory lock keeps the task a singleton as well).
func CheckStatusPageDomains(cfg StatusDomainsConfig, m *Metrics) Task {
	return Task{Name: "check_status_page_domains", Every: StatusDomainsEvery, LockKey: lockStatusDomains,
		RunOutsideTx: func(ctx context.Context, pool *db.Pool, _ time.Time) (int, error) {
			if !cfg.Enabled {
				return 0, nil
			}
			c := domainChecker{cfg: cfg, m: m}
			n, err := c.run(ctx, pool)
			if gerr := c.gauge(ctx, pool); err == nil {
				err = gerr
			}
			return n, err
		}}
}

type domainChecker struct {
	cfg StatusDomainsConfig
	m   *Metrics
}

type dueDomain struct {
	tenant, id, page uuid.UUID
	host, token      string
	state            string
}

type checked struct {
	dueDomain
	facts domains.Facts
}

func (c domainChecker) run(ctx context.Context, pool *db.Pool) (int, error) {
	start, total := time.Now(), 0
	for total < StatusDomainsMaxPerRun && time.Since(start) < StatusDomainsBudget {
		n, err := c.batch(ctx, pool)
		total += n
		if err != nil {
			return total, err
		}
		if n < StatusDomainsBatch {
			break
		}
	}
	return total, nil
}

// batch checks one batch of due domains in one transaction and returns how many it checked.
func (c domainChecker) batch(ctx context.Context, pool *db.Pool) (int, error) {
	n := 0
	results := map[string]int{}
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT tenant_id, id, page_id, host, token, state FROM status_page_domains_due($1)`, StatusDomainsBatch)
		if err != nil {
			return err
		}
		due, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (dueDomain, error) {
			var d dueDomain
			return d, r.Scan(&d.tenant, &d.id, &d.page, &d.host, &d.token, &d.state)
		})
		if err != nil {
			return err
		}
		n = len(due)
		for _, d := range c.probe(ctx, due) {
			res, err := c.record(ctx, tx, d)
			if err != nil {
				return fmt.Errorf("record check of %s: %w", d.host, err)
			}
			results[res]++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Counted after the commit, so a rolled back batch does not count.
	for res, k := range results {
		c.m.domainChecks.WithLabelValues(res).Add(float64(k))
	}
	return n, nil
}

// probe looks up every domain of the batch at once. A lookup that ends with the deadline is an
// unknown result, never a missing record.
func (c domainChecker) probe(ctx context.Context, due []dueDomain) []checked {
	ctx, cancel := context.WithTimeout(ctx, statusDomainsBatchLookups)
	defer cancel()
	out := make([]checked, len(due))
	var wg sync.WaitGroup
	for i, d := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = checked{d, domains.Probe(ctx, c.cfg.Resolver, d.host, d.token, c.cfg.CNAMETarget)}
		}()
	}
	wg.Wait()
	return out
}

// lastError is what is stored in last_error and counted as the check's result.
func lastError(state string, f domains.Facts) string {
	switch {
	case f.TXT == domains.Unknown || f.CNAME == domains.Unknown:
		return checkDNSError
	case f.TXT == domains.Present && f.CNAME == domains.Present:
		return checkOK
	case f.TXT == domains.Present:
		return checkCNAMEMiss
	case f.CNAME == domains.Present && state != string(domains.Pending):
		return checkOK // the CNAME alone keeps a served host alive
	default:
		return checkTXTMissing
	}
}

const uniqueHost = "status_page_domains_host_idx"

// record applies one check inside the batch transaction and returns its result label.
func (c domainChecker) record(ctx context.Context, tx pgx.Tx, d checked) (string, error) {
	res := lastError(d.state, d.facts)
	stored := &res
	if res == checkOK {
		stored = nil
	}
	state, events, err := recordCheck(ctx, tx, d, d.facts, stored)
	if db.IsUniqueViolation(err, uniqueHost) {
		// Another organization holds the host: only the api can settle a takeover (the user's
		// "verify"). The claim stays pending, and a pending claim that never wins expires.
		res = checkTXTMismatch
		state, events, err = recordCheck(ctx, tx, d, domains.Facts{TXT: domains.Missing, CNAME: domains.Missing}, &res)
	}
	if err != nil {
		return "", err
	}
	for _, e := range events {
		if err := c.audit(ctx, tx, d, state, e); err != nil {
			return "", err
		}
	}
	return res, nil
}

// recordCheck calls the definer under a savepoint, so a unique violation does not abort the batch.
func recordCheck(ctx context.Context, tx pgx.Tx, d checked, f domains.Facts, lastErr *string) (state string, events []string, err error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	err = sp.QueryRow(ctx, `SELECT state, events FROM status_page_domain_record_check($1, $2, $3, $4, $5)`,
		d.tenant, d.id, proofText(f.TXT), proofText(f.CNAME), lastErr).Scan(&state, &events)
	if err != nil {
		_ = sp.Rollback(ctx)
		return "", nil, err
	}
	return state, events, sp.Commit(ctx)
}

func proofText(p domains.Proof) string {
	switch p {
	case domains.Present:
		return "present"
	case domains.Missing:
		return "missing"
	}
	return "unknown"
}

// audit names (design: status_page.domain_*), one per event that has one. A recovered domain
// has no audit name; it is back to verified and the state says so.
var domainAudit = map[string]string{
	string(domains.EventVerified):  "status_page.domain_verified",
	string(domains.EventActivated): "status_page.domain_activated",
	string(domains.EventFailed):    "status_page.domain_failed",
	string(domains.EventExpired):   "status_page.domain_removed",
}

// audit writes the audit row of an event in the domain's tenant, inside the batch transaction. The
// tenant setting is switched on for the insert and cleared again, since the next domain is another
// tenant's and record_check refuses a mismatching setting.
func (c domainChecker) audit(ctx context.Context, tx pgx.Tx, d checked, state, event string) error {
	action, ok := domainAudit[event]
	if !ok {
		return nil
	}
	meta := map[string]any{"pageId": d.page.String(), "host": d.host}
	switch event {
	case string(domains.EventVerified):
		meta["takeover"] = false
	case string(domains.EventFailed):
		meta["reason"] = "proof-lost"
	case string(domains.EventExpired):
		meta["by"] = "expired"
	}
	if err := db.SetTenant(ctx, tx, d.tenant); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{TenantID: d.tenant, Action: action, EntityType: "status_page_domain", EntityID: d.id.String(), Metadata: meta}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', '', true)`); err != nil {
		return err
	}
	if event == string(domains.EventFailed) && c.cfg.Log != nil {
		c.cfg.Log.WarnContext(ctx, "status page domain failed: its DNS proof is gone", "domain_id", d.id, "state", state)
	}
	return nil
}

// gauge refreshes proreta_status_page_domains{state}.
func (c domainChecker) gauge(ctx context.Context, pool *db.Pool) error {
	return db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT state, n FROM status_page_domain_counts()`)
		if err != nil {
			return err
		}
		counts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			State string
			N     int64
		}])
		if err != nil {
			return err
		}
		for _, k := range counts {
			c.m.domains.WithLabelValues(k.State).Set(float64(k.N))
		}
		return nil
	})
}

func newDomainMetrics() (*prometheus.GaugeVec, *prometheus.CounterVec) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proreta_status_page_domains", Help: "Status page domains by state (pending, verified, active, failed), all tenants, released ones excluded.",
	}, []string{"state"})
	cv := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proreta_status_page_domains_checks_total", Help: "Status page domain DNS checks by result (ok, txt-missing, cname-missing, txt-mismatch, dns-error).",
	}, []string{"result"})
	for _, s := range domainStates {
		g.WithLabelValues(s)
	}
	for _, r := range checkResults {
		cv.WithLabelValues(r)
	}
	return g, cv
}
