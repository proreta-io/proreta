-- Members and invitations. Every query runs in a tenant transaction.

-- name: ListMembers :many
SELECT m.user_id, u.email, u.display_name, u.status, u.is_platform_admin, u.is_system_admin, coalesce(m.role, '')::text AS role, m.created_at
FROM memberships AS m
JOIN users AS u ON u.id = m.user_id
WHERE m.tenant_id = @tenant_id
ORDER BY m.created_at, m.user_id;

-- name: LockMember :one
-- The membership a role change or removal acts on, locked until the end of the transaction.
SELECT coalesce(m.role, '')::text AS role, m.created_at, u.email, u.display_name, u.status, u.is_platform_admin, u.is_system_admin
FROM memberships AS m
JOIN users AS u ON u.id = m.user_id
WHERE m.tenant_id = @tenant_id AND m.user_id = @user_id
FOR UPDATE OF m;

-- name: CountOtherActiveOwners :one
SELECT count(*)
FROM memberships AS m
JOIN users AS u ON u.id = m.user_id
WHERE m.tenant_id = @tenant_id AND m.role = 'owner' AND m.user_id <> @user_id AND u.status = 'active';

-- name: UpdateMembershipRole :execrows
UPDATE memberships
SET role = nullif(@role::text, '')
WHERE tenant_id = @tenant_id AND user_id = @user_id;

-- name: DeleteMembership :execrows
-- The user's sessions in the tenant go with it (sessions reference memberships ON DELETE CASCADE).
DELETE FROM memberships
WHERE tenant_id = @tenant_id AND user_id = @user_id;

-- name: ListMembersInProjects :many
-- The people a project-only person may see: everyone with an organization role (it covers every
-- project) and everyone with a direct role in one of the projects. Reads project_members, owned by
-- internal/projects, read-only.
SELECT m.user_id, u.email, u.display_name, u.status, u.is_platform_admin, u.is_system_admin, coalesce(m.role, '')::text AS role, m.created_at
FROM memberships AS m
JOIN users AS u ON u.id = m.user_id
WHERE m.tenant_id = @tenant_id
  AND (m.role IS NOT NULL
       OR EXISTS (SELECT 1 FROM project_members AS pm
                  WHERE pm.tenant_id = m.tenant_id AND pm.user_id = m.user_id AND pm.project_id = ANY(@project_ids::uuid[])))
ORDER BY m.created_at, m.user_id;

-- name: InsertInvitation :exec
INSERT INTO invitations (id, tenant_id, role, label, token_hash, created_by, created_at, expires_at, project_id)
VALUES (@id, @tenant_id, @role, @label, @token_hash, @created_by, @created_at, @expires_at, @project_id);

-- name: ListInvitations :many
-- An invitation of a deleted project is shown as closed (project_deleted). With all_projects false
-- only the invitations of the listed projects are returned. Reads projects, owned by
-- internal/projects, read-only.
SELECT i.id, i.role, i.label, i.created_at, i.expires_at, i.accepted_at, i.revoked_at,
       c.email AS created_by_login, a.email AS accepted_by_login,
       i.project_id, p.name AS project_name, (p.deleted_at IS NOT NULL)::boolean AS project_deleted
FROM invitations AS i
JOIN users AS c ON c.id = i.created_by
LEFT JOIN users AS a ON a.id = i.accepted_by
LEFT JOIN projects AS p ON p.tenant_id = i.tenant_id AND p.id = i.project_id
WHERE i.tenant_id = @tenant_id
  AND (@all_projects::boolean OR i.project_id = ANY(@project_ids::uuid[]))
  AND (sqlc.narg(only_project)::uuid IS NULL OR i.project_id = sqlc.narg(only_project))
ORDER BY i.created_at DESC, i.id DESC
LIMIT 200;

-- name: LockInvitationByHash :one
SELECT id, role, expires_at, accepted_at, revoked_at, project_id
FROM invitations
WHERE tenant_id = @tenant_id AND token_hash = @token_hash
FOR UPDATE;

-- name: LockInvitation :one
SELECT id, role, expires_at, accepted_at, revoked_at, project_id
FROM invitations
WHERE tenant_id = @tenant_id AND id = @id
FOR UPDATE;

-- name: MarkInvitationAccepted :execrows
UPDATE invitations
SET accepted_at = @now, accepted_by = @user_id
WHERE tenant_id = @tenant_id AND id = @id AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: RevokeInvitation :execrows
UPDATE invitations
SET revoked_at = @now
WHERE tenant_id = @tenant_id AND id = @id AND accepted_at IS NULL AND revoked_at IS NULL;
