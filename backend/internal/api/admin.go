package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
)

// adminPathPrefix is the prefix of every platform admin operation.
const adminPathPrefix = "/api/v1/admin/"

// errForbidden is an authenticated request the user may not make (not a platform admin).
var errForbidden = errors.New("forbidden")

// requirePlatformAdmin runs after the request validator, which has already required a session
// (and the CSRF token on unsafe methods) for every admin operation. It refuses every
// /api/v1/admin/ request of a user without the platform-admin flag, so a new admin operation
// cannot forget the check; the admin definer functions check the flag again.
func (s *server) requirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, adminPathPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.resolve(r)
		if err != nil {
			writeError(w, r, s.log, err)
			return
		}
		ok, err := s.auth.IsPlatformAdmin(r.Context(), p.UserID)
		if err != nil {
			writeError(w, r, s.log, err)
			return
		}
		if !ok {
			s.log.WarnContext(r.Context(), "admin operation refused: not a platform admin", "user_id", p.UserID)
			writeError(w, r, s.log, errForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminActor is the platform admin of the request handled under ctx.
func (s *server) adminActor(ctx context.Context) (admin.Actor, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return admin.Actor{}, err
	}
	return admin.Actor{UserID: p.UserID, IP: httpx.ClientIP(httpRequest(ctx), s.cfg.TrustedProxyHops)}, nil
}

func page(query *string, limit, offset *int) admin.Page {
	var p admin.Page
	if query != nil {
		p.Query = *query
	}
	if limit != nil {
		p.Limit = *limit
	}
	if offset != nil {
		p.Offset = *offset
	}
	return p
}

func (s *server) AdminGetStats(ctx context.Context, _ gen.AdminGetStatsRequestObject) (gen.AdminGetStatsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.admin.Stats(ctx, a)
	if err != nil {
		return nil, err
	}
	out := gen.AdminGetStats200JSONResponse{Users: st.Users, Monitors: st.Monitors, Heartbeats: st.Heartbeats, Plans: []gen.AdminPlanCount{}}
	out.Organizations.Total, out.Organizations.Active, out.Organizations.Suspended, out.Organizations.Deleted = st.Tenants.Total, st.Tenants.Active, st.Tenants.Suspended, st.Tenants.Deleted
	for _, p := range st.Plans {
		out.Plans = append(out.Plans, gen.AdminPlanCount{Key: p.Key, Name: p.Name, Organizations: p.Tenants})
	}
	return out, nil
}

func (s *server) AdminListOrganizations(ctx context.Context, req gen.AdminListOrganizationsRequestObject) (gen.AdminListOrganizationsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	pg := page(req.Params.Query, req.Params.Limit, req.Params.Offset)
	list, err := s.admin.ListTenants(ctx, a, pg, req.Params.IncludeDeleted != nil && *req.Params.IncludeDeleted)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizations200JSONResponse{Items: make([]gen.AdminOrganization, 0, len(list.Items)), Total: list.Total,
		Limit: effectiveLimit(pg.Limit), Offset: pg.Offset}
	for _, t := range list.Items {
		out.Items = append(out.Items, toAdminOrganization(t))
	}
	return out, nil
}

func (s *server) AdminGetOrganization(ctx context.Context, req gen.AdminGetOrganizationRequestObject) (gen.AdminGetOrganizationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.admin.GetTenant(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	return gen.AdminGetOrganization200JSONResponse(toAdminOrganization(t)), nil
}

func (s *server) AdminUpdateOrganization(ctx context.Context, req gen.AdminUpdateOrganizationRequestObject) (gen.AdminUpdateOrganizationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.admin.UpdateTenant(ctx, a, req.OrganizationId, admin.TenantUpdate{PlanKey: req.Body.PlanKey, Status: req.Body.Status})
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateOrganization200JSONResponse(toAdminOrganization(t)), nil
}

func (s *server) AdminDeleteOrganization(ctx context.Context, req gen.AdminDeleteOrganizationRequestObject) (gen.AdminDeleteOrganizationResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.DeleteTenant(ctx, a, req.OrganizationId); err != nil {
		return nil, err
	}
	return gen.AdminDeleteOrganization204Response{}, nil
}

func (s *server) AdminListOrganizationUsers(ctx context.Context, req gen.AdminListOrganizationUsersRequestObject) (gen.AdminListOrganizationUsersResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	pg := page(req.Params.Query, req.Params.Limit, req.Params.Offset)
	list, err := s.admin.ListUsers(ctx, a, req.OrganizationId, pg)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationUsers200JSONResponse{Items: make([]gen.AdminUser, 0, len(list.Items)), Total: list.Total,
		Limit: effectiveLimit(pg.Limit), Offset: pg.Offset}
	for _, u := range list.Items {
		out.Items = append(out.Items, gen.AdminUser{Id: u.ID, Login: u.Login, Status: gen.AdminUserStatus(u.Status),
			Role: optRole(u.Role), CreatedAt: u.CreatedAt, IsPlatformAdmin: u.IsPlatformAdmin})
	}
	return out, nil
}

// AdminResetUserPassword answers the temporary password once. It is never logged: the access log
// records only the route, and the response passes no middleware that reads bodies; every API
// response already carries Cache-Control: no-store (apiHeaders).
func (s *server) AdminResetUserPassword(ctx context.Context, req gen.AdminResetUserPasswordRequestObject) (gen.AdminResetUserPasswordResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	password, err := s.admin.ResetPassword(ctx, a, uuid.UUID(req.UserId))
	if err != nil {
		return nil, err
	}
	return gen.AdminResetUserPassword200JSONResponse{TemporaryPassword: password}, nil
}

func effectiveLimit(limit int) int {
	if limit <= 0 {
		return admin.DefaultLimit
	}
	return min(limit, admin.MaxLimit)
}

func toAdminOrganization(t admin.Tenant) gen.AdminOrganization {
	return gen.AdminOrganization{Id: t.ID, Slug: t.Slug, Name: t.Name, PlanKey: t.PlanKey, PlanName: t.PlanName,
		Status: gen.OrganizationStatus(t.Status), CreatedAt: t.CreatedAt, DeletedAt: t.DeletedAt,
		Users: t.Users, Monitors: t.Monitors, Heartbeats: t.Heartbeats}
}
