-- name: LockProjectName :one
-- The project an invitation is created for: live, and kept so until the transaction ends (like
-- LockProjectForUse, without blocking changes of its members).
SELECT name
FROM projects
WHERE tenant_id = @tenant_id AND id = @id AND deleted_at IS NULL
FOR KEY SHARE;
