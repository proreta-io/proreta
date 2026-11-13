-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A paused or deleted monitor is parked like a heartbeat: next_run_at is 'infinity', so it is
-- never due and the claim does not wade through it. Resume and a new monitor set a jitter again.
-- Delete now resets the schedule too (the api calls reset_monitor_runtime after the soft delete).
-- The reset also parks a paused monitor that is edited, which used to make it due again.

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
        next_run_at = CASE WHEN m.type IN ('http', 'tcp') AND NOT m.paused AND m.deleted_at IS NULL
                           THEN now() + random() * interval '30 seconds'
                           ELSE 'infinity'::timestamptz END
    FROM monitors AS m
    WHERE m.tenant_id = caller_tenant AND m.id = p_monitor_id
      AND s.tenant_id = m.tenant_id AND s.monitor_id = m.id;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
-- +goose StatementEnd

-- A monitor placed at a location while paused is parked, not due.

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
    mon_paused    boolean;
    loc_kind      text;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'monitor_set_location: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    SELECT project_id, paused INTO mon_project, mon_paused FROM monitors
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
    VALUES (caller_tenant, p_monitor_id, p_location_id,
            CASE WHEN mon_paused THEN 'infinity'::timestamptz ELSE now() + random() * interval '30 seconds' END)
    ON CONFLICT DO NOTHING;
    RETURN 'ok';
END
$$;
-- +goose StatementEnd

-- Rows that exist now.
UPDATE monitor_location_state AS s
SET next_run_at = 'infinity'
FROM monitors AS m
WHERE m.tenant_id = s.tenant_id AND m.id = s.monitor_id
  AND (m.paused OR m.deleted_at IS NOT NULL)
  AND s.next_run_at <> 'infinity';

-- The claim orders by next_run_at over rows that are due and unleased or whose lease expired, which
-- the partial index (lease_token IS NULL) cannot serve; a plain index can, and parked rows
-- sit at its end. The table has one row per monitor and location, so a plain build is short.
DROP INDEX monitor_location_state_due_idx;
CREATE INDEX monitor_location_state_due_idx ON monitor_location_state (next_run_at);
