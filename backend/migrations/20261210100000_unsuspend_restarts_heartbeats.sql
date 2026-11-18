-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Unsuspending a tenant restarts the wait of its heartbeat monitors. Same function otherwise;
-- the grants are unchanged.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_set_tenant_status(p_actor uuid, p_tenant_id uuid, p_status text, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_old     text;
    v_slug    text;
    v_deleted timestamptz;
    v_expired integer;
    v_meta    jsonb;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_status IS NULL OR p_status NOT IN ('active', 'suspended') THEN
        RAISE EXCEPTION 'admin: invalid status' USING ERRCODE = 'PRA04';
    END IF;
    SELECT t.status, t.slug, t.deleted_at INTO v_old, v_slug, v_deleted FROM tenants AS t WHERE t.id = p_tenant_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RAISE EXCEPTION 'admin: tenant is deleted' USING ERRCODE = 'PRA08';
    END IF;
    IF p_status = 'suspended' THEN
        IF v_slug = 'default' THEN
            RAISE EXCEPTION 'admin: the default tenant cannot be suspended' USING ERRCODE = 'PRA05';
        END IF;
        IF EXISTS (SELECT 1 FROM memberships AS m WHERE m.tenant_id = p_tenant_id AND m.user_id = p_actor) THEN
            RAISE EXCEPTION 'admin: cannot suspend a tenant the admin belongs to' USING ERRCODE = 'PRA06';
        END IF;
    END IF;
    IF v_old = p_status THEN
        RETURN false;
    END IF;
    UPDATE tenants SET status = p_status, updated_at = now() WHERE id = p_tenant_id;
    v_meta := jsonb_build_object('from', v_old, 'to', p_status);
    IF p_status = 'active' THEN
        -- owner_delivery_update lets the owner update rows of any tenant; claim_deliveries skips
        -- the tenant until this transaction commits, so no row is in progress.
        UPDATE notification_deliveries AS d
        SET status = 'failed', last_error = 'tenant_suspended'
        WHERE d.tenant_id = p_tenant_id AND d.status = 'pending' AND d.created_at < now() - interval '15 minutes';
        GET DIAGNOSTICS v_expired = ROW_COUNT;
        v_meta := v_meta || jsonb_build_object('deliveriesExpired', v_expired);
        -- Pings of a suspended tenant were not recorded, so every heartbeat deadline has passed:
        -- restart the wait instead of opening incidents the moment the tenant is back.
        UPDATE heartbeat_tokens SET waiting_since = now() WHERE tenant_id = p_tenant_id;
    END IF;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id,
                        CASE WHEN p_status = 'suspended' THEN 'admin.organization.suspended' ELSE 'admin.organization.unsuspended' END,
                        'organization', p_tenant_id::text, v_meta, p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
