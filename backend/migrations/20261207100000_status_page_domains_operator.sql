-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Operator interface of the custom status page domains: the platform admin's list and abuse
-- switch, and the export `proretactl status-domains list` reads.

-- Domains of every tenant, newest first, with the number of matches in every row (total_count).
-- Released domains are not listed. p_id (optional) selects one domain. p_query matches the host,
-- the organization's slug or name (substring, case-insensitive). p_states (optional) keeps the
-- given states; an unknown state raises PRA04.
-- +goose StatementBegin
CREATE FUNCTION admin_status_page_domains_list(p_actor uuid, p_id uuid, p_query text, p_states text[], p_limit integer, p_offset integer)
RETURNS TABLE (
    id               uuid,
    host             text,
    state            text,
    is_primary       boolean,
    disabled_at      timestamptz,
    tenant_id        uuid,
    tenant_slug      text,
    page_id          uuid,
    page_slug        text,
    verified_at      timestamptz,
    last_checked_at  timestamptz,
    last_error       text,
    created_at       timestamptz,
    total_count      integer
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    q text := lower(btrim(COALESCE(p_query, '')));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF EXISTS (SELECT 1 FROM unnest(COALESCE(p_states, ARRAY[]::text[])) AS s WHERE s NOT IN ('pending', 'verified', 'active', 'failed')) THEN
        RAISE EXCEPTION 'admin: invalid state' USING ERRCODE = 'PRA04';
    END IF;
    RETURN QUERY
    SELECT d.id, d.host, d.state, d.is_primary, d.disabled_at, d.tenant_id, t.slug, d.page_id, p.slug,
           d.verified_at, d.last_checked_at, d.last_error, d.created_at,
           (count(*) OVER ())::integer
    FROM status_page_domains AS d
    JOIN tenants AS t ON t.id = d.tenant_id
    JOIN status_pages AS p ON p.tenant_id = d.tenant_id AND p.id = d.page_id
    WHERE d.released_at IS NULL
      AND (p_id IS NULL OR d.id = p_id)
      AND (COALESCE(cardinality(p_states), 0) = 0 OR d.state = ANY (p_states))
      AND (q = '' OR strpos(d.host, q) > 0 OR strpos(lower(t.slug), q) > 0 OR strpos(lower(t.name), q) > 0)
    ORDER BY d.created_at DESC, d.id
    LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
    OFFSET greatest(COALESCE(p_offset, 0), 0);
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_status_page_domains_list(uuid, uuid, text, text[], integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_status_page_domains_list(uuid, uuid, text, text[], integer, integer) TO proreta_api;

-- The abuse switch: disables (p_disabled) or re-enables a domain and writes the audit row
-- admin.status_page_domain.disabled|enabled into the organization in the same transaction. The
-- lookup and allowed definers read disabled_at, so a disabled host stops being served and
-- certificate-eligible when this commits. Returns false, writing nothing, when the domain is
-- already in the requested state. PRA02 unknown or released domain, PRA08 deleted organization
-- (read-only like every admin write), PRA18 the reason is empty or longer than 500 characters.
-- +goose StatementBegin
CREATE FUNCTION admin_status_page_domain_set_disabled(p_actor uuid, p_id uuid, p_disabled boolean, p_reason text, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    v_tenant  uuid;
    v_page    uuid;
    v_host    text;
    v_disabled timestamptz;
    v_deleted timestamptz;
    v_reason  text := btrim(COALESCE(p_reason, ''));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_disabled IS NULL OR v_reason = '' OR char_length(v_reason) > 500 THEN
        RAISE EXCEPTION 'admin: invalid reason' USING ERRCODE = 'PRA18';
    END IF;
    SELECT d.tenant_id, d.page_id, d.host, d.disabled_at, t.deleted_at INTO v_tenant, v_page, v_host, v_disabled, v_deleted
    FROM status_page_domains AS d
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.id = p_id AND d.released_at IS NULL
    FOR UPDATE OF d;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: domain not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RAISE EXCEPTION 'admin: tenant is deleted' USING ERRCODE = 'PRA08';
    END IF;
    IF (v_disabled IS NOT NULL) = p_disabled THEN
        RETURN false;
    END IF;
    UPDATE status_page_domains SET disabled_at = CASE WHEN p_disabled THEN now() END, updated_at = now() WHERE id = p_id;
    PERFORM admin_audit(v_tenant, p_actor, p_audit_id,
                        CASE WHEN p_disabled THEN 'admin.status_page_domain.disabled' ELSE 'admin.status_page_domain.enabled' END,
                        'status_page_domain', p_id::text,
                        jsonb_build_object('host', v_host, 'pageId', v_page, 'reason', v_reason), p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_status_page_domain_set_disabled(uuid, uuid, boolean, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_status_page_domain_set_disabled(uuid, uuid, boolean, text, uuid, inet) TO proreta_api;

-- `proretactl status-domains list`: the hosts that are served now (verified or active, not
-- disabled or released, page not deleted, organization active), or with p_all every unreleased
-- domain with its state. No actor: the command is run by the operator with the database URL of
-- the api. Hosts and the slugs of their page and organization; no token, no user data.
-- +goose StatementBegin
CREATE FUNCTION status_page_domains_export(p_all boolean)
RETURNS TABLE (
    host         text,
    state        text,
    is_primary   boolean,
    disabled     boolean,
    tenant_slug  text,
    page_slug    text,
    verified_at  timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT d.host, d.state, d.is_primary, d.disabled_at IS NOT NULL, t.slug, p.slug, d.verified_at
    FROM status_page_domains AS d
    JOIN status_pages AS p ON p.tenant_id = d.tenant_id AND p.id = d.page_id
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.released_at IS NULL
      AND (COALESCE(p_all, false)
           OR (d.state IN ('verified', 'active') AND d.disabled_at IS NULL
               AND p.deleted_at IS NULL AND t.status = 'active' AND t.deleted_at IS NULL))
    ORDER BY d.host, d.id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domains_export(boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domains_export(boolean) TO proreta_api;
