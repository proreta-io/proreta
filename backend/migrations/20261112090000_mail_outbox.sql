-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The outgoing email queue. The api inserts a row in the transaction of the state change that
-- sends the message (a registration, an invitation), and jobs delivers it later through
-- claim_mail / finish_mail, with retries. Like notification_deliveries, a row is claimed with a
-- lease and a claim token that fences a jobs process whose lease expired.
--
-- The table is global (no tenant_id, no RLS): a message goes to a person, not to a tenant, and
-- its content is sealed. payload is the AES-256-GCM envelope of the recipient and the template
-- parameters (organization name, the one-time token), bound to the row id; it is emptied as soon
-- as the row is sent or failed, so a finished row holds no address and no token. Grants are the
-- guard: the api may only insert, the worker role (jobs) reaches the table through the definer
-- functions below. expires_at is when the message stops being worth sending (the lifetime of its
-- token); a row still queued then is failed as 'expired' instead of being delivered late.
CREATE TABLE mail_outbox (
    id               uuid        PRIMARY KEY,
    kind             text        NOT NULL CHECK (kind ~ '^[a-z][a-z_]{0,39}$'),
    payload          bytea       NOT NULL CHECK (octet_length(payload) <= 8192),
    status           text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'sent', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    lease_until      timestamptz,
    claim_token      uuid,
    last_error       text        CHECK (length(last_error) <= 500),
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    finished_at      timestamptz,
    CHECK (expires_at > created_at),
    CHECK ((status = 'in_progress') = (lease_until IS NOT NULL)),
    CHECK ((status = 'in_progress') = (claim_token IS NOT NULL)),
    CHECK ((status IN ('sent', 'failed')) = (finished_at IS NOT NULL)),
    CHECK ((status IN ('pending', 'in_progress')) = (octet_length(payload) > 0))
);
CREATE INDEX mail_outbox_ready_idx ON mail_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX mail_outbox_lease_idx ON mail_outbox (lease_until) WHERE status = 'in_progress';
CREATE INDEX mail_outbox_finished_idx ON mail_outbox (finished_at) WHERE finished_at IS NOT NULL;
REVOKE ALL ON mail_outbox FROM proreta_api, proreta_worker;
GRANT INSERT ON mail_outbox TO proreta_api;

-- Claims up to n messages, oldest first.
-- 1. Rows left in progress by a dead jobs process return to pending when their 2-minute lease
--    expires. The requeue counts as an attempt, so a message that kills every jobs process ends as
--    failed instead of being claimed forever; 5 is jobs.MailMaxAttempts, and the jobs integration
--    test fails if they drift apart.
-- 2. Pending messages past expires_at are failed ('expired') and their payload is emptied.
-- 3. Ready rows are marked in_progress with a new lease and claim token.
-- +goose StatementBegin
CREATE FUNCTION claim_mail(n integer)
RETURNS TABLE (
    id           uuid,
    kind         text,
    payload      bytea,
    attempts     integer,
    claim_token  uuid
)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    UPDATE mail_outbox AS m
    SET status = CASE WHEN m.attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END,
        attempts = m.attempts + 1,
        last_error = 'lease_expired',
        lease_until = NULL,
        claim_token = NULL,
        finished_at = CASE WHEN m.attempts + 1 >= 5 THEN now() END,
        payload = CASE WHEN m.attempts + 1 >= 5 THEN ''::bytea ELSE m.payload END
    WHERE m.id IN (
        SELECT e.id FROM mail_outbox AS e
        WHERE e.status = 'in_progress' AND e.lease_until <= now()
        FOR UPDATE SKIP LOCKED
    );

    UPDATE mail_outbox AS m
    SET status = 'failed', last_error = 'expired', finished_at = now(), payload = ''::bytea
    WHERE m.id IN (
        SELECT e.id FROM mail_outbox AS e
        WHERE e.status = 'pending' AND e.expires_at <= now()
        FOR UPDATE SKIP LOCKED
    );

    WITH ready AS (
        SELECT e.id
        FROM mail_outbox AS e
        WHERE e.status = 'pending' AND e.next_attempt_at <= now()
        ORDER BY e.created_at, e.id
        LIMIT least(greatest(n, 0), 100)
        FOR UPDATE SKIP LOCKED
    )
    UPDATE mail_outbox AS m
    SET status = 'in_progress', lease_until = now() + interval '2 minutes', claim_token = gen_random_uuid()
    FROM ready
    WHERE m.id = ready.id
    RETURNING m.id, m.kind, m.payload, m.attempts, m.claim_token
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_mail(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_mail(integer) TO proreta_worker;

-- Records the outcome of a claimed message: 'sent', 'failed' (permanent) or 'pending' (retry at
-- retry_at). Counts the attempt, ends the lease and, for sent and failed, empties the payload.
-- Returns false when the row is not in progress under p_token.
-- +goose StatementBegin
CREATE FUNCTION finish_mail(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamptz)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending') THEN
        RAISE EXCEPTION 'finish_mail: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status = 'pending' AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_mail: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE mail_outbox AS m
    SET status = p_status,
        attempts = m.attempts + 1,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        finished_at = CASE WHEN p_status = 'pending' THEN NULL ELSE now() END,
        payload = CASE WHEN p_status = 'pending' THEN m.payload ELSE ''::bytea END,
        next_attempt_at = CASE WHEN p_status = 'pending' THEN p_retry_at ELSE m.next_attempt_at END
    WHERE m.id = p_id AND m.status = 'in_progress' AND m.claim_token = p_token;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION finish_mail(uuid, uuid, text, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION finish_mail(uuid, uuid, text, text, timestamptz) TO proreta_worker;

-- Backlog gauge of jobs (proreta_jobs_mail_pending): a number only.
-- +goose StatementBegin
CREATE FUNCTION mail_pending()
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT count(*) FROM mail_outbox WHERE status = 'pending'
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION mail_pending() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mail_pending() TO proreta_worker;

-- Deletes finished rows (sent or failed, already emptied) older than p_keep, at most 1000 per
-- call, and returns how many.
-- +goose StatementBegin
CREATE FUNCTION purge_mail_outbox(p_keep interval)
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH gone AS (
        DELETE FROM mail_outbox AS m
        WHERE m.id IN (
            SELECT e.id FROM mail_outbox AS e
            WHERE e.finished_at IS NOT NULL AND e.finished_at < now() - p_keep
            ORDER BY e.finished_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_mail_outbox(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_mail_outbox(interval) TO proreta_worker;
