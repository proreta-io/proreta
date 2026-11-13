-- name: GetUserByEmail :one
SELECT id, email, password_hash, display_name, status, is_platform_admin, is_system_admin, updated_at, password_changed_at, configured_password_hash, password_must_change, email_verified_at
FROM users
WHERE email = @email;

-- name: LockUserCredential :one
-- The login's re-check of a verified credential. FOR SHARE waits for a password reset
-- or rotation in flight and blocks a new one until the session is committed; the api's column
-- UPDATE grant on users is what allows the row lock.
SELECT password_hash, status, updated_at
FROM users
WHERE id = @id
FOR SHARE;

-- name: GetUserByID :one
SELECT id, email, display_name, status, is_platform_admin, is_system_admin, password_must_change, email_verified_at
FROM users
WHERE id = @id;

-- name: GetPasswordMustChange :one
-- Read for every authenticated request: while it is true the api answers only the session and
-- password-change operations.
SELECT password_must_change
FROM users
WHERE id = @id;

-- name: InsertUserIfMissing :exec
-- A user created from the Secret signs in with a password somebody else knows: it must change it.
INSERT INTO users (id, email, password_hash, display_name, password_must_change)
VALUES (@id, @email, @password_hash, @display_name, true)
ON CONFLICT (email) DO NOTHING;

-- name: ApplyConfiguredPasswordHash :execrows
-- The configured admin hash replaces the password and clears the in-app marker; it is also
-- remembered, so that the next start can tell an unchanged Secret from a new one. A new hash
-- (must_change) asks for a change at the next sign-in; the flag is never cleared here.
UPDATE users
SET password_hash = @password_hash, password_changed_at = NULL, configured_password_hash = @password_hash,
    password_must_change = password_must_change OR @must_change,
    updated_at = CASE WHEN password_hash IS DISTINCT FROM @password_hash THEN now() ELSE updated_at END
WHERE id = @id;

-- name: LockUserForPasswordChange :one
-- The password change re-reads the credential it verified; FOR UPDATE makes concurrent changes
-- and resets wait for each other.
SELECT email, password_hash, status, updated_at, email_verified_at
FROM users
WHERE id = @id
FOR UPDATE;

-- name: SetUserPassword :exec
UPDATE users
SET password_hash = @password_hash, password_changed_at = @changed_at, password_must_change = false, updated_at = @changed_at
WHERE id = @id;

-- name: LockLiveSession :one
-- A password change acts only for a session that still exists.
SELECT id
FROM sessions
WHERE id = @id AND expires_at > @now
FOR UPDATE;

-- name: GetUserCredential :one
SELECT email, password_hash, display_name, status, is_platform_admin, is_system_admin
FROM users
WHERE id = @id;

-- name: UpsertMembership :exec
INSERT INTO memberships (tenant_id, user_id, role)
VALUES (@tenant_id, @user_id, nullif(@role::text, ''))
ON CONFLICT (tenant_id, user_id) DO UPDATE
SET role = EXCLUDED.role
WHERE memberships.role IS DISTINCT FROM EXCLUDED.role;

-- name: GetMembershipRole :one
-- The organization role; empty for a person who belongs only through projects.
SELECT coalesce(role, '')::text AS role
FROM memberships
WHERE tenant_id = @tenant_id AND user_id = @user_id;

-- name: InsertSession :exec
INSERT INTO sessions (id, user_id, tenant_id, token_hash, csrf_token, created_at, expires_at, last_seen_at, ip, user_agent)
VALUES (@id, @user_id, @tenant_id, @token_hash, @csrf_token, @created_at, @expires_at, @created_at, @ip, @user_agent);

-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = @now
WHERE id = @id AND last_seen_at < @stale_before;

-- name: DeleteSession :execrows
DELETE FROM sessions
WHERE id = @id;

-- name: ActivateUser :execrows
UPDATE users
SET status = 'active', updated_at = now()
WHERE id = @id AND status <> 'active';

-- name: DisableUser :exec
UPDATE users
SET status = 'disabled', updated_at = now()
WHERE id = @id;

-- name: ListOtherActiveOwners :many
SELECT m.user_id
FROM memberships AS m
JOIN users AS u ON u.id = m.user_id
WHERE m.tenant_id = @tenant_id AND m.role = 'owner' AND m.user_id <> @user_id AND u.status = 'active'
  -- Only a previous system admin (still flagged until auth_sync_platform_admin runs after
  -- this), never an owner granted through member management.
  AND u.is_system_admin
ORDER BY m.user_id;

-- name: InsertUser :exec
-- Registration: a duplicate email fails on users_email_key, which the caller maps to email-taken.
INSERT INTO users (id, email, password_hash, display_name)
VALUES (@id, @email, @password_hash, @display_name);

-- name: InsertMembership :exec
-- An empty role stores NULL: no organization role.
INSERT INTO memberships (tenant_id, user_id, role)
VALUES (@tenant_id, @user_id, nullif(@role::text, ''));

-- name: TouchMembership :exec
-- The organization a login enters next (auth_login_tenant).
UPDATE memberships
SET last_used_at = @used_at
WHERE tenant_id = @tenant_id AND user_id = @user_id;
