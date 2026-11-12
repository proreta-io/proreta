-- name: GetUserForReset :one
-- The account a reset request names, if it can be reset: active, with a password.
SELECT id, email, status, is_platform_admin, is_system_admin, (password_hash IS NOT NULL)::boolean AS has_password
FROM users
WHERE email = @email;

-- name: CountRecentPasswordResets :one
SELECT count(*)::int AS issued
FROM password_resets
WHERE user_id = @user_id AND created_at > @since;

-- name: SupersedePasswordResets :exec
UPDATE password_resets
SET used_at = @now
WHERE user_id = @user_id AND used_at IS NULL;

-- name: InsertPasswordReset :exec
INSERT INTO password_resets (token_hash, user_id, email, created_at, expires_at)
VALUES (@token_hash, @user_id, @email, @now, @expires_at);

-- name: PeekPasswordReset :one
-- Whether a token is still open, without using it.
SELECT user_id, email
FROM password_resets
WHERE token_hash = @token_hash AND used_at IS NULL AND expires_at > @now;

-- name: ConsumePasswordReset :one
-- Single use: the first caller gets the row, every later one gets none.
UPDATE password_resets
SET used_at = @now
WHERE token_hash = @token_hash AND used_at IS NULL AND expires_at > @now
RETURNING user_id, email;

-- name: LockUserForReset :one
SELECT email, status, is_platform_admin, is_system_admin, (password_hash IS NOT NULL)::boolean AS has_password, email_verified_at
FROM users
WHERE id = @id
FOR UPDATE;
