package admin

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
)

// maxTenantName is the length of tenants.name.
const maxTenantName = 200

// slugAttempts bounds retries after a collision of a generated slug (50 random bits each).
const slugAttempts = 3

// TenantCreate is the input of CreateTenant.
type TenantCreate struct {
	Name string
	// Slug is optional: a random "t-..." one is generated when nil.
	Slug *string
	// PlanKey is optional: the internal plan when nil.
	PlanKey *string
	// OwnerEmail is the first owner: an existing account becomes the owner, otherwise an
	// owner invitation is created.
	OwnerEmail string
}

// TenantCreated is a new organization and its first owner: Member when the account existed,
// else Invitation with its secret Token (shown once).
type TenantCreated struct {
	Tenant     Tenant
	Member     *auth.Member
	Invitation *auth.Invitation
	Token      string
}

// CreateTenant creates an organization with its default project and a first owner, so it is never
// left unreachable. The owner step runs after the tenant exists (the member services work in the
// organization's own transaction); when it fails the new organization is discarded (deleted, slug
// released) and the error returned.
func (s *Service) CreateTenant(ctx context.Context, actor Actor, in TenantCreate) (TenantCreated, error) {
	var verr validation.Error
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxTenantName {
		verr.Add("name", "must be 1 to 200 characters")
	}
	if in.Slug != nil {
		if msg := projects.ValidateSlug(*in.Slug); msg != "" {
			verr.Add("slug", msg)
		}
	}
	plan := plans.KeyInternal
	if in.PlanKey != nil {
		if plan = *in.PlanKey; plan == "" {
			verr.Add("planKey", "must not be empty")
		}
	}
	email, msg := auth.ValidateEmail(in.OwnerEmail)
	if msg != "" {
		verr.Add("ownerEmail", msg)
	}
	if err := verr.Err(); err != nil {
		return TenantCreated{}, err
	}

	// One transaction: a refusal of the owner (plan member limit, system admin) or a crash leaves
	// no organization behind and no slug taken.
	var id uuid.UUID
	out := TenantCreated{}
	var action string
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if id, err = s.insertTenant(ctx, tx, actor, in.Slug, name, plan); err != nil {
			return err
		}
		if err := db.SetTenant(ctx, tx, id); err != nil {
			return err
		}
		out.Member, out.Invitation, out.Token, action, err = s.auth.AddFirstOwnerTx(ctx, tx, auth.PlatformActor(id, actor.UserID, actor.IP), email)
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			err = mapError("create organization", err)
		}
		return TenantCreated{}, err
	}
	s.recorded(ctx, action, "tenant_id", id)
	if out.Tenant, err = s.GetTenant(ctx, actor, id); err != nil {
		return TenantCreated{}, err
	}
	s.recorded(ctx, ActionCreated, "tenant_id", id, "plan_key", plan)
	return out, nil
}

// insertTenant creates the tenant in tx, retrying a colliding generated slug.
func (s *Service) insertTenant(ctx context.Context, tx pgx.Tx, actor Actor, slug *string, name, plan string) (id uuid.UUID, err error) {
	for attempt := 1; ; attempt++ {
		tid := uuidv7.New()
		sl := ""
		if slug != nil {
			sl = *slug
		} else if sl, err = auth.RandomTenantSlug(); err != nil {
			return id, err
		}
		created, err := callChange(ctx, tx, `SELECT admin_create_tenant($1, $2, $3, $4, $5, $6, $7)`,
			actor.UserID, tid, sl, name, plan, uuidv7.New(), actor.ip())
		switch {
		case err != nil:
			return id, err
		case created:
			return tid, nil
		case slug != nil || attempt >= slugAttempts:
			return id, ErrSlugTaken
		}
	}
}
