package statuspages

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
	"github.com/proreta-io/proreta/backend/internal/statuspages/statuspagesdb"
)

// Audit actions of the custom domains of a page.
const (
	ActionDomainAdded          = "status_page.domain_added"
	ActionDomainVerified       = "status_page.domain_verified"
	ActionDomainActivated      = "status_page.domain_activated"
	ActionDomainFailed         = "status_page.domain_failed"
	ActionDomainRemoved        = "status_page.domain_removed"
	ActionDomainPrimaryChanged = "status_page.domain_primary_changed"
)

// Limits of the custom domains of a page and of an organization.
const (
	// MaxDomainsPerPage is the hard cap of domains on one page.
	MaxDomainsPerPage = 3
	// MaxDomainsAddedPerDay is how many domains an organization may add in 24 hours.
	MaxDomainsAddedPerDay = 10
	// verifyPerMinute and verifyPerHour limit "verify now" per organization: each runs DNS lookups.
	verifyPerMinute = 6
	verifyPerHour   = 30
	// verifyTimeout bounds the DNS lookups of one synchronous check.
	verifyTimeout = 10 * time.Second
)

// ErrPageDomainLimit means the page already has MaxDomainsPerPage domains.
var ErrPageDomainLimit = errors.New("statuspages: domain limit of the page reached")

// ErrDomainsNotConfigured means the service was built without a DomainResolver.
var ErrDomainsNotConfigured = errors.New("statuspages: custom domains are not configured")

// DomainOptions configure the custom domain operations of the Service.
type DomainOptions struct {
	// Resolver answers the DNS lookups of a check (the real one or domains.Fake).
	Resolver domains.DomainResolver
	// Rules hold the installation's own names that no customer may take.
	Rules domains.Rules
	// CNAMETarget is where a host's CNAME must point (PRORETA_STATUSPAGE_CNAME_TARGET); empty when
	// the operator has not set one, and then a domain stays verified and never becomes active.
	CNAMETarget string
}

// Domain is a custom domain of a status page as the customer API shows it.
type Domain struct {
	ID            uuid.UUID
	Host          string
	State         domains.State
	Primary       bool
	Disabled      bool
	Token         string
	CNAMETarget   string
	VerifiedAt    *time.Time
	LastCheckedAt *time.Time
	LastError     *string
	CreatedAt     time.Time
}

// ExpiresAt is when a pending domain is released if its TXT record is still missing; nil in every
// other state.
func (d Domain) ExpiresAt() *time.Time {
	if d.State != domains.Pending {
		return nil
	}
	t := d.CreatedAt.Add(domains.PendingTTL)
	return &t
}

func (s *Service) toDomain(r statuspagesdb.ListPageDomainsRow) Domain {
	return Domain{ID: r.ID, Host: r.Host, State: domains.State(r.State), Primary: r.IsPrimary, Disabled: r.Disabled, Token: r.Token,
		CNAMETarget: s.dom.CNAMETarget, VerifiedAt: r.VerifiedAt, LastCheckedAt: r.LastCheckedAt, LastError: r.LastError, CreatedAt: r.CreatedAt}
}

func (s *Service) toDomainLocked(r statuspagesdb.LockPageDomainRow) Domain {
	return s.toDomain(statuspagesdb.ListPageDomainsRow(r))
}

// ListDomains returns the unreleased domains of a page the actor may read, the primary one first.
func (s *Service) ListDomains(ctx context.Context, a Actor, pageID uuid.UUID) ([]Domain, error) {
	var out []Domain
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		page, err := getPage(ctx, tx, a.TenantID, pageID)
		if err != nil {
			return err
		}
		if !a.canRead(page.ProjectID) {
			return ErrNotFound
		}
		out, err = s.domainsOf(ctx, tx, a.TenantID, pageID)
		return err
	})
	return out, err
}

func (s *Service) domainsOf(ctx context.Context, tx pgx.Tx, tenantID, pageID uuid.UUID) ([]Domain, error) {
	rows, err := statuspagesdb.New(tx).ListPageDomains(ctx, statuspagesdb.ListPageDomainsParams{TenantID: tenantID, PageID: pageID})
	if err != nil {
		return nil, fmt.Errorf("statuspages: list domains: %w", err)
	}
	out := make([]Domain, len(rows))
	for i, r := range rows {
		out[i] = s.toDomain(r)
	}
	return out, nil
}

// AddDomain adds a pending domain to a page: the host is validated, the plan limit, the cap of
// MaxDomainsPerPage and the daily limit are checked in the same transaction, under the lock of the
// organization's row, so concurrent adds cannot exceed them. The caller shows the DNS records.
func (s *Service) AddDomain(ctx context.Context, a Actor, pageID uuid.UUID, rawHost string) (Domain, error) {
	token, err := domains.NewToken()
	if err != nil {
		return Domain{}, fmt.Errorf("statuspages: domain token: %w", err)
	}
	id := uuidv7.New()
	var out Domain
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		page, err := lockPage(ctx, q, a.TenantID, pageID)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		host, err := s.dom.Rules.Validate(rawHost)
		if err != nil {
			return err
		}
		if err := plans.CheckCreate(ctx, tx, a.TenantID, plans.ResourceStatusPageDomain); err != nil {
			return err
		}
		n, err := q.CountPageDomains(ctx, statuspagesdb.CountPageDomainsParams{TenantID: a.TenantID, PageID: pageID})
		if err != nil {
			return fmt.Errorf("statuspages: count domains: %w", err)
		}
		if n >= MaxDomainsPerPage {
			return ErrPageDomainLimit
		}
		now := s.clock.Now()
		recent, err := q.CountDomainsAddedSince(ctx, statuspagesdb.CountDomainsAddedSinceParams{TenantID: a.TenantID, Since: now.Add(-24 * time.Hour)})
		if err != nil {
			return fmt.Errorf("statuspages: count added domains: %w", err)
		}
		if recent.N >= MaxDomainsAddedPerDay {
			return &auth.RateLimitedError{RetryAfter: max(recent.Oldest.Add(24*time.Hour).Sub(now), time.Second)}
		}
		if err := q.InsertDomain(ctx, statuspagesdb.InsertDomainParams{ID: id, TenantID: a.TenantID, PageID: pageID, Host: host, Token: token}); err != nil {
			if db.IsUniqueViolation(err, "status_page_domains_tenant_host_idx") {
				var verr validation.Error
				verr.Add("host", "is already added to a status page of this organization")
				return &verr
			}
			return fmt.Errorf("statuspages: insert domain: %w", err)
		}
		if err := s.audit(ctx, tx, a, ActionDomainAdded, pageID, map[string]any{"pageId": pageID.String(), "host": host}); err != nil {
			return err
		}
		out, err = s.lockedDomain(ctx, q, a.TenantID, pageID, id)
		return err
	})
	return out, err
}

func (s *Service) lockedDomain(ctx context.Context, q *statuspagesdb.Queries, tenantID, pageID, id uuid.UUID) (Domain, error) {
	r, err := q.LockPageDomain(ctx, statuspagesdb.LockPageDomainParams{TenantID: tenantID, PageID: pageID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	if err != nil {
		return Domain{}, fmt.Errorf("statuspages: lock domain: %w", err)
	}
	return s.toDomainLocked(r), nil
}

// SetPrimaryDomain makes a serving domain the primary one of its page and clears the previous
// primary in the same transaction. A domain that is not verified or active is ErrUnavailable.
func (s *Service) SetPrimaryDomain(ctx context.Context, a Actor, pageID, domainID uuid.UUID) (Domain, error) {
	var out Domain
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		page, err := lockPage(ctx, q, a.TenantID, pageID)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		d, err := s.lockedDomain(ctx, q, a.TenantID, pageID, domainID)
		if err != nil {
			return err
		}
		if !d.State.Serving() {
			return domains.ErrUnavailable
		}
		if !d.Primary {
			if err := q.ClearPagePrimary(ctx, statuspagesdb.ClearPagePrimaryParams{TenantID: a.TenantID, PageID: pageID}); err != nil {
				return fmt.Errorf("statuspages: clear primary: %w", err)
			}
			if err := q.SetDomainPrimary(ctx, statuspagesdb.SetDomainPrimaryParams{TenantID: a.TenantID, ID: domainID}); err != nil {
				return fmt.Errorf("statuspages: set primary: %w", err)
			}
			if err := s.audit(ctx, tx, a, ActionDomainPrimaryChanged, pageID, map[string]any{"pageId": pageID.String(), "host": d.Host}); err != nil {
				return err
			}
		}
		out, err = s.lockedDomain(ctx, q, a.TenantID, pageID, domainID)
		return err
	})
	return out, err
}

// RemoveDomain releases a domain (the row stays for audit). When it was the primary one, the oldest
// domain that still serves takes over.
func (s *Service) RemoveDomain(ctx context.Context, a Actor, pageID, domainID uuid.UUID) error {
	return db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		page, err := lockPage(ctx, q, a.TenantID, pageID)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		d, err := s.lockedDomain(ctx, q, a.TenantID, pageID, domainID)
		if err != nil {
			return err
		}
		if err := q.ReleaseDomain(ctx, statuspagesdb.ReleaseDomainParams{TenantID: a.TenantID, ID: domainID}); err != nil {
			return fmt.Errorf("statuspages: release domain: %w", err)
		}
		if d.Primary {
			if err := q.PromotePagePrimary(ctx, statuspagesdb.PromotePagePrimaryParams{TenantID: a.TenantID, PageID: pageID}); err != nil {
				return fmt.Errorf("statuspages: promote primary: %w", err)
			}
		}
		return s.audit(ctx, tx, a, ActionDomainRemoved, pageID, map[string]any{"pageId": pageID.String(), "host": d.Host, "by": "user"})
	})
}

// VerifyDomain checks the DNS records of a domain now, synchronously, and returns the domain in
// the state it ended in. It is limited to 6 per minute and 30 per hour per organization
// (*auth.RateLimitedError). A host another organization holds moves here only when that
// organization's own token is no longer in DNS; otherwise the answer is domains.ErrUnavailable.
func (s *Service) VerifyDomain(ctx context.Context, a Actor, pageID, domainID uuid.UUID) (Domain, error) {
	if s.dom.Resolver == nil {
		return Domain{}, ErrDomainsNotConfigured
	}
	// Read and authorize first, so a caller who may not verify never spends the organization's budget.
	var cur Domain
	var held bool
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		page, err := lockPage(ctx, q, a.TenantID, pageID)
		if err != nil {
			return err
		}
		if err := a.checkManage(page.ProjectID); err != nil {
			return err
		}
		if cur, err = s.lockedDomain(ctx, q, a.TenantID, pageID, domainID); err != nil {
			return err
		}
		if cur.State == domains.Pending || cur.State == domains.Failed {
			if err := tx.QueryRow(ctx, `SELECT status_page_domain_held_by_other($1)`, cur.Host).Scan(&held); err != nil {
				return fmt.Errorf("statuspages: held by other: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Domain{}, err
	}
	for _, l := range []*auth.Limiter{s.verifyMinute, s.verifyHour} {
		if ok, wait := l.Reserve(a.TenantID.String()); !ok {
			return Domain{}, &auth.RateLimitedError{RetryAfter: wait}
		}
	}

	dctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	facts := domains.Probe(dctx, s.dom.Resolver, cur.Host, cur.Token, s.dom.CNAMETarget)
	var found []string
	if held && facts.TXT == domains.Present {
		// Only now are the tokens in DNS needed: the takeover compares them in the database, so the
		// holder's token never leaves it.
		found = s.tokensInDNS(dctx, cur.Host)
	}

	var out Domain
	var previous *takenOver
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := statuspagesdb.New(tx)
		if _, err := lockPage(ctx, q, a.TenantID, pageID); err != nil {
			return err
		}
		d, err := s.lockedDomain(ctx, q, a.TenantID, pageID, domainID)
		if err != nil {
			return err
		}
		takeover := false
		if held && facts.TXT == domains.Present && (d.State == domains.Pending || d.State == domains.Failed) {
			var granted bool
			var prevTenant, prevPage *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT granted, previous_tenant_id, previous_page_id FROM status_page_domain_takeover($1, $2, $3)`,
				d.Host, domainID, found).Scan(&granted, &prevTenant, &prevPage); err != nil {
				return fmt.Errorf("statuspages: takeover: %w", err)
			}
			if !granted {
				return domains.ErrUnavailable
			}
			if prevTenant != nil && prevPage != nil {
				takeover, previous = true, &takenOver{tenant: *prevTenant, page: *prevPage, host: d.Host}
			}
		}
		var state string
		var events []string
		err = tx.QueryRow(ctx, `SELECT state, events FROM status_page_domain_record_check($1, $2, $3, $4, $5)`,
			a.TenantID, domainID, proofText(facts.TXT), proofText(facts.CNAME), lastError(facts, d.State)).Scan(&state, &events)
		switch {
		case db.IsUniqueViolation(err, "status_page_domains_host_idx"):
			// Another organization proved the host first, between the read and now.
			return domains.ErrUnavailable
		case err != nil:
			return fmt.Errorf("statuspages: record check: %w", err)
		}
		for _, e := range events {
			if err := s.auditCheckEvent(ctx, tx, a, pageID, d.Host, e, takeover, facts); err != nil {
				return err
			}
		}
		if slicesContainAny(events, "verified", "recovered") {
			if err := q.PromotePagePrimary(ctx, statuspagesdb.PromotePagePrimaryParams{TenantID: a.TenantID, PageID: pageID}); err != nil {
				return fmt.Errorf("statuspages: promote primary: %w", err)
			}
		}
		if slicesContainAny(events, "expired") {
			// The row is released now; show it as it was.
			out = d
			return nil
		}
		out, err = s.lockedDomain(ctx, q, a.TenantID, pageID, domainID)
		return err
	})
	if err != nil {
		return Domain{}, err
	}
	if previous != nil {
		s.auditTakenOver(ctx, *previous)
	}
	return out, nil
}

type takenOver struct {
	tenant, page uuid.UUID
	host         string
}

// auditTakenOver writes the audit row of the organization that lost a host, in its own tenant. It
// cannot share the claimant's transaction (one tenant per transaction); a failure is logged, the
// transfer itself is already committed.
func (s *Service) auditTakenOver(ctx context.Context, p takenOver) {
	err := db.WithTenantTx(ctx, s.pool, p.tenant, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{TenantID: p.tenant, Action: ActionDomainRemoved, EntityType: "status_page", EntityID: p.page.String(),
			Metadata: map[string]any{"pageId": p.page.String(), "host": p.host, "by": "takeover"}})
	})
	if err != nil {
		s.log.ErrorContext(ctx, "status page domain takeover audit failed", "tenant_id", p.tenant.String(), "page_id", p.page.String(), "error", err)
	}
}

func (s *Service) auditCheckEvent(ctx context.Context, tx pgx.Tx, a Actor, pageID uuid.UUID, host, event string, takeover bool, f domains.Facts) error {
	meta := map[string]any{"pageId": pageID.String(), "host": host}
	var action string
	switch event {
	case "verified", "recovered":
		action, meta["takeover"] = ActionDomainVerified, takeover
	case "activated":
		action = ActionDomainActivated
	case "failed":
		action, meta["reason"] = ActionDomainFailed, failureReason(f)
	case "expired":
		action, meta["by"] = ActionDomainRemoved, "expired"
	default:
		return nil
	}
	return s.audit(ctx, tx, a, action, pageID, meta)
}

// tokensInDNS returns every proof token published in the TXT record of host's proof name.
func (s *Service) tokensInDNS(ctx context.Context, host string) []string {
	txt, err := s.dom.Resolver.LookupTXT(ctx, domains.TXTName(host))
	if err != nil {
		return nil
	}
	var out []string
	for _, v := range txt {
		if m := tokenValue.FindStringSubmatch(strings.TrimSpace(v)); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

var tokenValue = regexp.MustCompile(`^` + regexp.QuoteMeta(domains.TXTValue("")) + `([0-9a-f]{32})$`)

func proofText(p domains.Proof) string {
	switch p {
	case domains.Present:
		return "present"
	case domains.Missing:
		return "missing"
	}
	return "unknown"
}

// lastError is the last_error to store for a check: what is wrong now, nil when nothing is. A
// serving domain whose CNAME is right is fine even without its TXT record (the CNAME is the
// proof that remains); a pending one still needs the TXT.
func lastError(f domains.Facts, state domains.State) *string {
	var e string
	switch {
	case f.CNAME == domains.Present && state.Serving():
		return nil
	case f.TXT == domains.Present && f.CNAME == domains.Present:
		return nil
	case f.TXT == domains.Present:
		e = "cname-missing"
		if f.CNAME == domains.Unknown {
			e = "dns-error"
		}
	case f.TXT == domains.Unknown || f.CNAME == domains.Unknown:
		e = "dns-error"
	default:
		e = "txt-missing"
	}
	return &e
}

func failureReason(f domains.Facts) string {
	if e := lastError(f, domains.Failed); e != nil {
		return *e
	}
	return "txt-missing"
}

func slicesContainAny(have []string, want ...string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

// releaseAudit lists the unreleased domains of a page so the page deletion can audit their release
// (the database trigger releases them in the same statement).
func releaseAudit(ctx context.Context, q *statuspagesdb.Queries, tenantID, pageID uuid.UUID) ([]string, error) {
	rows, err := q.ListPageDomains(ctx, statuspagesdb.ListPageDomainsParams{TenantID: tenantID, PageID: pageID})
	if err != nil {
		return nil, fmt.Errorf("statuspages: list domains: %w", err)
	}
	hosts := make([]string, len(rows))
	for i, r := range rows {
		hosts[i] = r.Host
	}
	return hosts, nil
}
