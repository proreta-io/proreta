-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Platform admin: users across all organizations. Same shape as the other admin_* functions: the
-- actor comes first and is checked by admin_require_platform_admin, the audit row is written in
-- the same transaction, and only proreta_api may call them. Errors reuse the PRA codes: PRA01 not
-- a platform admin, PRA02 user not found, PRA04 invalid status, PRA07 a platform admin cannot be
-- disabled.

-- A page of users, newest first, with the number of matches in every row. p_query matches the
-- email or the display name (substring, case-insensitive); an empty p_status matches any status.
-- organization_count counts every membership, deleted organizations included.
-- +goose StatementBegin
CREATE FUNCTION admin_list_all_users(p_actor uuid, p_query text, p_status text, p_limit integer, p_offset integer)
RETURNS TABLE (
    id                   uuid,
    login                text,
    display_name         text,
    status               text,
    is_platform_admin    boolean,
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
    SELECT u.id, u.email::text, u.display_name, u.status, u.is_platform_admin, u.email_verified_at IS NOT NULL,
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

-- One user with the number of open (unexpired) sessions in every tenant.
-- +goose StatementBegin
CREATE FUNCTION admin_get_user(p_actor uuid, p_user_id uuid)
RETURNS TABLE (
    id                   uuid,
    login                text,
    display_name         text,
    status               text,
    is_platform_admin    boolean,
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
    SELECT u.id, u.email::text, u.display_name, u.status, u.is_platform_admin, u.email_verified_at IS NOT NULL,
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

-- Every membership of a user, oldest first. organization_status is active, suspended or deleted.
-- +goose StatementBegin
CREATE FUNCTION admin_list_user_memberships(p_actor uuid, p_user_id uuid)
RETURNS TABLE (
    organization_id     uuid,
    organization_name   text,
    organization_slug   text,
    organization_status text,
    role                text,
    joined_at           timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF NOT EXISTS (SELECT 1 FROM users AS u WHERE u.id = p_user_id) THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    RETURN QUERY
    SELECT t.id, t.name, t.slug, CASE WHEN t.deleted_at IS NOT NULL THEN 'deleted' ELSE t.status END, m.role, m.created_at
    FROM memberships AS m
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.user_id = p_user_id
    ORDER BY m.created_at, t.id;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_list_user_memberships(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_user_memberships(uuid, uuid) TO proreta_api;

-- The tenant that receives the audit row of an action on a user who is not tied to one tenant:
-- the user's last used organization (as the password reset does), else the default tenant.
-- Internal helper, not SECURITY DEFINER.
-- +goose StatementBegin
CREATE FUNCTION admin_user_audit_tenant(p_user_id uuid)
RETURNS uuid
LANGUAGE sql
STABLE
SET search_path = public, pg_temp
AS $$
    SELECT COALESCE(auth_login_tenant(p_user_id), (SELECT t.id FROM tenants AS t WHERE t.slug = 'default'))
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_user_audit_tenant(uuid) FROM PUBLIC;

-- Disables or enables a user. Disabling also deletes all of the user's sessions in every tenant
-- and refuses a platform admin (PRA07). updated_at changes with the status, so a login that
-- verified the password before the change is refused when it creates its session. Setting the
-- status the user already has changes nothing and returns changed = false (no audit row).
-- +goose StatementBegin
CREATE FUNCTION admin_set_user_status(p_actor uuid, p_user_id uuid, p_status text, p_audit_id uuid, p_ip inet)
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
    v_admin  boolean;
    v_tenant uuid;
    v_n      integer := 0;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_status IS NULL OR p_status NOT IN ('active', 'disabled') THEN
        RAISE EXCEPTION 'admin: invalid status' USING ERRCODE = 'PRA04';
    END IF;
    SELECT u.status, u.is_platform_admin INTO v_status, v_admin FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_status = p_status THEN
        RETURN QUERY SELECT false, 0;
        RETURN;
    END IF;
    IF p_status = 'disabled' AND v_admin THEN
        RAISE EXCEPTION 'admin: a platform admin cannot be disabled' USING ERRCODE = 'PRA07';
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
REVOKE ALL ON FUNCTION admin_set_user_status(uuid, uuid, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_user_status(uuid, uuid, text, uuid, inet) TO proreta_api;

-- Signs a user out everywhere: deletes all of the user's sessions in every tenant and returns the
-- number of open ones that were deleted. The audit row is written only when that number is above
-- zero.
-- +goose StatementBegin
CREATE FUNCTION admin_revoke_user_sessions(p_actor uuid, p_user_id uuid, p_audit_id uuid, p_ip inet)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_tenant uuid;
    v_n      integer;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    PERFORM 1 FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    SELECT count(*)::integer INTO v_n FROM sessions AS s WHERE s.user_id = p_user_id AND s.expires_at > now();
    PERFORM auth_revoke_user_sessions(p_user_id);
    IF v_n > 0 THEN
        v_tenant := admin_user_audit_tenant(p_user_id);
        IF v_tenant IS NOT NULL THEN
            PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.user.sessions_revoked', 'user', p_user_id::text,
                                jsonb_build_object('sessionsRevoked', v_n), p_ip);
        END IF;
    END IF;
    RETURN v_n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_revoke_user_sessions(uuid, uuid, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_revoke_user_sessions(uuid, uuid, uuid, inet) TO proreta_api;
