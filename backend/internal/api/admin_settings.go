package api

import (
	"context"
	"time"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/specula"
)

// The settings areas of an organization as its owner sees them: plan and usage, sign-in domains,
// notification channels, status pages and Specula. The domains and the Specula can be changed;
// channels and status pages are read-only here.

func (s *server) AdminGetOrganizationUsage(ctx context.Context, req gen.AdminGetOrganizationUsageRequestObject) (gen.AdminGetOrganizationUsageResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.admin.Usage(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	return gen.AdminGetOrganizationUsage200JSONResponse(toUsage(u)), nil
}

func (s *server) AdminListOrganizationAuthDomains(ctx context.Context, req gen.AdminListOrganizationAuthDomainsRequestObject) (gen.AdminListOrganizationAuthDomainsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListAuthDomains(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationAuthDomains200JSONResponse{Items: make([]gen.AuthDomain, len(list))}
	for i, d := range list {
		out.Items[i] = toAuthDomain(d)
	}
	return out, nil
}

func (s *server) AdminUpdateOrganizationAuthDomain(ctx context.Context, req gen.AdminUpdateOrganizationAuthDomainRequestObject) (gen.AdminUpdateOrganizationAuthDomainResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	var role *string
	if req.Body.DefaultRole != nil {
		r := string(*req.Body.DefaultRole)
		role = &r
	}
	d, err := s.admin.UpdateAuthDomain(ctx, a, req.OrganizationId, req.DomainId, role, req.Body.Enabled)
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateOrganizationAuthDomain200JSONResponse(toAuthDomain(d)), nil
}

func (s *server) AdminDeleteOrganizationAuthDomain(ctx context.Context, req gen.AdminDeleteOrganizationAuthDomainRequestObject) (gen.AdminDeleteOrganizationAuthDomainResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.DeleteAuthDomain(ctx, a, req.OrganizationId, req.DomainId); err != nil {
		return nil, err
	}
	return gen.AdminDeleteOrganizationAuthDomain204Response{}, nil
}

func (s *server) AdminListOrganizationChannels(ctx context.Context, req gen.AdminListOrganizationChannelsRequestObject) (gen.AdminListOrganizationChannelsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListChannels(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationChannels200JSONResponse{Items: make([]gen.Channel, len(list))}
	for i, c := range list {
		out.Items[i] = toChannel(c)
	}
	return out, nil
}

func (s *server) AdminListOrganizationStatusPages(ctx context.Context, req gen.AdminListOrganizationStatusPagesRequestObject) (gen.AdminListOrganizationStatusPagesResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListStatusPages(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationStatusPages200JSONResponse{Items: make([]gen.StatusPage, len(list))}
	for i, p := range list {
		out.Items[i] = s.toStatusPage(p)
	}
	return out, nil
}

func (s *server) AdminListOrganizationSpecula(ctx context.Context, req gen.AdminListOrganizationSpeculaRequestObject) (gen.AdminListOrganizationSpeculaResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListSpecula(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := gen.AdminListOrganizationSpecula200JSONResponse{Items: make([]gen.Specula, len(list))}
	for i, sp := range list {
		out.Items[i] = toSpecula(sp, true, now)
	}
	return out, nil
}

func (s *server) AdminCreateOrganizationSpecula(ctx context.Context, req gen.AdminCreateOrganizationSpeculaRequestObject) (gen.AdminCreateOrganizationSpeculaResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	in := specula.CreateInput{Name: req.Body.Name, AllProjects: true}
	if req.Body.AllProjects != nil {
		in.AllProjects = *req.Body.AllProjects
	}
	if req.Body.ProjectIds != nil {
		in.ProjectIDs = *req.Body.ProjectIds
	}
	sp, token, err := s.admin.CreateSpecula(ctx, a, req.OrganizationId, in)
	if err != nil {
		return nil, err
	}
	return gen.AdminCreateOrganizationSpecula201JSONResponse{Specula: toSpecula(sp, true, time.Now()), Token: token}, nil
}

func (s *server) AdminUpdateOrganizationSpecula(ctx context.Context, req gen.AdminUpdateOrganizationSpeculaRequestObject) (gen.AdminUpdateOrganizationSpeculaResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	sp, err := s.admin.UpdateSpecula(ctx, a, req.OrganizationId, req.SpeculaId, specula.UpdateInput{Name: b.Name, AllProjects: b.AllProjects, ProjectIDs: b.ProjectIds})
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateOrganizationSpecula200JSONResponse(toSpecula(sp, true, time.Now())), nil
}

func (s *server) AdminRevokeOrganizationSpecula(ctx context.Context, req gen.AdminRevokeOrganizationSpeculaRequestObject) (gen.AdminRevokeOrganizationSpeculaResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.RevokeSpecula(ctx, a, req.OrganizationId, req.SpeculaId); err != nil {
		return nil, err
	}
	return gen.AdminRevokeOrganizationSpecula204Response{}, nil
}
