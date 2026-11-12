package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/authz"
)

func names(ps []authz.Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

func (s *server) GetPermissions(ctx context.Context, _ gen.GetPermissionsRequestObject) (gen.GetPermissionsResponseObject, error) {
	a, err := s.projectActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.projects.List(ctx, a)
	if err != nil {
		return nil, err
	}
	out := gen.PermissionsInfo{Organization: names(a.Access.Held()), Projects: make([]gen.ProjectPermissions, len(list)), Catalogue: []gen.PermissionDefinition{}}
	if authz.Valid(a.Access.Role) {
		r := gen.MembershipRole(a.Access.Role)
		out.OrganizationRole = &r
	}
	for i, p := range list {
		out.Projects[i] = gen.ProjectPermissions{ProjectId: p.ID, Name: p.Name, Role: gen.MembershipRole(p.Role), Permissions: names(authz.ProjectPermissions(p.Role))}
	}
	for _, d := range authz.Permissions() {
		roles := []gen.MembershipRole{}
		for _, r := range authz.RolesWith(d.Name) {
			roles = append(roles, gen.MembershipRole(r))
		}
		out.Catalogue = append(out.Catalogue, gen.PermissionDefinition{Name: string(d.Name), Level: gen.PermissionDefinitionLevel(d.Level), Description: d.Description, Roles: roles})
	}
	return gen.GetPermissions200JSONResponse(out), nil
}
