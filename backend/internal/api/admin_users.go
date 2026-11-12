package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api/gen"
)

func toAdminUserSummary(a admin.Account) gen.AdminUserSummary {
	return gen.AdminUserSummary{Id: a.ID, Login: a.Login, DisplayName: a.DisplayName, Status: gen.AdminUserSummaryStatus(a.Status),
		IsPlatformAdmin: a.IsPlatformAdmin, IsSystemAdmin: a.IsSystemAdmin, EmailVerified: a.EmailVerified, MustChangePassword: a.MustChangePassword,
		CreatedAt: a.CreatedAt, OrganizationCount: a.OrganizationCount}
}

func toAdminUserDetail(d admin.AccountDetail) gen.AdminUserDetail {
	out := gen.AdminUserDetail{User: toAdminUserSummary(d.Account), OpenSessions: d.OpenSessions,
		Memberships: make([]gen.AdminUserMembership, 0, len(d.Memberships))}
	for _, m := range d.Memberships {
		out.Memberships = append(out.Memberships, gen.AdminUserMembership{OrganizationId: m.OrganizationID, OrganizationName: m.OrganizationName,
			OrganizationSlug: m.OrganizationSlug, OrganizationStatus: gen.AdminUserMembershipOrganizationStatus(m.OrganizationStatus),
			Role: optRole(m.Role), JoinedAt: m.JoinedAt})
	}
	return out
}

func (s *server) AdminListAllUsers(ctx context.Context, req gen.AdminListAllUsersRequestObject) (gen.AdminListAllUsersResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	pg := page(req.Params.Query, req.Params.Limit, req.Params.Offset)
	var status string
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	list, err := s.admin.ListAccounts(ctx, a, pg, status)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListAllUsers200JSONResponse{Items: make([]gen.AdminUserSummary, 0, len(list.Items)), Total: list.Total,
		Limit: effectiveLimit(pg.Limit), Offset: pg.Offset}
	for _, u := range list.Items {
		out.Items = append(out.Items, toAdminUserSummary(u))
	}
	return out, nil
}

func (s *server) AdminGetUser(ctx context.Context, req gen.AdminGetUserRequestObject) (gen.AdminGetUserResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.admin.GetAccount(ctx, a, req.UserId)
	if err != nil {
		return nil, err
	}
	return gen.AdminGetUser200JSONResponse(toAdminUserDetail(d)), nil
}

func (s *server) AdminDisableUser(ctx context.Context, req gen.AdminDisableUserRequestObject) (gen.AdminDisableUserResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.admin.SetAccountStatus(ctx, a, req.UserId, admin.UserDisabled)
	if err != nil {
		return nil, err
	}
	return gen.AdminDisableUser200JSONResponse(toAdminUserDetail(d)), nil
}

func (s *server) AdminEnableUser(ctx context.Context, req gen.AdminEnableUserRequestObject) (gen.AdminEnableUserResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.admin.SetAccountStatus(ctx, a, req.UserId, admin.UserActive)
	if err != nil {
		return nil, err
	}
	return gen.AdminEnableUser200JSONResponse(toAdminUserDetail(d)), nil
}

func (s *server) AdminRevokeUserSessions(ctx context.Context, req gen.AdminRevokeUserSessionsRequestObject) (gen.AdminRevokeUserSessionsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.admin.RevokeAccountSessions(ctx, a, req.UserId)
	if err != nil {
		return nil, err
	}
	return gen.AdminRevokeUserSessions200JSONResponse{SessionsRevoked: n}, nil
}
