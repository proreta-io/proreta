package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/projects"
)

func toAdminProject(p admin.Project) gen.AdminProject {
	return gen.AdminProject{Id: p.ID, Name: p.Name, Slug: p.Slug, IsDefault: p.IsDefault, AllMembers: p.AllMembers,
		MemberCount: p.MemberCount, MonitorCount: p.MonitorCount, ChannelCount: p.ChannelCount,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func (s *server) AdminListOrganizationProjects(ctx context.Context, req gen.AdminListOrganizationProjectsRequestObject) (gen.AdminListOrganizationProjectsResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListProjects(ctx, a, req.OrganizationId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationProjects200JSONResponse{Items: make([]gen.AdminProject, len(list))}
	for i, p := range list {
		out.Items[i] = toAdminProject(p)
	}
	return out, nil
}

func (s *server) AdminCreateOrganizationProject(ctx context.Context, req gen.AdminCreateOrganizationProjectRequestObject) (gen.AdminCreateOrganizationProjectResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.admin.CreateProject(ctx, a, req.OrganizationId, projects.CreateInput{Name: req.Body.Name, Slug: req.Body.Slug})
	if err != nil {
		return nil, err
	}
	return gen.AdminCreateOrganizationProject201JSONResponse(toAdminProject(p)), nil
}

func (s *server) AdminUpdateOrganizationProject(ctx context.Context, req gen.AdminUpdateOrganizationProjectRequestObject) (gen.AdminUpdateOrganizationProjectResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.admin.UpdateProject(ctx, a, req.OrganizationId, req.ProjectId, projects.UpdateInput{Name: req.Body.Name, Slug: req.Body.Slug})
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdateOrganizationProject200JSONResponse(toAdminProject(p)), nil
}

func (s *server) AdminDeleteOrganizationProject(ctx context.Context, req gen.AdminDeleteOrganizationProjectRequestObject) (gen.AdminDeleteOrganizationProjectResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.DeleteProject(ctx, a, req.OrganizationId, req.ProjectId); err != nil {
		return nil, err
	}
	return gen.AdminDeleteOrganizationProject204Response{}, nil
}

func (s *server) AdminListOrganizationProjectMembers(ctx context.Context, req gen.AdminListOrganizationProjectMembersRequestObject) (gen.AdminListOrganizationProjectMembersResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ProjectMembers(ctx, a, req.OrganizationId, req.ProjectId)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListOrganizationProjectMembers200JSONResponse{Items: make([]gen.ProjectMember, len(list))}
	for i, m := range list {
		out.Items[i] = toProjectMember(m, true)
	}
	return out, nil
}

func (s *server) AdminPutOrganizationProjectMember(ctx context.Context, req gen.AdminPutOrganizationProjectMemberRequestObject) (gen.AdminPutOrganizationProjectMemberResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	var role *authz.Role
	if req.Body.Role != nil {
		r := authz.Role(*req.Body.Role)
		role = &r
	}
	m, err := s.admin.PutProjectMember(ctx, a, req.OrganizationId, req.ProjectId, req.UserId, role)
	if err != nil {
		return nil, err
	}
	return gen.AdminPutOrganizationProjectMember200JSONResponse(toProjectMember(m, true)), nil
}

func (s *server) AdminRemoveOrganizationProjectMember(ctx context.Context, req gen.AdminRemoveOrganizationProjectMemberRequestObject) (gen.AdminRemoveOrganizationProjectMemberResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.RemoveProjectMember(ctx, a, req.OrganizationId, req.ProjectId, req.UserId); err != nil {
		return nil, err
	}
	return gen.AdminRemoveOrganizationProjectMember204Response{}, nil
}
