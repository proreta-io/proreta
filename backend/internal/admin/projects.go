package admin

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
)

// ErrProjectsUnavailable means the service was built without the project service.
var ErrProjectsUnavailable = errors.New("admin: project service not configured")

// Project is a project of an organization with its people and contents counted.
type Project struct {
	projects.Project
	MemberCount int
}

// projectActor runs project operations in the organization as an owner. The platform-admin flag is
// checked by the api before any admin handler runs; the rules of the project service (guard rails,
// caps, slugs) apply unchanged.
func (s *Service) projectActor(a Actor, tenantID uuid.UUID) projects.Actor {
	return projects.Actor{TenantID: tenantID, IP: a.IP, Admin: true,
		Access: authz.Actor{UserID: a.UserID, Role: authz.Owner}}
}

// projectTenant checks that the organization exists and, for a change, that it is not deleted.
func (s *Service) projectTenant(ctx context.Context, a Actor, id uuid.UUID, write bool) error {
	if s.projects == nil {
		return ErrProjectsUnavailable
	}
	t, err := s.GetTenant(ctx, a, id)
	if err != nil {
		return err
	}
	if write && t.DeletedAt != nil {
		return ErrTenantDeleted
	}
	return nil
}

// ListProjects returns every live project of the organization, Default first.
func (s *Service) ListProjects(ctx context.Context, a Actor, tenantID uuid.UUID) ([]Project, error) {
	if err := s.projectTenant(ctx, a, tenantID, false); err != nil {
		return nil, err
	}
	pa := s.projectActor(a, tenantID)
	list, err := s.projects.List(ctx, pa)
	if err != nil {
		return nil, err
	}
	out := make([]Project, len(list))
	for i, p := range list {
		members, err := s.projects.Members(ctx, pa, p.ID)
		if err != nil {
			return nil, err
		}
		out[i] = Project{Project: p, MemberCount: len(members)}
	}
	return out, nil
}

func (s *Service) withMemberCount(ctx context.Context, pa projects.Actor, p projects.Project) (Project, error) {
	members, err := s.projects.Members(ctx, pa, p.ID)
	if err != nil {
		return Project{}, err
	}
	return Project{Project: p, MemberCount: len(members)}, nil
}

// CreateProject adds a project under the organization's project cap.
func (s *Service) CreateProject(ctx context.Context, a Actor, tenantID uuid.UUID, in projects.CreateInput) (Project, error) {
	if err := s.projectTenant(ctx, a, tenantID, true); err != nil {
		return Project{}, err
	}
	pa := s.projectActor(a, tenantID)
	p, err := s.projects.Create(ctx, pa, in)
	if err != nil {
		return Project{}, err
	}
	s.recorded(ctx, projects.AdminActionCreated, "tenant_id", tenantID, "project_id", p.ID)
	return s.withMemberCount(ctx, pa, p)
}

// UpdateProject renames a project or changes its slug.
func (s *Service) UpdateProject(ctx context.Context, a Actor, tenantID, id uuid.UUID, in projects.UpdateInput) (Project, error) {
	if err := s.projectTenant(ctx, a, tenantID, true); err != nil {
		return Project{}, err
	}
	pa := s.projectActor(a, tenantID)
	before, err := s.projects.Get(ctx, pa, id)
	if err != nil {
		return Project{}, err
	}
	p, err := s.projects.Update(ctx, pa, id, in)
	if err != nil {
		return Project{}, err
	}
	if p.Name != before.Name || p.Slug != before.Slug {
		s.recorded(ctx, projects.AdminActionUpdated, "tenant_id", tenantID, "project_id", id)
	}
	return s.withMemberCount(ctx, pa, p)
}

// DeleteProject soft-deletes an empty project that is not the Default one.
func (s *Service) DeleteProject(ctx context.Context, a Actor, tenantID, id uuid.UUID) error {
	if err := s.projectTenant(ctx, a, tenantID, true); err != nil {
		return err
	}
	if err := s.projects.Delete(ctx, s.projectActor(a, tenantID), id); err != nil {
		return err
	}
	s.recorded(ctx, projects.AdminActionDeleted, "tenant_id", tenantID, "project_id", id)
	return nil
}

// ProjectMembers lists the people with access to a project.
func (s *Service) ProjectMembers(ctx context.Context, a Actor, tenantID, id uuid.UUID) ([]projects.Member, error) {
	if err := s.projectTenant(ctx, a, tenantID, false); err != nil {
		return nil, err
	}
	return s.projects.Members(ctx, s.projectActor(a, tenantID), id)
}

// PutProjectMember gives a member of the organization a direct role in a project or changes it.
// A user who is not a member of the organization is a 422 on userId.
func (s *Service) PutProjectMember(ctx context.Context, a Actor, tenantID, id, userID uuid.UUID, role authz.Role) (projects.Member, error) {
	if err := s.projectTenant(ctx, a, tenantID, true); err != nil {
		return projects.Member{}, err
	}
	pa := s.projectActor(a, tenantID)
	before, err := s.projects.Members(ctx, pa, id)
	if err != nil {
		return projects.Member{}, err
	}
	m, err := s.projects.PutMember(ctx, pa, id, userID, role)
	if errors.Is(err, projects.ErrMemberNotFound) {
		var verr validation.Error
		verr.Add("userId", "is not a member of this organization")
		return projects.Member{}, verr.Err()
	}
	if err != nil {
		return projects.Member{}, err
	}
	var prev *projects.Member
	for i := range before {
		if before[i].UserID == userID {
			prev = &before[i]
		}
	}
	if prev == nil || !prev.Explicit || *prev.ProjectRole != role {
		action := projects.AdminActionMemberUpdated
		if prev == nil || !prev.Explicit {
			action = projects.AdminActionMemberAdded
		}
		s.recorded(ctx, action, "tenant_id", tenantID, "project_id", id, "user_id", userID)
	}
	return m, nil
}

// RemoveProjectMember takes a member off a project.
func (s *Service) RemoveProjectMember(ctx context.Context, a Actor, tenantID, id, userID uuid.UUID) error {
	if err := s.projectTenant(ctx, a, tenantID, true); err != nil {
		return err
	}
	if err := s.projects.RemoveMember(ctx, s.projectActor(a, tenantID), id, userID); err != nil {
		return err
	}
	s.recorded(ctx, projects.AdminActionMemberRemoved, "tenant_id", tenantID, "project_id", id, "user_id", userID)
	return nil
}
