-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Settings of the whole installation that the platform admin changes in the panel, one row.
-- Global like email_settings (no tenant_id, no RLS): nobody reaches the table directly, every
-- read and write goes through the definer functions below.
--
-- registration_enabled NULL means "follow PRORETA_REGISTRATION_ENABLED"; true or false is the
-- admin's choice and wins over the environment.
CREATE TABLE platform_settings (
    id                   boolean     PRIMARY KEY DEFAULT true CHECK (id),
    registration_enabled boolean,
    updated_at           timestamptz,
    updated_by           uuid
);
REVOKE ALL ON platform_settings FROM proreta_api, proreta_worker, proreta_statuspage;
INSERT INTO platform_settings (id) VALUES (true);

-- What the api needs to decide whether self-service registration is open: the admin's choice,
-- NULL when there is none (the environment value applies). Callable without a session.
-- +goose StatementBegin
CREATE FUNCTION platform_registration_override()
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.registration_enabled FROM platform_settings AS s
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION platform_registration_override() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform_registration_override() TO proreta_api;

-- The setting for the admin panel.
-- +goose StatementBegin
CREATE FUNCTION admin_registration_get(p_actor uuid)
RETURNS TABLE (
    registration_enabled boolean,
    updated_at           timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY SELECT s.registration_enabled, s.updated_at FROM platform_settings AS s;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_registration_get(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_registration_get(uuid) TO proreta_api;

-- Sets the admin's choice: true, false, or NULL to follow the environment again. Returns the
-- previous and the new label ('enabled', 'disabled', 'environment') when something changed;
-- no row (and no audit row) when the value is the same. The audit row goes to the admin's own
-- organization, because the setting has no tenant.
-- +goose StatementBegin
CREATE FUNCTION admin_registration_set(p_actor uuid, p_enabled boolean, p_audit_id uuid, p_ip inet)
RETURNS TABLE (
    from_value text,
    to_value   text
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_old    boolean;
    v_from   text;
    v_to     text;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT s.registration_enabled INTO v_old FROM platform_settings AS s WHERE s.id FOR UPDATE;
    IF v_old IS NOT DISTINCT FROM p_enabled THEN
        RETURN;
    END IF;
    UPDATE platform_settings AS s SET registration_enabled = p_enabled, updated_at = now(), updated_by = p_actor WHERE s.id;
    v_from := CASE v_old WHEN true THEN 'enabled' WHEN false THEN 'disabled' ELSE 'environment' END;
    v_to := CASE p_enabled WHEN true THEN 'enabled' WHEN false THEN 'disabled' ELSE 'environment' END;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.registration_settings.updated', 'registration_settings', 'registration',
                            jsonb_build_object('from', v_from, 'to', v_to), p_ip);
    END IF;
    RETURN QUERY SELECT v_from, v_to;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_registration_set(uuid, boolean, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_registration_set(uuid, boolean, uuid, inet) TO proreta_api;
