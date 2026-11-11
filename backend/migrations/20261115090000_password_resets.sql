-- +goose Up
SET LOCAL lock_timeout = '5s';

-- One-time tokens to reset a forgotten password, sent to the account's email address. Only the
-- SHA-256 of the 256-bit token is stored. A token is for the address the account had when it was
-- issued; used_at is set when it is consumed or replaced by a newer one or by a completed reset.
-- Global like users (no tenant_id, no RLS): grants are the guard. The api inserts, reads and marks
-- tokens used but never deletes; purge_password_resets, run by jobs, removes old rows.
CREATE TABLE password_resets (
    token_hash  bytea       PRIMARY KEY CHECK (length(token_hash) = 32),
    user_id     uuid        NOT NULL REFERENCES users (id),
    email       citext      NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX password_resets_user_idx ON password_resets (user_id, created_at DESC);
CREATE INDEX password_resets_expires_idx ON password_resets (expires_at);
REVOKE ALL ON password_resets FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON password_resets TO proreta_api;
GRANT UPDATE (used_at) ON password_resets TO proreta_api;

-- Deletes tokens that expired more than a day ago, at most 1000 per call, and returns how many.
-- +goose StatementBegin
CREATE FUNCTION purge_password_resets()
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH gone AS (
        DELETE FROM password_resets AS r
        WHERE r.token_hash IN (
            SELECT e.token_hash FROM password_resets AS e
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
REVOKE ALL ON FUNCTION purge_password_resets() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_password_resets() TO proreta_worker;
