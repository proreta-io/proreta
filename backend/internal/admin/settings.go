package admin

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
)

// The settings areas of an organization that its owner sees in the app: plan and usage, sign-in
// domains, notification channels, status pages and Specula. The platform admin reads them all and
// changes the domains and the Specula through the owner's services; a change happens in the
// organization's own transaction. Channels and status pages are read-only here.

// ErrSettingsUnavailable means the service was built without the service an operation needs.
var ErrSettingsUnavailable = errors.New("admin: organization settings services not configured")

// owner is the actor the owner-side services see: every project, the owner's role.
func owner(a Actor) authz.Actor { return authz.Actor{UserID: a.UserID, Role: authz.Owner} }

// settingsTenant checks that the organization exists and, for a change, that it is not deleted.
func (s *Service) settingsTenant(ctx context.Context, a Actor, id uuid.UUID, write bool) error {
	t, err := s.GetTenant(ctx, a, id)
	if err != nil {
		return err
	}
	if write && t.DeletedAt != nil {
		return ErrTenantDeleted
	}
	return nil
}

// Usage returns the plan, its limits and the usage of the organization.
func (s *Service) Usage(ctx context.Context, a Actor, id uuid.UUID) (plans.Usage, error) {
	if s.plans == nil {
		return plans.Usage{}, ErrSettingsUnavailable
	}
	// A deleted organization has no quota to resolve.
	if err := s.settingsTenant(ctx, a, id, true); err != nil {
		return plans.Usage{}, err
	}
	return s.plans.Usage(ctx, id)
}

// ListAuthDomains returns the sign-in domains the organization claimed.
func (s *Service) ListAuthDomains(ctx context.Context, a Actor, id uuid.UUID) ([]auth.Domain, error) {
	if err := s.settingsTenant(ctx, a, id, false); err != nil {
		return nil, err
	}
	return s.auth.ListAuthDomains(ctx, id)
}

// UpdateAuthDomain changes the default role of a domain or switches auto-join on or off.
func (s *Service) UpdateAuthDomain(ctx context.Context, a Actor, id, domainID uuid.UUID, defaultRole *string, enabled *bool) (auth.Domain, error) {
	if err := s.settingsTenant(ctx, a, id, true); err != nil {
		return auth.Domain{}, err
	}
	d, err := s.auth.UpdateAuthDomain(ctx, auth.PlatformActor(id, a.UserID, a.IP), domainID, defaultRole, enabled)
	if err != nil {
		return auth.Domain{}, err
	}
	s.recorded(ctx, auth.ActionAdminAuthDomainPrefix+"updated", "tenant_id", id, "domain_id", domainID)
	return d, nil
}

// DeleteAuthDomain releases a domain; members who joined through it stay.
func (s *Service) DeleteAuthDomain(ctx context.Context, a Actor, id, domainID uuid.UUID) error {
	if err := s.settingsTenant(ctx, a, id, true); err != nil {
		return err
	}
	if err := s.auth.DeleteAuthDomain(ctx, auth.PlatformActor(id, a.UserID, a.IP), domainID); err != nil {
		return err
	}
	s.recorded(ctx, auth.ActionAdminAuthDomainPrefix+"deleted", "tenant_id", id, "domain_id", domainID)
	return nil
}

// ListChannels returns every live channel of the organization as its owner sees them.
func (s *Service) ListChannels(ctx context.Context, a Actor, id uuid.UUID) ([]notify.Channel, error) {
	if s.channels == nil {
		return nil, ErrSettingsUnavailable
	}
	if err := s.settingsTenant(ctx, a, id, false); err != nil {
		return nil, err
	}
	list, err := s.channels.List(ctx, id, authz.Scope{All: true}, nil)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = list[i].ForViewer(owner(a))
	}
	return list, nil
}

// ListStatusPages returns every status page of the organization as its owner sees them.
func (s *Service) ListStatusPages(ctx context.Context, a Actor, id uuid.UUID) ([]statuspages.Page, error) {
	if s.statusPages == nil {
		return nil, ErrSettingsUnavailable
	}
	if err := s.settingsTenant(ctx, a, id, false); err != nil {
		return nil, err
	}
	return s.statusPages.List(ctx, statuspages.Actor{TenantID: id, IP: a.IP, Access: owner(a)}, nil)
}

func (s *Service) speculaActor(a Actor, id uuid.UUID) specula.Actor {
	return specula.Actor{TenantID: id, UserID: a.UserID, IP: a.IP, Admin: true}
}

// ListSpecula returns every Specula of the organization, revoked ones included.
func (s *Service) ListSpecula(ctx context.Context, a Actor, id uuid.UUID) ([]specula.Specula, error) {
	if s.specula == nil {
		return nil, ErrSettingsUnavailable
	}
	if err := s.settingsTenant(ctx, a, id, false); err != nil {
		return nil, err
	}
	return s.specula.List(ctx, id)
}

// CreateSpecula registers a Specula and returns it with its token, shown once.
func (s *Service) CreateSpecula(ctx context.Context, a Actor, id uuid.UUID, in specula.CreateInput) (specula.Specula, string, error) {
	if s.specula == nil {
		return specula.Specula{}, "", ErrSettingsUnavailable
	}
	if err := s.settingsTenant(ctx, a, id, true); err != nil {
		return specula.Specula{}, "", err
	}
	sp, token, err := s.specula.Create(ctx, s.speculaActor(a, id), in)
	if err != nil {
		return specula.Specula{}, "", err
	}
	s.recorded(ctx, specula.AdminActionCreated, "tenant_id", id, "specula_id", sp.ID)
	return sp, token, nil
}

// UpdateSpecula renames a Specula or changes the projects that may use it.
func (s *Service) UpdateSpecula(ctx context.Context, a Actor, id, speculaID uuid.UUID, in specula.UpdateInput) (specula.Specula, error) {
	if s.specula == nil {
		return specula.Specula{}, ErrSettingsUnavailable
	}
	if err := s.settingsTenant(ctx, a, id, true); err != nil {
		return specula.Specula{}, err
	}
	sp, err := s.specula.Update(ctx, s.speculaActor(a, id), speculaID, in)
	if err != nil {
		return specula.Specula{}, err
	}
	s.recorded(ctx, specula.AdminActionUpdated, "tenant_id", id, "specula_id", speculaID)
	return sp, nil
}

// RevokeSpecula stops a Specula at once.
func (s *Service) RevokeSpecula(ctx context.Context, a Actor, id, speculaID uuid.UUID) error {
	if s.specula == nil {
		return ErrSettingsUnavailable
	}
	if err := s.settingsTenant(ctx, a, id, true); err != nil {
		return err
	}
	if err := s.specula.Revoke(ctx, s.speculaActor(a, id), speculaID); err != nil {
		return err
	}
	s.recorded(ctx, specula.AdminActionRevoked, "tenant_id", id, "specula_id", speculaID)
	return nil
}
