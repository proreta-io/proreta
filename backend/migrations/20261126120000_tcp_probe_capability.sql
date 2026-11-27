-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A tcp monitor with an expect rule (and so one that may send bytes) is handed only to a Specula
-- that announced the capability tcp/probe. Bodies only; arguments, results, owner and grants are
-- unchanged, so the previous version keeps working during the rollout.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION claim_due_monitors(n integer, p_location text) RETURNS TABLE(tenant_id uuid, monitor_id uuid, location_id uuid, lease_token uuid, lease_until timestamp with time zone, url text, expected_status text, failure_threshold integer, interval_seconds integer, timeout_seconds integer, next_run_at timestamp with time zone, method text, request text, monitor_type text, host text, port integer)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
                  AND NOT ('schedule/1' = ANY (a.capabilities))
                  AND (m.type = 'http' OR ('tcp' = ANY (a.capabilities)
                       AND (NOT (m.config ? 'expect') OR 'tcp/probe' = ANY (a.capabilities))))
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION specula_schedule(p_tenant uuid, p_specula uuid) RETURNS TABLE(monitor_id uuid, epoch integer, anchor bigint, interval_seconds integer, timeout_seconds integer, monitor_type text, url text, expected_status text, method text, request text, host text, port integer)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT e.monitor_id, e.epoch, e.anchor, e.interval_seconds, m.timeout_seconds, m.type,
           COALESCE(m.config ->> 'url', ''), COALESCE(m.config ->> 'expectedStatus', '2xx'),
           COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config ->> 'request', ''),
           COALESCE(m.config ->> 'host', ''), COALESCE((m.config ->> 'port')::integer, 0)
    FROM specula_agents AS a
    JOIN tenants AS t ON t.id = a.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
    JOIN probe_locations AS l ON l.id = a.location_id AND l.status = 'active'
    JOIN monitor_schedule_epochs AS e ON e.tenant_id = a.tenant_id AND e.location_id = a.location_id AND e.active_until IS NULL
    JOIN monitors AS m ON m.tenant_id = e.tenant_id AND m.id = e.monitor_id
    WHERE a.tenant_id = p_tenant AND a.id = p_specula AND a.revoked_at IS NULL
      AND m.type IN ('http', 'tcp') AND NOT m.paused AND m.deleted_at IS NULL
      -- A tcp monitor that sends or expects something goes only to an executor that can run it: an
      -- older one would report a plain connect as a pass.
      AND (m.type = 'http' OR NOT (m.config ? 'expect') OR 'tcp/probe' = ANY (a.capabilities))
      AND (a.all_projects OR EXISTS (
            SELECT 1 FROM specula_projects AS sp WHERE sp.specula_id = a.id AND sp.project_id = m.project_id))
    ORDER BY e.monitor_id
    LIMIT 5001
$$;
-- +goose StatementEnd
