-- +goose Up
SET LOCAL lock_timeout = '5s';

-- One-time tokens that prove control of a user's email address. Only the SHA-256 of the 256-bit
-- token is stored (the token travels in the emailed link). A token verifies the address it was
-- issued for: if users.email changed since, it does nothing. used_at is set when the token is
-- consumed or replaced by a newer one, so only the newest link works and a token works once.
-- Users are global, so the table is too (no tenant_id, no RLS); like users, grants are the guard:
-- the api inserts, reads and marks tokens used but never deletes; rows are removed by
-- purge_email_verifications, run by jobs.
CREATE TABLE email_verifications (
    token_hash  bytea       PRIMARY KEY CHECK (length(token_hash) = 32),
    user_id     uuid        NOT NULL REFERENCES users (id),
    email       citext      NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX email_verifications_user_idx ON email_verifications (user_id, created_at DESC);
CREATE INDEX email_verifications_expires_idx ON email_verifications (expires_at);
REVOKE ALL ON email_verifications FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON email_verifications TO proreta_api;
GRANT UPDATE (used_at) ON email_verifications TO proreta_api;

-- Deletes tokens that expired more than a day ago, at most 1000 per call, and returns how many.
-- +goose StatementBegin
CREATE FUNCTION purge_email_verifications()
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH gone AS (
        DELETE FROM email_verifications AS v
        WHERE v.token_hash IN (
            SELECT e.token_hash FROM email_verifications AS e
            WHERE e.expires_at < now() - interval '1 day'
            ORDER BY e.expires_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_email_verifications() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_email_verifications() TO proreta_worker;
