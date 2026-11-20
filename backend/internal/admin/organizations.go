package admin

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/authz"
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
// organization's own transaction); when it fails the new organization is deleted again and the
// error returned.
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

	id, err := s.insertTenant(ctx, actor, in.Slug, name, plan)
	if err != nil {
		return TenantCreated{}, err
	}
	out := TenantCreated{}
	if err := s.addFirstOwner(ctx, actor, id, email, &out); err != nil {
		// Best effort: the organization has no owner, so it must not stay behind.
		if derr := s.DeleteTenant(ctx, actor, id); derr != nil {
			s.log.ErrorContext(ctx, "delete organization left without an owner", "tenant_id", id, "error", derr)
		}
		return TenantCreated{}, err
	}
	if out.Tenant, err = s.GetTenant(ctx, actor, id); err != nil {
		return TenantCreated{}, err
	}
	s.recorded(ctx, ActionCreated, "tenant_id", id, "plan_key", plan)
	return out, nil
}

func (s *Service) insertTenant(ctx context.Context, actor Actor, slug *string, name, plan string) (id uuid.UUID, err error) {
	for attempt := 1; ; attempt++ {
		tid := uuidv7.New()
		sl := ""
		if slug != nil {
			sl = *slug
		} else if sl, err = auth.RandomTenantSlug(); err != nil {
			return id, err
		}
		var created bool
		err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			created, err = callChange(ctx, tx, `SELECT admin_create_tenant($1, $2, $3, $4, $5, $6, $7)`,
				actor.UserID, tid, sl, name, plan, uuidv7.New(), actor.ip())
			return err
		})
		switch {
		case err != nil:
			return id, mapError("create tenant", err)
		case created:
			return tid, nil
		case slug != nil || attempt >= slugAttempts:
			return id, ErrSlugTaken
		}
	}
}

// addFirstOwner makes the account with the address the owner, or creates an owner invitation
// (link only, no email) when there is no such account: the platform admin is the only actor allowed an owner link.
func (s *Service) addFirstOwner(ctx context.Context, actor Actor, id uuid.UUID, email string, out *TenantCreated) error {
	userID, err := s.auth.FindUserByEmail(ctx, email)
	switch {
	case err == nil:
		m, err := s.SetMember(ctx, actor, id, userID, auth.RoleOwner)
		out.Member = &m
		return err
	case errors.Is(err, auth.ErrUserNotFound):
		inv, token, _, err := s.CreateInvitation(ctx, actor, id, auth.InvitationInput{Role: string(authz.Owner), Label: email, SendEmail: new(bool)})
		out.Invitation, out.Token = &inv, token
		return err
	}
	return err
}
