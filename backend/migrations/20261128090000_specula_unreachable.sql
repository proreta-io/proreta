-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A Specula that is revoked, or has not been heard from for p_after, cannot say whether the
-- services behind it are up, so the monitors that run there show "unknown" instead of keeping a
-- stale "up" or "down". Their schedule rows are marked the same way, so the incident state
-- machine starts from unknown when the Specula is back. Open incidents are left alone: they
-- resolve when a check passes again. Returns the number of monitors changed. A Specula that never
-- connected has nothing to flag. Cross-tenant, run by the jobs process every minute.
-- +goose StatementBegin
CREATE FUNCTION specula_mark_unreachable(p_after interval DEFAULT interval '3 minutes')
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH gone AS (
        SELECT a.location_id
        FROM specula_agents AS a
        WHERE a.revoked_at IS NOT NULL OR a.last_seen_at < now() - p_after
    ), flagged AS (
        UPDATE monitor_location_state AS s
        SET status = 'unknown', consecutive_failures = 0
        FROM gone
        WHERE s.location_id = gone.location_id AND s.status <> 'unknown'
        RETURNING s.tenant_id, s.monitor_id
    ), changed AS (
        UPDATE monitor_state AS m
        SET status = 'unknown', status_changed_at = now()
        FROM flagged
        WHERE m.tenant_id = flagged.tenant_id AND m.monitor_id = flagged.monitor_id AND m.status <> 'unknown'
        RETURNING 1
    )
    SELECT count(*)::integer FROM changed
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_mark_unreachable(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_mark_unreachable(interval) TO proreta_worker;

CREATE POLICY owner_specula_unreachable_read ON monitor_state FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_specula_unreachable_write ON monitor_state FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
