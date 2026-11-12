-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A monitor can move between Proreta's own worker and a Specula of its organization. An open
-- incident ends with its own reason when the location changes.
ALTER TABLE incidents DROP CONSTRAINT incidents_resolve_reason_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_resolve_reason_check
    CHECK (resolve_reason IN ('recovered', 'paused', 'deleted', 'url_changed', 'location_changed'));

-- Runs a live monitor of the caller's tenant at exactly one location: the platform worker's
-- location, or the location of a Specula of the tenant that is not revoked and is shared with the
-- monitor's project. The schedule rows of other locations are removed (their assignment rows stay:
-- check results refer to them). The api cannot write monitor_locations or delete schedules itself.
-- Returns 'ok', 'no_monitor' or 'bad_location'.
-- +goose StatementBegin
CREATE FUNCTION monitor_set_location(p_monitor_id uuid, p_location_id uuid)
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
    WHERE tenant_id = caller_tenant AND id = p_monitor_id AND deleted_at IS NULL AND type = 'http';
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
REVOKE ALL ON FUNCTION monitor_set_location(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION monitor_set_location(uuid, uuid) TO proreta_api;
