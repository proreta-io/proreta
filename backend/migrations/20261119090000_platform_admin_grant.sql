-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Platform admin becomes data. is_platform_admin is the flag the panel checks; is_system_admin
-- marks the one admin that PRORETA_ADMIN_LOGIN configures: its credentials come from the Secret,
-- it stays the owner of the default organization only, and the panel can neither revoke nor
-- disable it. Other platform admins are granted on the user page (admin_set_platform_admin).
ALTER TABLE users ADD COLUMN is_system_admin boolean NOT NULL DEFAULT false;
UPDATE users SET is_system_admin = true WHERE is_platform_admin;
ALTER TABLE users ADD CONSTRAINT users_system_admin_is_platform_admin CHECK (is_platform_admin OR NOT is_system_admin);

-- Sets the flags for the configured admin and clears them for the previous system admin only (a
-- retired admin loses the flag with its owner role). Admins granted on the user page are left
-- alone. Returns the number of users changed. It still refuses (PRA01) a user that is not an
-- active owner of the default tenant or that has a membership anywhere else.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION auth_sync_platform_admin(p_user_id uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    n integer;
    m integer;
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM users AS u
        JOIN memberships AS m ON m.user_id = u.id
        JOIN tenants AS t ON t.id = m.tenant_id
        WHERE u.id = p_user_id AND u.status = 'active' AND m.role = 'owner'
          AND t.slug = 'default' AND t.deleted_at IS NULL
    ) OR EXISTS (
        SELECT 1
        FROM memberships AS m
        JOIN tenants AS t ON t.id = m.tenant_id
        WHERE m.user_id = p_user_id AND t.slug <> 'default'
    ) THEN
        RAISE EXCEPTION 'auth_sync_platform_admin: user is not the owner of the default tenant only'
            USING ERRCODE = 'PRA01';
    END IF;
    UPDATE users AS u
    SET is_platform_admin = false, is_system_admin = false, updated_at = now()
    WHERE u.is_system_admin AND u.id <> p_user_id;
    GET DIAGNOSTICS n = ROW_COUNT;
    UPDATE users AS u
    SET is_platform_admin = true, is_system_admin = true, updated_at = now()
    WHERE u.id = p_user_id AND NOT (u.is_platform_admin AND u.is_system_admin);
    GET DIAGNOSTICS m = ROW_COUNT;
    RETURN n + m;
END
$$;
-- +goose StatementEnd

-- The user functions of the panel now also report is_system_admin.
DROP FUNCTION admin_list_all_users(uuid, text, text, integer, integer);
DROP FUNCTION admin_get_user(uuid, uuid);

-- +goose StatementBegin
CREATE FUNCTION admin_list_all_users(p_actor uuid, p_query text, p_status text, p_limit integer, p_offset integer)
RETURNS TABLE (
    id                   uuid,
    login                text,
    display_name         text,
    status               text,
    is_platform_admin    boolean,
    is_system_admin      boolean,
    email_verified       boolean,
    must_change_password boolean,
    created_at           timestamptz,
    organization_count   integer,
    total_count          integer
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    q  text := lower(btrim(COALESCE(p_query, '')));
    st text := COALESCE(p_status, '');
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF st NOT IN ('', 'active', 'disabled') THEN
        RAISE EXCEPTION 'admin: invalid status' USING ERRCODE = 'PRA04';
    END IF;
    RETURN QUERY
    SELECT u.id, u.email::text, u.display_name, u.status, u.is_platform_admin, u.is_system_admin, u.email_verified_at IS NOT NULL,
           u.password_must_change, u.created_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.user_id = u.id),
           (count(*) OVER ())::integer
    FROM users AS u
    WHERE (st = '' OR u.status = st)
      AND (q = '' OR strpos(lower(u.email::text), q) > 0 OR strpos(lower(u.display_name), q) > 0)
    ORDER BY u.created_at DESC, u.id
    LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
    OFFSET greatest(COALESCE(p_offset, 0), 0);
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_list_all_users(uuid, text, text, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_all_users(uuid, text, text, integer, integer) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_get_user(p_actor uuid, p_user_id uuid)
RETURNS TABLE (
    id                   uuid,
    login                text,
    display_name         text,
    status               text,
    is_platform_admin    boolean,
    is_system_admin      boolean,
    email_verified       boolean,
    must_change_password boolean,
    created_at           timestamptz,
    organization_count   integer,
    open_sessions        integer
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
    SELECT u.id, u.email::text, u.display_name, u.status, u.is_platform_admin, u.is_system_admin, u.email_verified_at IS NOT NULL,
           u.password_must_change, u.created_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.user_id = u.id),
           (SELECT count(*)::integer FROM sessions AS s WHERE s.user_id = u.id AND s.expires_at > now())
    FROM users AS u
    WHERE u.id = p_user_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_get_user(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_get_user(uuid, uuid) TO proreta_api;

-- Only the system admin is protected from being disabled; a granted admin can be disabled like
-- any user (it then loses admin access with its sessions). Otherwise unchanged.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_set_user_status(p_actor uuid, p_user_id uuid, p_status text, p_audit_id uuid, p_ip inet)
RETURNS TABLE (
    changed          boolean,
    sessions_revoked integer
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_status text;
    v_system boolean;
    v_tenant uuid;
    v_n      integer := 0;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_status IS NULL OR p_status NOT IN ('active', 'disabled') THEN
        RAISE EXCEPTION 'admin: invalid status' USING ERRCODE = 'PRA04';
    END IF;
    SELECT u.status, u.is_system_admin INTO v_status, v_system FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_status = p_status THEN
        RETURN QUERY SELECT false, 0;
        RETURN;
    END IF;
    IF p_status = 'disabled' AND v_system THEN
        RAISE EXCEPTION 'admin: the system admin cannot be disabled' USING ERRCODE = 'PRA07';
    END IF;
    UPDATE users SET status = p_status, updated_at = now() WHERE id = p_user_id;
    IF p_status = 'disabled' THEN
        SELECT count(*)::integer INTO v_n FROM sessions AS s WHERE s.user_id = p_user_id AND s.expires_at > now();
        PERFORM auth_revoke_user_sessions(p_user_id);
    END IF;
    v_tenant := admin_user_audit_tenant(p_user_id);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id,
                            CASE p_status WHEN 'disabled' THEN 'admin.user.disabled' ELSE 'admin.user.enabled' END,
                            'user', p_user_id::text,
                            CASE p_status WHEN 'disabled' THEN jsonb_build_object('sessionsRevoked', v_n) ELSE '{}'::jsonb END, p_ip);
    END IF;
    RETURN QUERY SELECT true, v_n;
END
$$;
-- +goose StatementEnd

-- Grants or revokes the platform-admin flag. PRA02 user not found; PRA04 grant not set; PRA07 the
-- system admin cannot be revoked; PRA14 the last active platform admin cannot be revoked; PRA15
-- only an active user can be granted. Granting an admin or revoking a non-admin changes nothing
-- and returns changed = false (no audit row). Sessions stay: the flag is read on every request.
-- An advisory lock serialises grants and revocations, so two admins revoking each other cannot
-- both pass the last-admin check.
-- +goose StatementBegin
CREATE FUNCTION admin_set_platform_admin(p_actor uuid, p_user_id uuid, p_grant boolean, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_status text;
    v_admin  boolean;
    v_system boolean;
    v_tenant uuid;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('proreta.platform_admin'));
    PERFORM admin_require_platform_admin(p_actor);
    IF p_grant IS NULL THEN
        RAISE EXCEPTION 'admin: grant must be set' USING ERRCODE = 'PRA04';
    END IF;
    SELECT u.status, u.is_platform_admin, u.is_system_admin INTO v_status, v_admin, v_system
    FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_admin = p_grant THEN
        RETURN false;
    END IF;
    IF p_grant THEN
        IF v_status <> 'active' THEN
            RAISE EXCEPTION 'admin: only an active user can be a platform admin' USING ERRCODE = 'PRA15';
        END IF;
    ELSE
        IF v_system THEN
            RAISE EXCEPTION 'admin: the system admin cannot be revoked' USING ERRCODE = 'PRA07';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM users AS u WHERE u.is_platform_admin AND u.status = 'active' AND u.id <> p_user_id) THEN
            RAISE EXCEPTION 'admin: the last platform admin cannot be revoked' USING ERRCODE = 'PRA14';
        END IF;
    END IF;
    UPDATE users SET is_platform_admin = p_grant, updated_at = now() WHERE id = p_user_id;
    v_tenant := admin_user_audit_tenant(p_user_id);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id,
                            CASE WHEN p_grant THEN 'admin.user.platform_admin_granted' ELSE 'admin.user.platform_admin_revoked' END,
                            'user', p_user_id::text, '{}'::jsonb, p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_set_platform_admin(uuid, uuid, boolean, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_platform_admin(uuid, uuid, boolean, uuid, inet) TO proreta_api;
