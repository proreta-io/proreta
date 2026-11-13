-- +goose Up
SET LOCAL lock_timeout = '5s';

-- How long finished notification deliveries and hourly rollups are kept. NULL means the built-in
-- default (30 days, 13 months, applied by the jobs worker); a number is the platform admin's
-- choice. The bounds are enforced by admin_retention_set and by the purge functions, which are
-- the only writers.
ALTER TABLE platform_settings
    ADD COLUMN delivery_retention_days  integer,
    ADD COLUMN rollup_retention_months  integer,
    ADD COLUMN retention_updated_at     timestamptz;

-- The admin's choices for the jobs worker, NULL where there is none. Callable without a session.
-- +goose StatementBegin
CREATE FUNCTION platform_retention_overrides()
RETURNS TABLE (
    delivery_days integer,
    rollup_months integer
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.delivery_retention_days, s.rollup_retention_months FROM platform_settings AS s
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION platform_retention_overrides() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform_retention_overrides() TO proreta_worker;

-- The settings for the admin panel.
-- +goose StatementBegin
CREATE FUNCTION admin_retention_get(p_actor uuid)
RETURNS TABLE (
    delivery_days integer,
    rollup_months integer,
    updated_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY SELECT s.delivery_retention_days, s.rollup_retention_months, s.retention_updated_at FROM platform_settings AS s;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_retention_get(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_retention_get(uuid) TO proreta_api;

-- Sets both retentions (NULL = the default again). Deliveries: 7 to 365 days. Rollups: 4 to 60
-- months; at least 4 because the status page shows 90 days, at most 5 years. PRA16 / PRA17 for a
-- value out of range. Returns true, and writes one audit row to the admin's own organization
-- (the settings have no tenant), only when something changed.
-- +goose StatementBegin
CREATE FUNCTION admin_retention_set(p_actor uuid, p_delivery_days integer, p_rollup_months integer, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_old_days   integer;
    v_old_months integer;
    v_tenant     uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_delivery_days IS NOT NULL AND p_delivery_days NOT BETWEEN 7 AND 365 THEN
        RAISE EXCEPTION 'admin: delivery retention out of range' USING ERRCODE = 'PRA16';
    END IF;
    IF p_rollup_months IS NOT NULL AND p_rollup_months NOT BETWEEN 4 AND 60 THEN
        RAISE EXCEPTION 'admin: rollup retention out of range' USING ERRCODE = 'PRA17';
    END IF;
    SELECT s.delivery_retention_days, s.rollup_retention_months INTO v_old_days, v_old_months
    FROM platform_settings AS s WHERE s.id FOR UPDATE;
    IF v_old_days IS NOT DISTINCT FROM p_delivery_days AND v_old_months IS NOT DISTINCT FROM p_rollup_months THEN
        RETURN false;
    END IF;
    UPDATE platform_settings AS s
    SET delivery_retention_days = p_delivery_days, rollup_retention_months = p_rollup_months,
        retention_updated_at = now(), updated_by = p_actor
    WHERE s.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.retention_settings.updated', 'retention_settings', 'retention',
                            jsonb_build_object('deliveryDays', p_delivery_days, 'rollupMonths', p_rollup_months,
                                               'previousDeliveryDays', v_old_days, 'previousRollupMonths', v_old_months), p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_retention_set(uuid, integer, integer, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_retention_set(uuid, integer, integer, uuid, inet) TO proreta_api;

-- The purge runs as proreta_owner (definer) and needs to delete across tenants.
CREATE POLICY owner_delivery_purge ON notification_deliveries FOR DELETE TO proreta_owner USING (true);

-- Deletes up to 1000 finished deliveries (sent or failed, never pending or in progress) created
-- more than p_days ago, and returns how many; the worker calls it until a batch comes back short.
-- created_at is the age: a delivery is finished within hours of it, far inside the 7-day minimum.
-- +goose StatementBegin
CREATE FUNCTION purge_notification_deliveries(p_days integer)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_n integer;
BEGIN
    IF p_days IS NULL OR p_days < 7 THEN
        RAISE EXCEPTION 'purge: delivery retention below the minimum' USING ERRCODE = '22023';
    END IF;
    WITH gone AS (
        DELETE FROM notification_deliveries AS d
        WHERE d.id IN (
            SELECT e.id FROM notification_deliveries AS e
            WHERE e.status IN ('sent', 'failed') AND e.created_at < now() - make_interval(days => p_days)
            ORDER BY e.created_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer INTO v_n FROM gone;
    RETURN v_n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_notification_deliveries(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_notification_deliveries(integer) TO proreta_worker;

-- Deletes up to 1000 hourly rollups older than p_months months, oldest first, and returns how
-- many. (monitor_id, location_id, hour) is the key, so the batch is picked by ctid.
-- +goose StatementBegin
CREATE FUNCTION purge_result_rollups(p_months integer)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_n integer;
BEGIN
    IF p_months IS NULL OR p_months < 4 THEN
        RAISE EXCEPTION 'purge: rollup retention below the minimum' USING ERRCODE = '22023';
    END IF;
    WITH gone AS (
        DELETE FROM result_rollups_hourly AS r
        WHERE r.ctid IN (
            SELECT e.ctid FROM result_rollups_hourly AS e
            WHERE e.hour < now() - make_interval(months => p_months)
            ORDER BY e.hour
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer INTO v_n FROM gone;
    RETURN v_n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_result_rollups(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_result_rollups(integer) TO proreta_worker;
