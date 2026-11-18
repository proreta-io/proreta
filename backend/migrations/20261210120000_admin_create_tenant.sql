-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Creates an organization with a chosen plan for a platform admin (the panel's "New
-- organization"). Returns false when the slug is taken. The default slug is reserved. The audit
-- row is written into the new organization. The default project comes from the tenants trigger.
-- +goose StatementBegin
CREATE FUNCTION admin_create_tenant(p_actor uuid, p_id uuid, p_slug text, p_name text, p_plan_key text, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_slug = 'default' THEN
        RAISE EXCEPTION 'admin: the default slug is reserved' USING ERRCODE = 'PRA05';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM plans AS p WHERE p.key = p_plan_key) THEN
        RAISE EXCEPTION 'admin: unknown plan' USING ERRCODE = 'PRA03';
    END IF;
    INSERT INTO tenants (id, slug, name, plan_key)
    VALUES (p_id, p_slug, p_name, p_plan_key)
    ON CONFLICT (slug) DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NULL THEN
        RETURN false;
    END IF;
    PERFORM admin_audit(p_id, p_actor, p_audit_id, 'admin.organization.created', 'organization', p_id::text,
                        jsonb_build_object('slug', p_slug, 'planKey', p_plan_key), p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_create_tenant(uuid, uuid, text, text, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_create_tenant(uuid, uuid, text, text, text, uuid, inet) TO proreta_api;
