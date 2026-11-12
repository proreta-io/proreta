-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The claim hands the worker what it needs to send a monitor's request: the method (GET when the
-- monitor has none) and the sealed headers and body, which only a holder of the request keyring
-- can open. The return type gains columns, so the function is dropped and created again with the
-- same body, owner and grants. Monitors written before this change have neither key and keep
-- sending a plain GET.
DROP FUNCTION claim_due_monitors(integer, text);

-- +goose StatementBegin
CREATE FUNCTION claim_due_monitors(n integer, p_location text)
RETURNS TABLE (
    tenant_id          uuid,
    monitor_id         uuid,
    location_id        uuid,
    lease_token        uuid,
    lease_until        timestamptz,
    url                text,
    expected_status    text,
    failure_threshold  integer,
    interval_seconds   integer,
    timeout_seconds    integer,
    next_run_at        timestamptz,
    method             text,
    request            text
)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH due AS (
        SELECT s.monitor_id, s.location_id
        FROM monitor_location_state AS s
        JOIN monitors AS m ON m.tenant_id = s.tenant_id AND m.id = s.monitor_id
        JOIN tenants AS t ON t.id = s.tenant_id
        JOIN probe_locations AS l ON l.id = s.location_id
        WHERE l.code = p_location
          AND l.status = 'active'
          AND t.status = 'active'
          AND t.deleted_at IS NULL
          AND s.next_run_at <= now()
          AND (s.lease_token IS NULL OR s.lease_until <= now())
          AND m.type = 'http'
          AND NOT m.paused
          AND m.deleted_at IS NULL
        ORDER BY s.next_run_at
        LIMIT least(greatest(n, 0), 1000)
        FOR UPDATE OF s SKIP LOCKED
    )
    UPDATE monitor_location_state AS s
    SET lease_token = gen_random_uuid(),
        lease_until = now() + interval '30 seconds'
    FROM due, monitors AS m
    WHERE s.monitor_id = due.monitor_id AND s.location_id = due.location_id
      AND m.tenant_id = s.tenant_id AND m.id = s.monitor_id
    RETURNING s.tenant_id, s.monitor_id, s.location_id, s.lease_token, s.lease_until,
              m.config ->> 'url', COALESCE(m.config ->> 'expectedStatus', '2xx'),
              m.failure_threshold, m.interval_seconds, m.timeout_seconds, s.next_run_at,
              COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config ->> 'request', '')
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_due_monitors(integer, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_due_monitors(integer, text) TO proreta_worker;
