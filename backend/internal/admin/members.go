package admin

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/tenants"
)

// Members and invitations of an organization. The platform admin acts through the member
// services of internal/auth inside the organization's own transaction, with the owner's rights
// (auth.PlatformActor), so the rules (last owner, platform admin, plan limit, row-lock order)
// stay in one place. The organization is read through admin_get_tenant first, which also
// checks the platform-admin flag in the database.

// memberActor checks that the caller is a platform admin and the organization exists, and
// returns the actor for the member services. A deleted organization is ErrTenantDeleted when
// write is set.
func (s *Service) memberActor(ctx context.Context, actor Actor, orgID uuid.UUID, write bool) (auth.Actor, error) {
	t, err := s.GetTenant(ctx, actor, orgID)
	if err != nil {
		return auth.Actor{}, err
	}
	if write && t.DeletedAt != nil {
		return auth.Actor{}, ErrTenantDeleted
	}
	return auth.PlatformActor(orgID, actor.UserID, actor.IP), nil
}

// mapMemberError turns the failure to lock a tenant that was deleted after memberActor checked
// it into ErrTenantDeleted.
func mapMemberError(err error) error {
	if errors.Is(err, tenants.ErrNotFound) {
		return ErrTenantDeleted
	}
	return err
}

// ListMembers returns the members of the organization, oldest first.
func (s *Service) ListMembers(ctx context.Context, actor Actor, orgID uuid.UUID) ([]auth.Member, error) {
	if _, err := s.memberActor(ctx, actor, orgID, false); err != nil {
		return nil, err
	}
	return s.auth.ListMembers(ctx, orgID)
}

// SetMember adds an existing account to the organization or changes its role.
func (s *Service) SetMember(ctx context.Context, actor Actor, orgID, userID uuid.UUID, role string) (auth.Member, error) {
	a, err := s.memberActor(ctx, actor, orgID, true)
	if err != nil {
		return auth.Member{}, err
	}
	m, action, err := s.auth.SetMember(ctx, a, userID, role)
	if err != nil {
		return auth.Member{}, mapMemberError(err)
	}
	if action != "" {
		s.recorded(ctx, action, "tenant_id", orgID, "target_user_id", userID)
	}
	return m, nil
}

// AddMemberByEmail is SetMember for the account with the address; auth.ErrUserNotFound when
// there is none.
func (s *Service) AddMemberByEmail(ctx context.Context, actor Actor, orgID uuid.UUID, email, role string) (auth.Member, error) {
	if _, err := s.memberActor(ctx, actor, orgID, true); err != nil {
		return auth.Member{}, err
	}
	userID, err := s.auth.FindUserByEmail(ctx, email)
	if err != nil {
		return auth.Member{}, err
	}
	return s.SetMember(ctx, actor, orgID, userID, role)
}

// RemoveMember ends the membership and the user's sessions in the organization.
func (s *Service) RemoveMember(ctx context.Context, actor Actor, orgID, userID uuid.UUID) error {
	a, err := s.memberActor(ctx, actor, orgID, true)
	if err != nil {
		return err
	}
	if err := s.auth.AdminRemoveMember(ctx, a, userID); err != nil {
		return mapMemberError(err)
	}
	s.recorded(ctx, auth.ActionAdminMemberRemoved, "tenant_id", orgID, "target_user_id", userID)
	return nil
}

// ListInvitations returns the newest invitations of the organization.
func (s *Service) ListInvitations(ctx context.Context, actor Actor, orgID uuid.UUID) ([]auth.Invitation, error) {
	if _, err := s.memberActor(ctx, actor, orgID, false); err != nil {
		return nil, err
	}
	return s.auth.ListInvitations(ctx, orgID)
}

// CreateInvitation creates an invitation and returns it with its token, shown once.
func (s *Service) CreateInvitation(ctx context.Context, actor Actor, orgID uuid.UUID, in auth.InvitationInput) (auth.Invitation, string, bool, error) {
	a, err := s.memberActor(ctx, actor, orgID, true)
	if err != nil {
		return auth.Invitation{}, "", false, err
	}
	inv, token, emailed, err := s.auth.AdminCreateInvitation(ctx, a, in)
	if err != nil {
		return auth.Invitation{}, "", false, mapMemberError(err)
	}
	s.recorded(ctx, auth.ActionAdminInvitationCreated, "tenant_id", orgID, "invitation_id", inv.ID)
	return inv, token, emailed, nil
}

// RevokeInvitation stops an open invitation; a closed one is left as it is.
func (s *Service) RevokeInvitation(ctx context.Context, actor Actor, orgID, invitationID uuid.UUID) error {
	a, err := s.memberActor(ctx, actor, orgID, true)
	if err != nil {
		return err
	}
	revoked, err := s.auth.AdminRevokeInvitation(ctx, a, invitationID)
	if err != nil {
		return mapMemberError(err)
	}
	if revoked {
		s.recorded(ctx, auth.ActionAdminInvitationRevoked, "tenant_id", orgID, "invitation_id", invitationID)
	}
	return nil
}
