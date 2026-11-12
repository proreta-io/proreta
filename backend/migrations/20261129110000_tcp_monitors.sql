-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A new monitor type, tcp: connect to a host and a port, up when the TCP handshake completes. Its
-- target is config {host, port}; the interval and timeout are fixed like an http monitor's.
ALTER TABLE monitors DROP CONSTRAINT monitors_type_check;
ALTER TABLE monitors ADD CONSTRAINT monitors_type_check CHECK (type IN ('http', 'tcp', 'heartbeat'));
ALTER TABLE monitors DROP CONSTRAINT monitors_mvp_interval;
ALTER TABLE monitors ADD CONSTRAINT monitors_mvp_interval CHECK (type = 'heartbeat' OR interval_seconds = 60);
ALTER TABLE monitors DROP CONSTRAINT monitors_mvp_timeout;
ALTER TABLE monitors ADD CONSTRAINT monitors_mvp_timeout CHECK (type = 'heartbeat' OR timeout_seconds = 10);
-- CASE guards the casts: SQL does not promise to evaluate AND operands left to right.
ALTER TABLE monitors ADD CONSTRAINT monitors_tcp_check CHECK (
    type <> 'tcp' OR (
        COALESCE(jsonb_typeof(config -> 'host'), '') = 'string' -- NULL would pass a CHECK
        AND CASE WHEN jsonb_typeof(config -> 'port') = 'number' AND (config ->> 'port') ~ '^[0-9]+$'
                 THEN (config ->> 'port')::numeric BETWEEN 1 AND 65535
                 ELSE false END));

-- The claim hands out tcp monitors too, with their type, host and port (url and the request are
-- empty for them). The return type gains columns, so the function is dropped and created again
-- with the same body, owner and grants.
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
    request            text,
    monitor_type       text,
    host               text,
    port               integer
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
          AND m.type IN ('http', 'tcp')
          AND NOT m.paused
          AND m.deleted_at IS NULL
          AND (l.kind = 'platform' OR EXISTS (
                SELECT 1 FROM specula_agents AS a
                WHERE a.location_id = l.id AND a.tenant_id = s.tenant_id AND a.revoked_at IS NULL
                  AND (a.all_projects OR EXISTS (
                        SELECT 1 FROM specula_projects AS sp
                        WHERE sp.specula_id = a.id AND sp.project_id = m.project_id))))
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
              COALESCE(m.config ->> 'url', ''), COALESCE(m.config ->> 'expectedStatus', '2xx'),
              m.failure_threshold, m.interval_seconds, m.timeout_seconds, s.next_run_at,
              COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config ->> 'request', ''),
              m.type, COALESCE(m.config ->> 'host', ''), COALESCE((m.config ->> 'port')::integer, 0)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_due_monitors(integer, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_due_monitors(integer, text) TO proreta_worker;

-- A tcp monitor is placed at a location like an http one. Its body is unchanged except the type.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION monitor_set_location(p_monitor_id uuid, p_location_id uuid)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    mon_project   uuid;
    loc_kind      text;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'monitor_set_location: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    SELECT project_id INTO mon_project FROM monitors
    WHERE tenant_id = caller_tenant AND id = p_monitor_id AND deleted_at IS NULL AND type IN ('http', 'tcp');
    IF mon_project IS NULL THEN
        RETURN 'no_monitor';
    END IF;
    SELECT kind INTO loc_kind FROM probe_locations WHERE id = p_location_id AND status = 'active';
    IF loc_kind IS NULL THEN
        RETURN 'bad_location';
    END IF;
    IF loc_kind = 'agent' AND NOT EXISTS (
        SELECT 1 FROM specula_agents AS a
        WHERE a.tenant_id = caller_tenant AND a.location_id = p_location_id AND a.revoked_at IS NULL
          AND (a.all_projects OR EXISTS (
                SELECT 1 FROM specula_projects AS sp WHERE sp.specula_id = a.id AND sp.project_id = mon_project)))
    THEN
        RETURN 'bad_location';
    END IF;
    DELETE FROM monitor_location_state
    WHERE tenant_id = caller_tenant AND monitor_id = p_monitor_id AND location_id <> p_location_id;
    INSERT INTO monitor_locations (tenant_id, monitor_id, location_id)
    VALUES (caller_tenant, p_monitor_id, p_location_id) ON CONFLICT DO NOTHING;
    INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at)
    VALUES (caller_tenant, p_monitor_id, p_location_id, now() + random() * interval '30 seconds')
    ON CONFLICT DO NOTHING;
    RETURN 'ok';
END
$$;
-- +goose StatementEnd

-- A resumed or restarted tcp monitor is due again after a jitter, like an http one; the quota and
-- the admin counts treat it as a monitor (a heartbeat has its own quota).

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reset_monitor_runtime(p_monitor_id uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    n             integer;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'reset_monitor_runtime: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    UPDATE monitor_location_state AS s
    SET status = 'unknown',
        consecutive_failures = 0,
        lease_token = NULL,
        lease_until = NULL,
        next_run_at = CASE WHEN m.type IN ('http', 'tcp') THEN now() + random() * interval '30 seconds'
                           ELSE 'infinity'::timestamptz END
    FROM monitors AS m
    WHERE m.tenant_id = caller_tenant AND m.id = p_monitor_id AND m.deleted_at IS NULL
      AND s.tenant_id = m.tenant_id AND s.monitor_id = m.id;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_list_tenants(p_actor uuid, p_query text, p_include_deleted boolean, p_limit integer, p_offset integer)
RETURNS TABLE (
    id              uuid,
    slug            text,
    name            text,
    plan_key        text,
    plan_name       text,
    status          text,
    created_at      timestamptz,
    deleted_at      timestamptz,
    user_count      integer,
    monitor_count   integer,
    heartbeat_count integer,
    total_count     integer
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    q text := lower(btrim(COALESCE(p_query, '')));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    WITH matched AS (
        SELECT t.id, t.slug, t.name, t.plan_key, t.status, t.created_at, t.deleted_at
        FROM tenants AS t
        WHERE (p_include_deleted OR t.deleted_at IS NULL)
          AND (q = ''
               OR strpos(lower(t.slug), q) > 0
               OR strpos(lower(t.name), q) > 0
               OR t.id::text = q
               OR EXISTS (SELECT 1 FROM memberships AS m JOIN users AS u ON u.id = m.user_id
                          WHERE m.tenant_id = t.id AND strpos(lower(u.email::text), q) > 0))
    ), page AS (
        SELECT mt.*, count(*) OVER () AS total
        FROM matched AS mt
        ORDER BY mt.created_at DESC, mt.id DESC
        LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
        OFFSET greatest(COALESCE(p_offset, 0), 0)
    )
    SELECT p.id, p.slug, p.name, p.plan_key, pl.name, p.status, p.created_at, p.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = p.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type <> 'heartbeat' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL),
           p.total::integer
    FROM page AS p
    JOIN plans AS pl ON pl.key = p.plan_key
    ORDER BY p.created_at DESC, p.id DESC;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_get_tenant(p_actor uuid, p_tenant_id uuid)
RETURNS TABLE (
    id              uuid,
    slug            text,
    name            text,
    plan_key        text,
    plan_name       text,
    status          text,
    created_at      timestamptz,
    deleted_at      timestamptz,
    user_count      integer,
    monitor_count   integer,
    heartbeat_count integer
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    SELECT t.id, t.slug, t.name, t.plan_key, pl.name, t.status, t.created_at, t.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = t.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type <> 'heartbeat' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL)
    FROM tenants AS t
    JOIN plans AS pl ON pl.key = t.plan_key
    WHERE t.id = p_tenant_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_stats(p_actor uuid)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN jsonb_build_object(
        'tenants', (SELECT jsonb_build_object(
                        'total', count(*) FILTER (WHERE t.deleted_at IS NULL),
                        'active', count(*) FILTER (WHERE t.deleted_at IS NULL AND t.status = 'active'),
                        'suspended', count(*) FILTER (WHERE t.deleted_at IS NULL AND t.status = 'suspended'),
                        'deleted', count(*) FILTER (WHERE t.deleted_at IS NOT NULL))
                    FROM tenants AS t),
        'users', (SELECT count(*) FROM users AS u WHERE u.status = 'active'),
        'monitors', (SELECT count(*) FROM monitors AS m JOIN tenants AS t ON t.id = m.tenant_id
                     WHERE m.type <> 'heartbeat' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
        'heartbeats', (SELECT count(*) FROM monitors AS m JOIN tenants AS t ON t.id = m.tenant_id
                       WHERE m.type = 'heartbeat' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
        'plans', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                         'key', p.key, 'name', p.name,
                         'tenants', (SELECT count(*) FROM tenants AS t WHERE t.plan_key = p.key AND t.deleted_at IS NULL))
                      ORDER BY p.key), '[]'::jsonb)
                  FROM plans AS p)
    );
END
$$;
-- +goose StatementEnd
