-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The request an http monitor sends. Existing monitors keep sending a plain GET. The body is
-- stored as plain text and is visible to everyone who can read the monitor, so it must never
-- carry secrets. A body needs a method that allows one and a content type, and the other way
-- round; heartbeats send nothing.
ALTER TABLE monitors
    ADD COLUMN method       text NOT NULL DEFAULT 'GET',
    ADD COLUMN request_body text NOT NULL DEFAULT '',
    ADD COLUMN content_type text NOT NULL DEFAULT '',
    ADD CONSTRAINT monitors_method_check CHECK (method IN ('GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE')),
    ADD CONSTRAINT monitors_request_body_size CHECK (octet_length(request_body) <= 16384),
    ADD CONSTRAINT monitors_content_type_check CHECK (
        content_type IN ('', 'application/json', 'text/plain', 'application/x-www-form-urlencoded', 'application/xml')),
    ADD CONSTRAINT monitors_request_body_method CHECK (request_body = '' OR method IN ('POST', 'PUT', 'PATCH', 'DELETE')),
    ADD CONSTRAINT monitors_request_content_type CHECK ((request_body = '') = (content_type = '')),
    ADD CONSTRAINT monitors_request_http_only CHECK (type = 'http' OR (method = 'GET' AND request_body = ''));

-- The claimed job carries the request, so the return type changes.
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
    request_body       text,
    content_type       text
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
              m.method, m.request_body, m.content_type
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_due_monitors(integer, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_due_monitors(integer, text) TO proreta_worker;
