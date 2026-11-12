-- name: SupersedeEmailVerifications :exec
-- A new link replaces the open ones of the user.
UPDATE email_verifications
SET used_at = @now
WHERE user_id = @user_id AND used_at IS NULL;

-- name: InsertEmailVerification :exec
INSERT INTO email_verifications (token_hash, user_id, email, created_at, expires_at)
VALUES (@token_hash, @user_id, @email, @now, @expires_at);

-- name: ConsumeEmailVerification :one
-- Single use: the first caller gets the row, every later one gets none.
UPDATE email_verifications
SET used_at = @now
WHERE token_hash = @token_hash AND used_at IS NULL AND expires_at > @now
RETURNING user_id, email;

-- name: MarkEmailVerifiedFor :execrows
-- Only the address the token was issued for counts.
UPDATE users
SET email_verified_at = @now, updated_at = @now
WHERE id = @id AND email = @email AND email_verified_at IS NULL;

-- name: LockUserForVerification :one
-- Serialises resends and the consumption of a link for one user.
SELECT email, email_verified_at, status, is_platform_admin, is_system_admin
FROM users
WHERE id = @id
FOR UPDATE;

-- name: CountRecentEmailVerifications :one
-- Links issued to the user since @since, and the newest time.
SELECT count(*)::int AS issued, max(created_at)::timestamptz AS newest
FROM email_verifications
WHERE user_id = @user_id AND created_at > @since;
