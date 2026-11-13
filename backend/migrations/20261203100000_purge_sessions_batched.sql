-- +goose Up
SET LOCAL lock_timeout = '5s';

-- purge_expired_sessions deleted every expired session in one statement, which on a large backlog
-- outruns the worker's statement_timeout. It now deletes at most 1000 per call, like
-- purge_mail_outbox, and returns how many; jobs repeats it until a call comes back short. The name,
-- signature, return type and grants stay, so a jobs version that calls it once keeps working
-- (it purges 1000 per run). No FOR UPDATE SKIP LOCKED: sessions has no UPDATE policy for the
-- owner, so row locking would make the subquery see nothing; jobs holds an advisory lock anyway.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION purge_expired_sessions()
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH gone AS (
        DELETE FROM sessions AS s
        WHERE s.id IN (
            SELECT e.id FROM sessions AS e
            WHERE e.expires_at < now()
            ORDER BY e.expires_at
            LIMIT 1000
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
