-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The platform admin creates, edits and deletes plans. The seeded plans (free, internal, community)
-- are system plans: their name and limits can change, they cannot be deleted (free is the default
-- of tenants.plan_key and internal is the plan of the default tenant).
ALTER TABLE plans ADD COLUMN is_system boolean NOT NULL DEFAULT false;
UPDATE plans SET is_system = true WHERE key IN ('free', 'internal', 'community');

-- True when l is a limits object for a plan: exactly the keys plans.Resolve needs, each a
-- non-negative integer up to 2147483647 (plans.Unlimited). A key added to the application later
-- is added here and to every plan in the same migration.
-- +goose StatementBegin
CREATE FUNCTION plan_limits_valid(l jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path = public, pg_temp
AS $$
    SELECT jsonb_typeof(l) = 'object'
        AND (SELECT count(*) FROM jsonb_object_keys(l)) = 7
        AND NOT EXISTS (
            SELECT 1
            FROM unnest(ARRAY['max_monitors', 'max_heartbeats', 'max_channels', 'max_members', 'max_projects', 'max_status_pages', 'max_agents']) AS k
            WHERE jsonb_typeof(l -> k) IS DISTINCT FROM 'number'
               OR CASE WHEN (l ->> k) ~ '^(0|[1-9][0-9]{0,9})$' THEN (l ->> k)::bigint > 2147483647 ELSE true END
        )
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION plan_limits_valid(jsonb) FROM PUBLIC;

-- Every plan with its limits, the number of organizations that are not deleted on it, and
-- whether it can be deleted (not a system plan, and no organization at all, deleted ones
-- included, refers to it).
-- +goose StatementBegin
CREATE FUNCTION admin_plans_list(p_actor uuid)
RETURNS TABLE (
    key           text,
    name          text,
    limits        jsonb,
    is_system     boolean,
    organizations integer,
    deletable     boolean
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
    SELECT p.key, p.name, p.limits, p.is_system,
           (SELECT count(*) FROM tenants AS t WHERE t.plan_key = p.key AND t.deleted_at IS NULL)::integer,
           NOT p.is_system AND NOT EXISTS (SELECT 1 FROM tenants AS t WHERE t.plan_key = p.key)
    FROM plans AS p
    ORDER BY p.is_system DESC, p.created_at, p.key;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_plans_list(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plans_list(uuid) TO proreta_api;

-- The highest use of each resource among the organizations (not deleted) on the plan, counted
-- like internal/plans does, so the edit form can warn before a limit goes below it. Organization
-- overrides (tenants.limits) are not taken into account. PRA02 for an unknown plan.
-- +goose StatementBegin
CREATE FUNCTION admin_plan_peak_usage(p_actor uuid, p_key text)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_ids uuid[];
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF NOT EXISTS (SELECT 1 FROM plans AS p WHERE p.key = p_key) THEN
        RAISE EXCEPTION 'admin: plan not found' USING ERRCODE = 'PRA02';
    END IF;
    SELECT COALESCE(array_agg(t.id), '{}') INTO v_ids FROM tenants AS t WHERE t.plan_key = p_key AND t.deleted_at IS NULL;
    RETURN jsonb_build_object(
        'monitors', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM monitors AS m
            WHERE m.tenant_id = ANY (v_ids) AND m.deleted_at IS NULL AND m.type <> 'heartbeat' GROUP BY m.tenant_id) AS x), 0),
        'heartbeats', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM monitors AS m
            WHERE m.tenant_id = ANY (v_ids) AND m.deleted_at IS NULL AND m.type = 'heartbeat' GROUP BY m.tenant_id) AS x), 0),
        'channels', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM notification_channels AS n
            WHERE n.tenant_id = ANY (v_ids) AND n.deleted_at IS NULL GROUP BY n.tenant_id) AS x), 0),
        'members', COALESCE((SELECT max(c) FROM (
            SELECT tid, sum(c) AS c FROM (
                SELECT m.tenant_id AS tid, count(*) AS c FROM memberships AS m WHERE m.tenant_id = ANY (v_ids) GROUP BY m.tenant_id
                UNION ALL
                SELECT i.tenant_id, count(*) FROM invitations AS i
                WHERE i.tenant_id = ANY (v_ids) AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
                  AND (i.project_id IS NULL OR EXISTS (SELECT 1 FROM projects AS p
                       WHERE p.tenant_id = i.tenant_id AND p.id = i.project_id AND p.deleted_at IS NULL))
                GROUP BY i.tenant_id) AS u GROUP BY tid) AS x), 0),
        'projects', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM projects AS p
            WHERE p.tenant_id = ANY (v_ids) AND p.deleted_at IS NULL GROUP BY p.tenant_id) AS x), 0),
        'statusPages', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM status_pages AS s
            WHERE s.tenant_id = ANY (v_ids) AND s.deleted_at IS NULL GROUP BY s.tenant_id) AS x), 0),
        'specula', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM specula_agents AS a
            WHERE a.tenant_id = ANY (v_ids) AND a.revoked_at IS NULL GROUP BY a.tenant_id) AS x), 0)
    );
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_plan_peak_usage(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_peak_usage(uuid, text) TO proreta_api;

-- Creates a custom plan. The key is a slug (lowercase letters, digits, hyphen, 1-63 characters,
-- starting with a letter or digit) that never changes; the name is 1-200 characters; the limits
-- carry every key (plan_limits_valid). PRA30 duplicate key, PRA31 invalid key or name, PRA32
-- invalid limits. Audited in the admin's own organization (a plan has no tenant) in the same
-- transaction as admin.plan.created.
-- +goose StatementBegin
CREATE FUNCTION admin_plan_create(p_actor uuid, p_key text, p_name text, p_limits jsonb, p_audit_id uuid, p_ip inet)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_key IS NULL OR p_key !~ '^[a-z0-9][a-z0-9-]{0,62}$' OR p_name IS NULL OR length(p_name) NOT BETWEEN 1 AND 200 OR btrim(p_name) = '' THEN
        RAISE EXCEPTION 'admin: invalid plan key or name' USING ERRCODE = 'PRA31';
    END IF;
    IF NOT COALESCE(plan_limits_valid(p_limits), false) THEN
        RAISE EXCEPTION 'admin: invalid plan limits' USING ERRCODE = 'PRA32';
    END IF;
    -- Serialises two creates of one key, so the loser gets PRA30 and not a unique violation.
    PERFORM pg_advisory_xact_lock(hashtext('plan:' || p_key));
    IF EXISTS (SELECT 1 FROM plans AS p WHERE p.key = p_key) THEN
        RAISE EXCEPTION 'admin: plan key exists' USING ERRCODE = 'PRA30';
    END IF;
    INSERT INTO plans (key, name, limits) VALUES (p_key, p_name, p_limits);
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.plan.created', 'plan', p_key,
                            jsonb_build_object('key', p_key, 'name', p_name, 'limits', p_limits), p_ip);
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_plan_create(uuid, text, text, jsonb, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_create(uuid, text, text, jsonb, uuid, inet) TO proreta_api;

-- Changes the name and limits of a plan, a system plan included; the key and features stay.
-- Applies at once to every organization on the plan; resources above a lowered limit are kept
-- and only new ones are refused. Returns false (no audit row) when nothing changes. PRA02
-- unknown plan, PRA31 / PRA32 as for create. Audited as admin.plan.updated with the old and new
-- values and the number of organizations on the plan.
-- +goose StatementBegin
CREATE FUNCTION admin_plan_update(p_actor uuid, p_key text, p_name text, p_limits jsonb, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_old_name   text;
    v_old_limits jsonb;
    v_tenant     uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_name IS NULL OR length(p_name) NOT BETWEEN 1 AND 200 OR btrim(p_name) = '' THEN
        RAISE EXCEPTION 'admin: invalid plan key or name' USING ERRCODE = 'PRA31';
    END IF;
    IF NOT COALESCE(plan_limits_valid(p_limits), false) THEN
        RAISE EXCEPTION 'admin: invalid plan limits' USING ERRCODE = 'PRA32';
    END IF;
    SELECT p.name, p.limits INTO v_old_name, v_old_limits FROM plans AS p WHERE p.key = p_key FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: plan not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_old_name = p_name AND v_old_limits = p_limits THEN
        RETURN false;
    END IF;
    UPDATE plans SET name = p_name, limits = p_limits WHERE key = p_key;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.plan.updated', 'plan', p_key,
                            jsonb_build_object('key', p_key, 'name', p_name, 'previousName', v_old_name,
                                               'limits', p_limits, 'previousLimits', v_old_limits,
                                               'organizations', (SELECT count(*) FROM tenants AS t WHERE t.plan_key = p_key AND t.deleted_at IS NULL)), p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_plan_update(uuid, text, text, jsonb, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_update(uuid, text, text, jsonb, uuid, inet) TO proreta_api;

-- Deletes a custom plan nobody refers to. PRA02 unknown plan, PRA33 system plan, PRA34 some
-- organization (a deleted one too: its row still refers to the plan) is on it. Audited as
-- admin.plan.deleted with the name and limits the plan had.
-- +goose StatementBegin
CREATE FUNCTION admin_plan_delete(p_actor uuid, p_key text, p_audit_id uuid, p_ip inet)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_name   text;
    v_limits jsonb;
    v_system boolean;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT p.name, p.limits, p.is_system INTO v_name, v_limits, v_system FROM plans AS p WHERE p.key = p_key FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: plan not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_system THEN
        RAISE EXCEPTION 'admin: system plan' USING ERRCODE = 'PRA33';
    END IF;
    -- A tenant changing to this plan needs the plan row lock (the foreign key takes a share lock
    -- on it), which this transaction holds, so the check below cannot race with an assignment.
    IF EXISTS (SELECT 1 FROM tenants AS t WHERE t.plan_key = p_key) THEN
        RAISE EXCEPTION 'admin: plan in use' USING ERRCODE = 'PRA34';
    END IF;
    DELETE FROM plans WHERE key = p_key;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.plan.deleted', 'plan', p_key,
                            jsonb_build_object('key', p_key, 'name', v_name, 'limits', v_limits), p_ip);
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_plan_delete(uuid, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_delete(uuid, text, uuid, inet) TO proreta_api;
