-- +goose Up
SET LOCAL lock_timeout = '5s';

-- =============================================================================================
-- Status pages on a customer's own domain (internal/statuspages)
-- =============================================================================================

-- A host belongs to one status page. It is proven by a TXT record carrying the token and routed
-- by a CNAME; a CNAME alone never serves anything. state: pending (token issued, not seen),
-- verified (TXT seen: served, waiting for the CNAME), active (CNAME right too), failed (no
-- proof for 7 days: not served). A released row (released_at) never serves and never counts;
-- it stays for audit. Only rows that serve are unique by host, so several organizations may
-- hold a pending claim on one host and the first to prove control wins.
CREATE TABLE status_page_domains (
    id              uuid        PRIMARY KEY,
    tenant_id       uuid        NOT NULL REFERENCES tenants (id),
    page_id         uuid        NOT NULL,
    host            text        NOT NULL CHECK (host = lower(host) AND char_length(host) BETWEEN 4 AND 253
                                  AND host ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'verified', 'active', 'failed')),
    is_primary      boolean     NOT NULL DEFAULT false,
    -- Public by design: it sits in DNS.
    token           text        NOT NULL CHECK (token ~ '^[0-9a-f]{32}$'),
    verified_at     timestamptz,
    last_checked_at timestamptz,
    last_proof_at   timestamptz,
    check_failures  integer     NOT NULL DEFAULT 0 CHECK (check_failures >= 0),
    last_error      text        CHECK (last_error IS NULL OR last_error IN ('txt-missing', 'txt-mismatch', 'cname-missing', 'cname-wrong', 'dns-error')),
    -- The platform admin's abuse switch: stops serving and certificates whatever the state.
    disabled_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    released_at     timestamptz,
    UNIQUE (tenant_id, id),
    CONSTRAINT status_page_domains_released_not_primary CHECK (released_at IS NULL OR NOT is_primary),
    FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages (tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX status_page_domains_host_idx ON status_page_domains (host)
    WHERE state IN ('verified', 'active') AND released_at IS NULL;
CREATE UNIQUE INDEX status_page_domains_tenant_host_idx ON status_page_domains (tenant_id, host)
    WHERE released_at IS NULL;
CREATE UNIQUE INDEX status_page_domains_primary_idx ON status_page_domains (page_id)
    WHERE is_primary AND released_at IS NULL;
CREATE INDEX status_page_domains_due_idx ON status_page_domains (last_checked_at NULLS FIRST)
    WHERE released_at IS NULL;
CREATE INDEX status_page_domains_tenant_idx ON status_page_domains (tenant_id, page_id) WHERE released_at IS NULL;
-- Soft release only, so the audit trail keeps its rows. The worker works through definers and has
-- no table rights (a rights-free worker cannot repeat a write on public-facing data); the public
-- renderer has none either.
REVOKE DELETE ON status_page_domains FROM proreta_api;
REVOKE ALL ON status_page_domains FROM proreta_worker;

ALTER TABLE status_page_domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_page_domains FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON status_page_domains
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- The definer functions below read across tenants, and change a row only to apply a check
-- result or to release a holder.
CREATE POLICY owner_domain_read ON status_page_domains FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_domain_update ON status_page_domains FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);

-- Deleting a status page releases its domains in the same statement, whoever deletes it. Invoker
-- rights: the update runs under the tenant isolation policy of the caller's transaction.
-- +goose StatementBegin
CREATE FUNCTION status_pages_release_domains()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
BEGIN
    UPDATE status_page_domains
    SET released_at = now(), is_primary = false, updated_at = now()
    WHERE tenant_id = NEW.tenant_id AND page_id = NEW.id AND released_at IS NULL;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_pages_release_domains() FROM PUBLIC;
CREATE TRIGGER status_pages_release_domains AFTER UPDATE OF deleted_at ON status_pages
    FOR EACH ROW WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
    EXECUTE FUNCTION status_pages_release_domains();

-- statuspage: the slug of the page a host serves. Nothing for a host that is unknown, pending,
-- failed, released or disabled, a page that is unpublished or deleted, or a suspended or deleted
-- tenant: the renderer answers all of them with the same 404. The page itself is then read through
-- status_page_snapshot(slug), so the allow-list of what a page may show is unchanged.
-- +goose StatementBegin
CREATE FUNCTION status_page_domain_lookup(p_host text)
RETURNS TABLE (slug text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT p.slug
    FROM status_page_domains AS d
    JOIN status_pages AS p ON p.tenant_id = d.tenant_id AND p.id = d.page_id
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.host = p_host
      AND d.state IN ('verified', 'active') AND d.disabled_at IS NULL AND d.released_at IS NULL
      AND p.published AND p.deleted_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_lookup(text) FROM PUBLIC;

-- statuspage: whether a certificate may be issued for a host. The same as the lookup without the
-- condition that the page is published (a verified host of a page being edited may get one).
-- +goose StatementBegin
CREATE FUNCTION status_page_domain_allowed(p_host text)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM status_page_domains AS d
        JOIN status_pages AS p ON p.tenant_id = d.tenant_id AND p.id = d.page_id
        JOIN tenants AS t ON t.id = d.tenant_id
        WHERE d.host = p_host
          AND d.state IN ('verified', 'active') AND d.disabled_at IS NULL AND d.released_at IS NULL
          AND p.deleted_at IS NULL
          AND t.status = 'active' AND t.deleted_at IS NULL)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_allowed(text) FROM PUBLIC;

-- The role is created by `proretactl db bootstrap`, never by a migration. When the bootstrap has
-- not created it yet, it grants EXECUTE itself once it does.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'proreta_statuspage') THEN
        GRANT EXECUTE ON FUNCTION status_page_domain_lookup(text) TO proreta_statuspage;
        GRANT EXECUTE ON FUNCTION status_page_domain_allowed(text) TO proreta_statuspage;
    END IF;
END
$$;
-- +goose StatementEnd

-- jobs: up to p_limit domains whose check is due, across tenants. A pending domain is checked every
-- 10 minutes in its first 24 hours and hourly after that, every other one every 6 hours; domains
-- of a suspended or deleted tenant wait. Rows are locked FOR UPDATE SKIP LOCKED until the end of
-- the caller's transaction, so a caller that records each result in that transaction never
-- shares a domain with another replica.
-- +goose StatementBegin
CREATE FUNCTION status_page_domains_due(p_limit integer)
RETURNS TABLE (tenant_id uuid, id uuid, host text, token text, state text, created_at timestamptz, last_proof_at timestamptz, check_failures integer)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    RETURN QUERY
    SELECT d.tenant_id, d.id, d.host, d.token, d.state, d.created_at, d.last_proof_at, d.check_failures
    FROM status_page_domains AS d
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.released_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
      AND (d.last_checked_at IS NULL OR d.last_checked_at <= now() - CASE
              WHEN d.state = 'pending' AND d.created_at > now() - interval '24 hours' THEN interval '10 minutes'
              WHEN d.state = 'pending' THEN interval '1 hour'
              ELSE interval '6 hours' END)
    ORDER BY d.last_checked_at NULLS FIRST, d.id
    LIMIT greatest(1, least(coalesce(p_limit, 1), 1000))
    FOR UPDATE OF d SKIP LOCKED;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domains_due(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domains_due(integer) TO proreta_worker;

-- Applies the result of one DNS check to a domain: the state machine, in one place, so neither
-- the worker nor the api needs a table right for it. p_txt and p_cname are what the lookups said
-- about each record: 'present', 'missing' (the resolver answered and it is not there) or
-- 'unknown' (any error; never counts as a missing record). p_error is the last_error to store
-- (NULL clears it). Rules, as internal/statuspages/domains.Step, which a test compares:
--   pending: a TXT proof verifies (and activates when the CNAME is right too); a missing TXT at
--     7 days of age expires it (released, event 'expired'); a CNAME alone never verifies.
--   verified / active: any proof refreshes it, the CNAME activates it; both records missing count
--     a failure, and after 3 failures in a row and 7 days since the last proof it fails.
--   failed: any proof returns it to verified.
-- Returns the state after the check and the events it caused ('verified', 'activated', 'failed',
-- 'recovered', 'expired'). The worker passes any tenant; the api, whose transaction has
-- app.tenant_id, only its own (PRD02). PRD01 invalid argument, PRD03 no such unreleased domain.
-- A pending to verified step on a host another organization holds is a unique violation: the
-- caller settles the takeover first.
-- +goose StatementBegin
CREATE FUNCTION status_page_domain_record_check(p_tenant uuid, p_id uuid, p_txt text, p_cname text, p_error text)
RETURNS TABLE (state text, events text[])
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_ctx      text := NULLIF(current_setting('app.tenant_id', true), '');
    d          status_page_domains%ROWTYPE;
    v_state    text;
    v_proof    timestamptz;
    v_failures integer;
    v_verified timestamptz;
    v_released timestamptz;
    v_events   text[] := '{}';
    v_any      boolean := p_txt = 'present' OR p_cname = 'present';
BEGIN
    IF p_txt IS NULL OR p_cname IS NULL OR p_txt NOT IN ('present', 'missing', 'unknown') OR p_cname NOT IN ('present', 'missing', 'unknown')
       OR (p_error IS NOT NULL AND p_error NOT IN ('txt-missing', 'txt-mismatch', 'cname-missing', 'cname-wrong', 'dns-error')) THEN
        RAISE EXCEPTION 'status page domain: invalid check result' USING ERRCODE = 'PRD01';
    END IF;
    IF v_ctx IS NOT NULL AND v_ctx <> p_tenant::text THEN
        RAISE EXCEPTION 'status page domain: wrong tenant' USING ERRCODE = 'PRD02';
    END IF;
    SELECT * INTO d FROM status_page_domains AS x WHERE x.tenant_id = p_tenant AND x.id = p_id AND x.released_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'status page domain: not found' USING ERRCODE = 'PRD03';
    END IF;
    v_state := d.state;
    v_proof := d.last_proof_at;
    v_failures := d.check_failures;
    v_verified := d.verified_at;

    IF d.state = 'pending' THEN
        IF p_txt = 'present' THEN
            v_state := 'verified';
            v_proof := now();
            v_failures := 0;
            v_verified := coalesce(d.verified_at, now());
            v_events := ARRAY['verified'];
            IF p_cname = 'present' THEN
                v_state := 'active';
                v_events := array_append(v_events, 'activated');
            END IF;
        ELSIF p_txt = 'missing' AND now() - d.created_at >= interval '7 days' THEN
            v_released := now();
            v_events := ARRAY['expired'];
        END IF;
    ELSIF d.state IN ('verified', 'active') THEN
        IF v_any THEN
            v_proof := now();
            v_failures := 0;
            IF d.state = 'verified' AND p_cname = 'present' THEN
                v_state := 'active';
                v_events := ARRAY['activated'];
            END IF;
        ELSIF p_txt = 'missing' AND p_cname = 'missing' THEN
            v_failures := d.check_failures + 1;
            IF v_failures >= 3 AND now() - coalesce(d.last_proof_at, d.created_at) >= interval '7 days' THEN
                v_state := 'failed';
                v_events := ARRAY['failed'];
            END IF;
        END IF;
    ELSIF d.state = 'failed' AND v_any THEN
        v_state := 'verified';
        v_proof := now();
        v_failures := 0;
        v_events := ARRAY['recovered'];
    END IF;

    UPDATE status_page_domains AS x
    SET state = v_state, last_proof_at = v_proof, check_failures = v_failures, verified_at = v_verified,
        last_checked_at = now(), last_error = p_error, updated_at = now(),
        released_at = v_released, is_primary = x.is_primary AND v_released IS NULL
    WHERE x.tenant_id = p_tenant AND x.id = p_id;
    RETURN QUERY SELECT v_state, v_events;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_record_check(uuid, uuid, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_record_check(uuid, uuid, text, text, text) TO proreta_worker, proreta_api;

-- api: whether another organization than the transaction's (app.tenant_id) holds a host that
-- serves (verified or active, not released). Answers only true or false, never whose it is.
-- +goose StatementBegin
CREATE FUNCTION status_page_domain_held_by_other(p_host text)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT EXISTS (
        SELECT 1 FROM status_page_domains AS d
        WHERE d.host = p_host AND d.state IN ('verified', 'active') AND d.released_at IS NULL
          AND d.tenant_id IS DISTINCT FROM NULLIF(current_setting('app.tenant_id', true), '')::uuid)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_held_by_other(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_held_by_other(text) TO proreta_api;

-- api: lets the organization of the transaction (app.tenant_id) take over a host another
-- organization holds. p_claimant is its own pending or failed row for p_host; p_dns_tokens are the
-- tokens (proreta-verify= values) the caller found in the TXT record of the host just now. The
-- claim is granted when the claimant's own token is among them and the holder's is not: the DNS
-- owner authorised only the claimant. Both present: the holder keeps the host (granted = false).
-- A CNAME plays no part, so a stale CNAME can never take a host. When granted, the holder's row is
-- released (the caller then records the claimant's check, and writes the holder's audit row in the
-- holder's own tenant) and its tenant and page are returned; both are NULL when no one held the
-- host. PRD02 without a tenant, PRD03 when the claimant row does not exist or is not pending or
-- failed.
-- +goose StatementBegin
CREATE FUNCTION status_page_domain_takeover(p_host text, p_claimant uuid, p_dns_tokens text[])
RETURNS TABLE (granted boolean, previous_tenant_id uuid, previous_page_id uuid)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_ctx    uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    v_tokens text[] := coalesce(p_dns_tokens, '{}');
    c        status_page_domains%ROWTYPE;
    h        status_page_domains%ROWTYPE;
BEGIN
    IF v_ctx IS NULL THEN
        RAISE EXCEPTION 'status page domain: no tenant' USING ERRCODE = 'PRD02';
    END IF;
    SELECT * INTO c FROM status_page_domains AS x
    WHERE x.tenant_id = v_ctx AND x.id = p_claimant AND x.host = p_host AND x.released_at IS NULL AND x.state IN ('pending', 'failed');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'status page domain: not found' USING ERRCODE = 'PRD03';
    END IF;
    IF NOT (c.token = ANY (v_tokens)) THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid;
        RETURN;
    END IF;
    SELECT * INTO h FROM status_page_domains AS x
    WHERE x.host = p_host AND x.state IN ('verified', 'active') AND x.released_at IS NULL AND x.tenant_id <> v_ctx
    FOR UPDATE;
    IF NOT FOUND THEN
        RETURN QUERY SELECT true, NULL::uuid, NULL::uuid;
        RETURN;
    END IF;
    IF h.token = ANY (v_tokens) THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::uuid;
        RETURN;
    END IF;
    UPDATE status_page_domains AS x SET released_at = now(), is_primary = false, updated_at = now()
    WHERE x.tenant_id = h.tenant_id AND x.id = h.id;
    RETURN QUERY SELECT true, h.tenant_id, h.page_id;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_takeover(text, uuid, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_takeover(text, uuid, text[]) TO proreta_api;

-- jobs: the number of unreleased domains per state, all four states always present, for a gauge.
-- No host names.
-- +goose StatementBegin
CREATE FUNCTION status_page_domain_counts()
RETURNS TABLE (state text, n bigint)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.state, count(d.id)
    FROM unnest(ARRAY['pending', 'verified', 'active', 'failed']) AS s(state)
    LEFT JOIN status_page_domains AS d ON d.state = s.state AND d.released_at IS NULL
    GROUP BY s.state
    ORDER BY s.state
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_counts() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_counts() TO proreta_worker;

-- =============================================================================================
-- Plan limit max_status_page_domains
-- =============================================================================================

-- plan_limits_valid lists exactly the keys of a plan: the key joins it here and every plan gets it,
-- next to the code that reads it. An older application ignores the key; during a rolling update its
-- plan editor (seven keys) is refused by the new check until it is replaced.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION plan_limits_valid(l jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path = public, pg_temp
AS $$
    SELECT jsonb_typeof(l) = 'object'
        AND (SELECT count(*) FROM jsonb_object_keys(l)) = 8
        AND NOT EXISTS (
            SELECT 1
            FROM unnest(ARRAY['max_monitors', 'max_heartbeats', 'max_channels', 'max_members', 'max_projects', 'max_status_pages', 'max_agents', 'max_status_page_domains']) AS k
            WHERE jsonb_typeof(l -> k) IS DISTINCT FROM 'number'
               OR CASE WHEN (l ->> k) ~ '^(0|[1-9][0-9]{0,9})$' THEN (l ->> k)::bigint > 2147483647 ELSE true END
        )
$$;
-- +goose StatementEnd

-- Free and Internal: none (a paid feature); Community: unlimited; custom plans start at none.
UPDATE plans
SET limits = limits || jsonb_build_object('max_status_page_domains', CASE WHEN key = 'community' THEN 2147483647 ELSE 0 END)
WHERE NOT (limits ? 'max_status_page_domains');

-- The peak use of domains joins the plan editor's warning.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_plan_peak_usage(p_actor uuid, p_key text)
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
            WHERE a.tenant_id = ANY (v_ids) AND a.revoked_at IS NULL GROUP BY a.tenant_id) AS x), 0),
        'statusPageDomains', COALESCE((SELECT max(c) FROM (SELECT count(*) AS c FROM status_page_domains AS d
            WHERE d.tenant_id = ANY (v_ids) AND d.released_at IS NULL GROUP BY d.tenant_id) AS x), 0)
    );
END
$$;
-- +goose StatementEnd
