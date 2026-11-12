package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/auth"
)

func toAdminMember(m auth.Member) gen.AdminMember {
	return gen.AdminMember{UserId: m.UserID, Login: m.Login, DisplayName: m.DisplayName, Role: gen.MembershipRole(m.Role),
		Status: gen.AdminMemberStatus(m.Status), JoinedAt: m.JoinedAt, IsPlatformAdmin: m.IsPlatformAdmin, IsSystemAdmin: m.IsSystemAdmin}
}

func (s *server) AdminListOrganizationMembers(ctx context.Context, req gen.AdminListOrganizationMembersRequestObject) (gen.AdminListOrganizationMembersResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListMembers(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationMembers200JSONResponse{Items: make([]gen.AdminMember, len(list))}
	for i, m := range list {
		out.Items[i] = toAdminMember(m)
	}
	return out, nil
}

func (s *server) AdminAddOrganizationMember(ctx context.Context, req gen.AdminAddOrganizationMemberRequestObject) (gen.AdminAddOrganizationMemberResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.admin.AddMemberByEmail(ctx, a, req.OrganizationId, req.Body.Email, string(req.Body.Role))
	if err != nil {
		return nil, err
	}
	return gen.AdminAddOrganizationMember200JSONResponse(toAdminMember(m)), nil
}

func (s *server) AdminSetOrganizationMember(ctx context.Context, req gen.AdminSetOrganizationMemberRequestObject) (gen.AdminSetOrganizationMemberResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.admin.SetMember(ctx, a, req.OrganizationId, req.UserId, string(req.Body.Role))
	if err != nil {
		return nil, err
	}
	return gen.AdminSetOrganizationMember200JSONResponse(toAdminMember(m)), nil
}

func (s *server) AdminRemoveOrganizationMember(ctx context.Context, req gen.AdminRemoveOrganizationMemberRequestObject) (gen.AdminRemoveOrganizationMemberResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.RemoveMember(ctx, a, req.OrganizationId, req.UserId); err != nil {
		return nil, err
	}
	return gen.AdminRemoveOrganizationMember204Response{}, nil
}

func (s *server) AdminListOrganizationInvitations(ctx context.Context, req gen.AdminListOrganizationInvitationsRequestObject) (gen.AdminListOrganizationInvitationsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListInvitations(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationInvitations200JSONResponse{Items: make([]gen.Invitation, len(list))}
	for i, inv := range list {
		out.Items[i] = toInvitation(inv)
	}
	return out, nil
}

// AdminCreateOrganizationInvitation answers the invitation link once, like CreateInvitation.
func (s *server) AdminCreateOrganizationInvitation(ctx context.Context, req gen.AdminCreateOrganizationInvitationRequestObject) (gen.AdminCreateOrganizationInvitationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	in := auth.InvitationInput{Role: string(req.Body.Role), SendEmail: req.Body.SendEmail}
	if req.Body.Label != nil {
		in.Label = *req.Body.Label
	}
	if req.Body.Email != nil {
		in.Email = *req.Body.Email
	}
	inv, token, emailed, err := s.admin.CreateInvitation(ctx, a, req.OrganizationId, in)
	if err != nil {
		return nil, err
	}
	return gen.AdminCreateOrganizationInvitation201JSONResponse{Invitation: toInvitation(inv), InviteUrl: s.cfg.PublicURL + "/invite#" + token, EmailQueued: emailed}, nil
}

func (s *server) AdminRevokeOrganizationInvitation(ctx context.Context, req gen.AdminRevokeOrganizationInvitationRequestObject) (gen.AdminRevokeOrganizationInvitationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.RevokeInvitation(ctx, a, req.OrganizationId, req.InvitationId); err != nil {
		return nil, err
	}
	return gen.AdminRevokeOrganizationInvitation204Response{}, nil
}
