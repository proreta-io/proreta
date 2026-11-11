-- +goose Up
SET LOCAL lock_timeout = '5s';

-- password_must_change: the password was issued by someone else (the generated Secret of the
-- platform admin, a temporary password from the admin panel) and the user has to replace it
-- before using the app. NOT NULL with a constant default is a metadata-only change; the previous
-- app version neither reads nor writes the column, and the new function below sets it for
-- both versions.
ALTER TABLE users ADD COLUMN password_must_change boolean NOT NULL DEFAULT false;
GRANT INSERT (password_must_change), UPDATE (password_must_change) ON users TO proreta_api;

-- The panel's password reset marks the temporary password. Same body as before plus the flag;
-- CREATE OR REPLACE keeps the grants.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_reset_user_password(p_actor uuid, p_user_id uuid, p_password_hash text, p_audit_id uuid, p_ip inet)
RETURNS TABLE (
    tenant_id        uuid,
    sessions_revoked integer
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_admin  boolean;
    v_tenant uuid;
    v_n      integer;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_password_hash IS NULL OR p_password_hash NOT LIKE '$argon2id$%' THEN
        RAISE EXCEPTION 'admin: invalid password hash' USING ERRCODE = 'PRA04';
    END IF;
    SELECT u.is_platform_admin INTO v_admin FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_admin THEN
        RAISE EXCEPTION 'admin: a platform admin password is managed by its Secret' USING ERRCODE = 'PRA07';
    END IF;
    v_tenant := auth_login_tenant(p_user_id);
    IF v_tenant IS NULL THEN
        RAISE EXCEPTION 'admin: user has no membership' USING ERRCODE = 'PRA02';
    END IF;
    -- updated_at changes with the hash: a login verified against the old hash is refused when
    -- it creates its session. The user must replace the temporary password at the next sign-in.
    UPDATE users SET password_hash = p_password_hash, password_must_change = true, updated_at = now() WHERE id = p_user_id;
    v_n := auth_revoke_user_sessions(p_user_id);
    PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.user.password_reset', 'user', p_user_id::text,
                        jsonb_build_object('sessionsRevoked', v_n), p_ip);
    RETURN QUERY SELECT v_tenant, v_n;
END
$$;
-- +goose StatementEnd
