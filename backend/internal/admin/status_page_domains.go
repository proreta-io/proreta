package admin

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Audit actions of the custom domain abuse switch (also label values of
// proreta_admin_actions_total).
const (
	ActionStatusPageDomainDisabled = "admin.status_page_domain.disabled"
	ActionStatusPageDomainEnabled  = "admin.status_page_domain.enabled"
)

// MaxDomainReasonLen is the longest reason of the abuse switch, in characters.
const MaxDomainReasonLen = 500

var domainStates = map[string]bool{"pending": true, "verified": true, "active": true, "failed": true}

// StatusPageDomain is a custom domain of any organization as the admin sees it.
type StatusPageDomain struct {
	ID               uuid.UUID
	Host             string
	State            string
	Primary          bool
	DisabledAt       *time.Time
	OrganizationID   uuid.UUID
	OrganizationSlug string
	StatusPageID     uuid.UUID
	StatusPageSlug   string
	VerifiedAt       *time.Time
	LastCheckedAt    *time.Time
	LastError        *string
	CreatedAt        time.Time
}

// StatusPageDomainList is one page of domains and the number of all matches.
type StatusPageDomainList struct {
	Items []StatusPageDomain
	Total int
}

const domainColumns = `id, host, state, is_primary, disabled_at, tenant_id, tenant_slug, page_id, page_slug, verified_at, last_checked_at, last_error, created_at`

func (d *StatusPageDomain) dest() []any {
	return []any{&d.ID, &d.Host, &d.State, &d.Primary, &d.DisabledAt, &d.OrganizationID, &d.OrganizationSlug, &d.StatusPageID, &d.StatusPageSlug,
		&d.VerifiedAt, &d.LastCheckedAt, &d.LastError, &d.CreatedAt}
}

// ListStatusPageDomains returns a page of the unreleased domains of every organization, newest
// first. Query matches the host or the organization's slug or name; states (empty for any) keeps
// only domains in those states.
func (s *Service) ListStatusPageDomains(ctx context.Context, actor Actor, page Page, states []string) (StatusPageDomainList, error) {
	page = page.normalized()
	var verr validation.Error
	for _, st := range states {
		if !domainStates[st] {
			verr.Add("state", "must be pending, verified, active or failed")
			break
		}
	}
	if err := verr.Err(); err != nil {
		return StatusPageDomainList{}, err
	}
	if states == nil {
		states = []string{}
	}
	list := StatusPageDomainList{Items: []StatusPageDomain{}}
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		list.Items, list.Total = []StatusPageDomain{}, 0
		rows, err := tx.Query(ctx, `SELECT `+domainColumns+`, total_count FROM admin_status_page_domains_list($1, NULL, $2, $3, $4, $5)`,
			actor.UserID, page.Query, states, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d StatusPageDomain
			if err := rows.Scan(append(d.dest(), &list.Total)...); err != nil {
				return err
			}
			list.Items = append(list.Items, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(list.Items) == 0 && page.Offset > 0 {
			return tx.QueryRow(ctx, `SELECT COALESCE(max(total_count), 0) FROM admin_status_page_domains_list($1, NULL, $2, $3, 1, 0)`,
				actor.UserID, page.Query, states).Scan(&list.Total)
		}
		return nil
	})
	if err != nil {
		return StatusPageDomainList{}, mapError("list status page domains", err)
	}
	return list, nil
}

// SetStatusPageDomainDisabled stops (disabled) or resumes serving a host and records why; the
// audit row is written in the same transaction. A domain already in that state is returned
// unchanged and nothing is written. Reason is required, 1 to 500 characters.
func (s *Service) SetStatusPageDomainDisabled(ctx context.Context, actor Actor, id uuid.UUID, disabled bool, reason string) (StatusPageDomain, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n == 0 || n > MaxDomainReasonLen {
		var verr validation.Error
		verr.Add("reason", "is required, at most 500 characters")
		return StatusPageDomain{}, verr.Err()
	}
	var out StatusPageDomain
	var changed bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT admin_status_page_domain_set_disabled($1, $2, $3, $4, $5, $6)`,
			actor.UserID, id, disabled, reason, uuidv7.New(), actor.ip()).Scan(&changed); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT `+domainColumns+` FROM admin_status_page_domains_list($1, $2, '', NULL, 1, 0)`, actor.UserID, id).Scan(out.dest()...)
	})
	if err != nil {
		return StatusPageDomain{}, mapError("set status page domain disabled", err)
	}
	if changed {
		action := ActionStatusPageDomainEnabled
		if disabled {
			action = ActionStatusPageDomainDisabled
		}
		s.recorded(ctx, action, "host", out.Host, "organizationId", out.OrganizationID)
	}
	return out, nil
}
