package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/projects/projectsdb"
)

// CheckInvite decides, inside the caller's tenant transaction, whether who may invite a person into
// a project with a role, and returns the project's name. The project stays live until the
// transaction ends. ErrNotFound when who has no role in the project or it is gone (so ids of
// other projects reveal nothing), authz.ErrProjectForbidden when who may not manage the project's
// people, authz.ErrForbidden when the role is above who's own.
func CheckInvite(ctx context.Context, tx pgx.Tx, tenantID, project uuid.UUID, who authz.Actor, role authz.Role) (string, error) {
	if !who.Sees(project) {
		return "", ErrNotFound
	}
	name, err := projectsdb.New(tx).LockProjectName(ctx, projectsdb.LockProjectNameParams{TenantID: tenantID, ID: project})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("projects: lock project: %w", err)
	}
	if !who.CanIn(project, "project.members.manage") {
		return "", authz.ErrProjectForbidden
	}
	if !authz.CanManageIn(who, project, "", role) {
		return "", authz.ErrForbidden
	}
	return name, nil
}

// Join gives a member of the organization the role in the project because an invitation was
// accepted. The role only raises: a person who already holds an equal or higher effective role in
// the project (through the organization role or a direct one) is left as is. ErrNotFound when the
// project is gone. granted reports whether a row was written.
func Join(ctx context.Context, tx pgx.Tx, tenantID, project, userID uuid.UUID, role authz.Role) (granted bool, err error) {
	q := projectsdb.New(tx)
	if _, err := q.LockProjectName(ctx, projectsdb.LockProjectNameParams{TenantID: tenantID, ID: project}); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("projects: lock project: %w", err)
	}
	cur, err := q.GetMemberRoles(ctx, projectsdb.GetMemberRolesParams{TenantID: tenantID, ProjectID: project, UserID: userID})
	if err != nil {
		return false, fmt.Errorf("projects: membership: %w", err)
	}
	if authz.Higher(authz.Role(cur.OrganizationRole), role) == authz.Role(cur.OrganizationRole) {
		return false, nil
	}
	if cur.ProjectRole != nil && authz.Higher(authz.Role(*cur.ProjectRole), role) == authz.Role(*cur.ProjectRole) {
		return false, nil
	}
	if err := q.UpsertProjectMember(ctx, projectsdb.UpsertProjectMemberParams{TenantID: tenantID, ProjectID: project, UserID: userID, Role: string(role)}); err != nil {
		return false, fmt.Errorf("projects: put member: %w", err)
	}
	return true, nil
}
