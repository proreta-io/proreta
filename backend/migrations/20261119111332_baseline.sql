-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The complete schema of a new installation (the 1.0 baseline). The database, the roles and the
-- citext extension are created by `proretactl db bootstrap` before this runs; migrations never
-- create them. Sections: privileges, tenancy and auth, projects, monitors and probe locations,
-- check results and rollups, incidents and notifications, heartbeats, status pages and domains,
-- invitations and OAuth domains, plans, platform admin, specula, email and platform settings,
-- grants on the migration table, seeds. Within a section: tables with their constraints, indexes,
-- RLS and grants, then foreign keys, functions and triggers.


-- =============================================================================================
-- Default privileges
-- =============================================================================================

-- No default privileges on tables or sequences: a table owner creates is closed to the runtime
-- roles until its migration grants exactly what the code needs (a forgotten grant fails loudly,
-- a forgotten revoke would not). Functions get no EXECUTE by default: SECURITY DEFINER functions
-- are granted one by one. The function default is global (not IN SCHEMA) because a per-schema
-- default can only add to the global one, and the global one grants EXECUTE to PUBLIC.
ALTER DEFAULT PRIVILEGES FOR ROLE proreta_owner
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

-- Row-level security on every table with tenant_id, with the same tenant_isolation policy.
-- FORCE makes it apply to the owner too, so definer functions see other tenants only through the
-- owner_* policies, each limited to proreta_owner and to the command its function needs. Runtime
-- roles are never members of proreta_owner (schema lint), so those policies do not widen their
-- access. NULLIF: after a SET LOCAL in an earlier transaction the setting reads as '' (not NULL)
-- on a pooled connection; without NULLIF a query outside WithTenantTx would fail with a cast
-- error on some connections and return nothing on others. With it, it always sees no rows.


-- =============================================================================================
-- Tenancy and auth (internal/tenants, internal/auth, internal/audit)
-- =============================================================================================

-- Tenants are the root of tenancy (no tenant_id column), so they are not under RLS. Soft delete
-- only. Tenants are created only through auth_create_tenant, which picks the plan. Sessions, identities,
-- email verifications and password resets belong to the user.

CREATE TABLE audit_log (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    actor_user_id uuid,
    action text NOT NULL,
    entity_type text NOT NULL,
    entity_id text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    ip inet,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT audit_log_action_check CHECK (((length(action) >= 1) AND (length(action) <= 100))),
    CONSTRAINT audit_log_entity_type_check CHECK (((length(entity_type) >= 1) AND (length(entity_type) <= 100))),
    CONSTRAINT audit_log_pkey PRIMARY KEY (id)
);

ALTER TABLE ONLY audit_log FORCE ROW LEVEL SECURITY;
CREATE INDEX audit_log_tenant_created_idx ON audit_log USING btree (tenant_id, created_at DESC);
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON audit_log FROM proreta_api, proreta_worker;
GRANT INSERT ON audit_log TO proreta_api;
GRANT INSERT ON audit_log TO proreta_worker;

CREATE TABLE auth_identities (
    user_id uuid NOT NULL,
    provider text NOT NULL,
    subject text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    email citext,
    hosted_domain text,
    last_login_at timestamptz,
    CONSTRAINT auth_identities_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320))),
    CONSTRAINT auth_identities_hosted_domain_check CHECK (((length(hosted_domain) >= 1) AND (length(hosted_domain) <= 253))),
    CONSTRAINT auth_identities_known_provider CHECK ((provider = ANY (ARRAY['google'::text, 'github'::text]))),
    CONSTRAINT auth_identities_provider_check CHECK (((length(provider) >= 1) AND (length(provider) <= 64))),
    CONSTRAINT auth_identities_subject_check CHECK (((length(subject) >= 1) AND (length(subject) <= 255))),
    CONSTRAINT auth_identities_pkey PRIMARY KEY (provider, subject)
);
CREATE INDEX auth_identities_user_id_idx ON auth_identities USING btree (user_id);
CREATE UNIQUE INDEX auth_identities_user_provider_key ON auth_identities USING btree (user_id, provider);
REVOKE ALL ON auth_identities FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, DELETE ON auth_identities TO proreta_api;
GRANT UPDATE(email) ON TABLE auth_identities TO proreta_api;
GRANT UPDATE(hosted_domain) ON TABLE auth_identities TO proreta_api;
GRANT UPDATE(last_login_at) ON TABLE auth_identities TO proreta_api;

CREATE TABLE email_verifications (
    token_hash bytea NOT NULL,
    user_id uuid NOT NULL,
    email citext NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CONSTRAINT email_verifications_check CHECK ((expires_at > created_at)),
    CONSTRAINT email_verifications_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320))),
    CONSTRAINT email_verifications_token_hash_check CHECK ((length(token_hash) = 32)),
    CONSTRAINT email_verifications_pkey PRIMARY KEY (token_hash)
);
CREATE INDEX email_verifications_expires_idx ON email_verifications USING btree (expires_at);
CREATE INDEX email_verifications_user_idx ON email_verifications USING btree (user_id, created_at DESC);
REVOKE ALL ON email_verifications FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON email_verifications TO proreta_api;
GRANT UPDATE(used_at) ON TABLE email_verifications TO proreta_api;

CREATE TABLE memberships (
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text,
    created_at timestamptz DEFAULT now() NOT NULL,
    last_used_at timestamptz,
    CONSTRAINT memberships_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'maintainer'::text, 'member'::text, 'viewer'::text]))),
    CONSTRAINT memberships_pkey PRIMARY KEY (tenant_id, user_id)
);

ALTER TABLE ONLY memberships FORCE ROW LEVEL SECURITY;
CREATE INDEX memberships_user_id_idx ON memberships USING btree (user_id);
ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_membership_lookup ON memberships FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON memberships USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON memberships FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON memberships TO proreta_api;

CREATE TABLE password_resets (
    token_hash bytea NOT NULL,
    user_id uuid NOT NULL,
    email citext NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CONSTRAINT password_resets_check CHECK ((expires_at > created_at)),
    CONSTRAINT password_resets_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320))),
    CONSTRAINT password_resets_token_hash_check CHECK ((length(token_hash) = 32)),
    CONSTRAINT password_resets_pkey PRIMARY KEY (token_hash)
);
CREATE INDEX password_resets_expires_idx ON password_resets USING btree (expires_at);
CREATE INDEX password_resets_user_idx ON password_resets USING btree (user_id, created_at DESC);
REVOKE ALL ON password_resets FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON password_resets TO proreta_api;
GRANT UPDATE(used_at) ON TABLE password_resets TO proreta_api;

CREATE TABLE sessions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    csrf_token text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz DEFAULT now() NOT NULL,
    ip inet,
    user_agent text,
    opened_with_password boolean DEFAULT false NOT NULL,
    CONSTRAINT sessions_token_hash_check CHECK ((length(token_hash) = 32)),
    CONSTRAINT sessions_user_agent_check CHECK ((length(user_agent) <= 512)),
    CONSTRAINT sessions_pkey PRIMARY KEY (id),
    CONSTRAINT sessions_token_hash_key UNIQUE (token_hash)
);

ALTER TABLE ONLY sessions FORCE ROW LEVEL SECURITY;
CREATE INDEX sessions_expires_at_idx ON sessions USING btree (expires_at);
CREATE INDEX sessions_tenant_user_idx ON sessions USING btree (tenant_id, user_id);
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_session_lookup ON sessions FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_session_purge ON sessions FOR DELETE TO proreta_owner USING ((expires_at < now()));
CREATE POLICY tenant_isolation ON sessions USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON sessions FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON sessions TO proreta_api;

CREATE TABLE tenants (
    id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    limits jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    plan_key text DEFAULT 'internal'::text NOT NULL,
    CONSTRAINT tenants_limits_is_object CHECK ((jsonb_typeof(limits) = 'object'::text)),
    -- An override may name any subset of the plan's keys (the reader ignores other keys), each a non-negative integer up to 2147483647.
    CONSTRAINT tenants_limits_valid CHECK (NOT jsonb_path_exists(limits, '$.keyvalue() ? (@.key like_regex "^max_(monitors|heartbeats|channels|members|projects|status_pages|agents|status_page_domains)$" && !(@.value.type() == "number" && @.value >= 0 && @.value <= 2147483647 && @.value.floor() == @.value))', '{}', true)),
    CONSTRAINT tenants_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200))),
    CONSTRAINT tenants_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text)),
    CONSTRAINT tenants_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text]))),
    CONSTRAINT tenants_pkey PRIMARY KEY (id),
    CONSTRAINT tenants_slug_key UNIQUE (slug)
);
REVOKE ALL ON tenants FROM proreta_api, proreta_worker;
GRANT SELECT ON tenants TO proreta_api;
GRANT SELECT ON tenants TO proreta_worker;
GRANT UPDATE(updated_at) ON TABLE tenants TO proreta_api;

CREATE TABLE users (
    id uuid NOT NULL,
    email citext NOT NULL,
    password_hash text,
    display_name text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    is_platform_admin boolean DEFAULT false NOT NULL,
    email_verified_at timestamptz,
    password_changed_at timestamptz,
    configured_password_hash text,
    password_must_change boolean DEFAULT false NOT NULL,
    is_system_admin boolean DEFAULT false NOT NULL,
    deleted_at timestamptz,
    CONSTRAINT users_deleted_pair CHECK (((status = 'deleted'::text) = (deleted_at IS NOT NULL))),
    CONSTRAINT users_display_name_check CHECK ((length(display_name) <= 200)),
    CONSTRAINT users_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320))),
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deleted'::text]))),
    CONSTRAINT users_system_admin_is_platform_admin CHECK ((is_platform_admin OR (NOT is_system_admin))),
    CONSTRAINT users_email_key UNIQUE (email),
    CONSTRAINT users_pkey PRIMARY KEY (id)
);
REVOKE ALL ON users FROM proreta_api, proreta_worker;
GRANT SELECT ON users TO proreta_api;
GRANT INSERT(id) ON TABLE users TO proreta_api;
GRANT INSERT(email),UPDATE(email) ON TABLE users TO proreta_api;
GRANT INSERT(password_hash),UPDATE(password_hash) ON TABLE users TO proreta_api;
GRANT INSERT(display_name),UPDATE(display_name) ON TABLE users TO proreta_api;
GRANT UPDATE(status) ON TABLE users TO proreta_api;
GRANT UPDATE(updated_at) ON TABLE users TO proreta_api;
GRANT INSERT(email_verified_at),UPDATE(email_verified_at) ON TABLE users TO proreta_api;
GRANT UPDATE(password_changed_at) ON TABLE users TO proreta_api;
GRANT UPDATE(configured_password_hash) ON TABLE users TO proreta_api;
GRANT INSERT(password_must_change),UPDATE(password_must_change) ON TABLE users TO proreta_api;

ALTER TABLE ONLY audit_log
    ADD CONSTRAINT audit_log_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id);

ALTER TABLE ONLY audit_log
    ADD CONSTRAINT audit_log_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY auth_identities
    ADD CONSTRAINT auth_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE ONLY email_verifications
    ADD CONSTRAINT email_verifications_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE ONLY memberships
    ADD CONSTRAINT memberships_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY memberships
    ADD CONSTRAINT memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE ONLY password_resets
    ADD CONSTRAINT password_resets_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_tenant_id_user_id_fkey FOREIGN KEY (tenant_id, user_id) REFERENCES memberships(tenant_id, user_id) ON DELETE CASCADE;

-- +goose StatementBegin
CREATE FUNCTION auth_count_other_memberships(p_user_id uuid, p_tenant_id uuid) RETURNS integer
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT count(*)::integer
    FROM memberships AS m
    WHERE m.user_id = p_user_id AND m.tenant_id <> p_tenant_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_count_other_memberships(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_count_other_memberships(uuid,uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_create_tenant(p_id uuid, p_slug text, p_name text) RETURNS uuid
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    INSERT INTO tenants (id, slug, name, plan_key)
    VALUES (p_id, p_slug, p_name, 'internal')
    ON CONFLICT (slug) DO NOTHING
    RETURNING id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_create_tenant(uuid,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_create_tenant(uuid,text,text) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_login_tenant(p_user_id uuid) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT m.tenant_id
    FROM memberships AS m
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.user_id = p_user_id
    ORDER BY (t.deleted_at IS NULL) DESC, (t.status = 'active') DESC, m.last_used_at DESC NULLS LAST,
             (t.slug = 'default') DESC, m.created_at, m.tenant_id
    LIMIT 1
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_login_tenant(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_login_tenant(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_lookup_session(p_token_hash bytea) RETURNS TABLE(id uuid, user_id uuid, tenant_id uuid, csrf_token text, created_at timestamp with time zone, expires_at timestamp with time zone, last_seen_at timestamp with time zone)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT s.id, s.user_id, s.tenant_id, s.csrf_token, s.created_at, s.expires_at, s.last_seen_at
    FROM sessions AS s
    JOIN users AS u ON u.id = s.user_id
    JOIN tenants AS t ON t.id = s.tenant_id
    WHERE s.token_hash = p_token_hash
      AND s.expires_at > now()
      AND u.status = 'active'
      AND t.status = 'active'
      AND t.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_lookup_session(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_session(bytea) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_revoke_user_sessions(p_user_id uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    caller text := current_setting('app.tenant_id', true);
    t      uuid;
    n      integer;
    total  integer := 0;
BEGIN
    FOR t IN SELECT DISTINCT s.tenant_id FROM sessions AS s WHERE s.user_id = p_user_id LOOP
        PERFORM set_config('app.tenant_id', t::text, true);
        DELETE FROM sessions AS s WHERE s.tenant_id = t AND s.user_id = p_user_id;
        GET DIAGNOSTICS n = ROW_COUNT;
        total := total + n;
    END LOOP;
    PERFORM set_config('app.tenant_id', COALESCE(caller, ''), true);
    RETURN total;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_revoke_user_sessions(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_revoke_user_sessions(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_sync_platform_admin(p_user_id uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION auth_sync_platform_admin(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_sync_platform_admin(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_user_organizations(p_user_id uuid) RETURNS TABLE(tenant_id uuid, slug text, name text, status text, role text, joined_at timestamp with time zone)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT t.id, t.slug, t.name, t.status, m.role, m.created_at
    FROM memberships AS m
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.user_id = p_user_id
      AND t.deleted_at IS NULL
    ORDER BY lower(t.name), t.id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_user_organizations(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_user_organizations(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION org_delete_tenant(p_actor uuid, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    v_tenant  uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    v_slug    text;
    v_deleted timestamptz;
BEGIN
    IF v_tenant IS NULL THEN
        RAISE EXCEPTION 'org_delete_tenant: no tenant in the transaction' USING ERRCODE = 'PRO02';
    END IF;
    SELECT t.slug, t.deleted_at INTO v_slug, v_deleted FROM tenants AS t WHERE t.id = v_tenant FOR UPDATE;
    IF NOT FOUND OR v_deleted IS NOT NULL THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM memberships AS m JOIN users AS u ON u.id = m.user_id
        WHERE m.tenant_id = v_tenant AND m.user_id = p_actor AND m.role = 'owner' AND u.status = 'active'
    ) THEN
        RAISE EXCEPTION 'org_delete_tenant: actor is not an owner' USING ERRCODE = 'PRO02';
    END IF;
    IF v_slug = 'default' THEN
        RAISE EXCEPTION 'org_delete_tenant: the default tenant cannot be deleted' USING ERRCODE = 'PRO01';
    END IF;
    UPDATE tenants SET deleted_at = now(), status = 'suspended', updated_at = now() WHERE id = v_tenant;
    INSERT INTO audit_log (id, tenant_id, actor_user_id, action, entity_type, entity_id, metadata, ip)
    VALUES (p_audit_id, v_tenant, p_actor, 'organization.deleted', 'organization', v_tenant::text, '{}'::jsonb, p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION org_delete_tenant(uuid,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION org_delete_tenant(uuid,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION purge_email_verifications() RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH gone AS (
        DELETE FROM email_verifications AS v
        WHERE v.token_hash IN (
            SELECT e.token_hash FROM email_verifications AS e
            WHERE e.expires_at < now() - interval '1 day'
            ORDER BY e.expires_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_email_verifications() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_email_verifications() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION purge_expired_sessions() RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH gone AS (
        DELETE FROM sessions AS s
        WHERE s.id IN (
            SELECT e.id FROM sessions AS e
            WHERE e.expires_at < now()
            ORDER BY e.expires_at
            LIMIT 1000
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_expired_sessions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_expired_sessions() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION purge_password_resets() RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH gone AS (
        DELETE FROM password_resets AS r
        WHERE r.token_hash IN (
            SELECT e.token_hash FROM password_resets AS e
            WHERE e.expires_at < now() - interval '1 day'
            ORDER BY e.expires_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_password_resets() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_password_resets() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION uuidv7_now() RETURNS uuid
    LANGUAGE sql
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT encode(set_bit(set_bit(overlay(uuid_send(gen_random_uuid())
               PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
               FROM 1 FOR 6), 52, 1), 53, 1), 'hex')::uuid
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION uuidv7_now() FROM PUBLIC;

-- =============================================================================================
-- Projects
-- =============================================================================================

-- A project groups monitors, channels and status pages inside a tenant; every tenant has a default one
-- (tenants_create_default_project).

CREATE TABLE project_members (
    tenant_id uuid NOT NULL,
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT project_members_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'maintainer'::text, 'member'::text, 'viewer'::text]))),
    CONSTRAINT project_members_pkey PRIMARY KEY (tenant_id, project_id, user_id)
);

ALTER TABLE ONLY project_members FORCE ROW LEVEL SECURITY;
CREATE INDEX project_members_user_idx ON project_members USING btree (tenant_id, user_id);
ALTER TABLE project_members ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_members USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON project_members FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON project_members TO proreta_api;

CREATE TABLE projects (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    all_members boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    CONSTRAINT projects_default_live CHECK (((NOT is_default) OR (deleted_at IS NULL))),
    CONSTRAINT projects_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 60))),
    CONSTRAINT projects_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text)),
    CONSTRAINT projects_pkey PRIMARY KEY (id),
    CONSTRAINT projects_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY projects FORCE ROW LEVEL SECURITY;
CREATE UNIQUE INDEX projects_one_default_idx ON projects USING btree (tenant_id) WHERE is_default;
CREATE UNIQUE INDEX projects_slug_idx ON projects USING btree (tenant_id, slug) WHERE (deleted_at IS NULL);
ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_invitation_project_lookup ON projects FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON projects USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON projects FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON projects TO proreta_api;

ALTER TABLE ONLY project_members
    ADD CONSTRAINT project_members_tenant_id_project_id_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id);

ALTER TABLE ONLY project_members
    ADD CONSTRAINT project_members_tenant_id_user_id_fkey FOREIGN KEY (tenant_id, user_id) REFERENCES memberships(tenant_id, user_id) ON DELETE CASCADE;

ALTER TABLE ONLY projects
    ADD CONSTRAINT projects_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

-- +goose StatementBegin
CREATE FUNCTION tenants_create_default_project() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    v_prev text := current_setting('app.tenant_id', true);
BEGIN
    PERFORM set_config('app.tenant_id', NEW.id::text, true);
    INSERT INTO projects (id, tenant_id, name, slug, is_default, all_members)
    VALUES (uuidv7_now(), NEW.id, 'Default', 'default', true, true);
    PERFORM set_config('app.tenant_id', coalesce(v_prev, ''), true);
    RETURN NULL;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION tenants_create_default_project() FROM PUBLIC;

CREATE TRIGGER tenants_create_default_project AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION tenants_create_default_project();

-- =============================================================================================
-- Monitors and probe locations (internal/monitors, internal/scheduler)
-- =============================================================================================

-- Monitors (http, tcp and heartbeat), their runtime state, the probe locations they run from and the
-- schedule epochs the claim query uses.

CREATE TABLE monitor_location_state (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    location_id uuid NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    next_run_at timestamptz NOT NULL,
    lease_token uuid,
    lease_until timestamptz,
    last_checked_at timestamptz,
    CONSTRAINT monitor_location_state_check CHECK (((lease_token IS NULL) = (lease_until IS NULL))),
    CONSTRAINT monitor_location_state_consecutive_failures_check CHECK ((consecutive_failures >= 0)),
    CONSTRAINT monitor_location_state_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'up'::text, 'down'::text]))),
    CONSTRAINT monitor_location_state_pkey PRIMARY KEY (monitor_id, location_id)
)
WITH (fillfactor='70');

ALTER TABLE ONLY monitor_location_state FORCE ROW LEVEL SECURITY;
CREATE INDEX monitor_location_state_due_idx ON monitor_location_state USING btree (next_run_at);
CREATE INDEX monitor_location_state_lease_idx ON monitor_location_state USING btree (lease_until) WHERE (lease_token IS NOT NULL);
ALTER TABLE monitor_location_state ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_claim_lease ON monitor_location_state FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_claim_read ON monitor_location_state FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON monitor_location_state USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON monitor_location_state FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON monitor_location_state TO proreta_api;
GRANT SELECT, UPDATE ON monitor_location_state TO proreta_worker;

CREATE TABLE monitor_locations (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    location_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT monitor_locations_pkey PRIMARY KEY (monitor_id, location_id),
    CONSTRAINT monitor_locations_tenant_id_monitor_id_location_id_key UNIQUE (tenant_id, monitor_id, location_id)
);

ALTER TABLE ONLY monitor_locations FORCE ROW LEVEL SECURITY;
CREATE INDEX monitor_locations_location_idx ON monitor_locations USING btree (location_id);
ALTER TABLE monitor_locations ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_specula_offline_locations ON monitor_locations FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON monitor_locations USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON monitor_locations FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON monitor_locations TO proreta_api;
GRANT SELECT ON monitor_locations TO proreta_worker;

CREATE TABLE monitor_schedule_epochs (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    location_id uuid NOT NULL,
    epoch integer NOT NULL,
    anchor bigint NOT NULL,
    interval_seconds integer NOT NULL,
    active_from timestamptz NOT NULL,
    active_until timestamptz,
    CONSTRAINT monitor_schedule_epochs_anchor_check CHECK ((anchor > 0)),
    CONSTRAINT monitor_schedule_epochs_check CHECK (((active_until IS NULL) OR (active_until >= active_from))),
    CONSTRAINT monitor_schedule_epochs_epoch_check CHECK ((epoch >= 1)),
    CONSTRAINT monitor_schedule_epochs_interval_seconds_check CHECK ((interval_seconds > 0)),
    CONSTRAINT monitor_schedule_epochs_pkey PRIMARY KEY (monitor_id, location_id, epoch)
);

ALTER TABLE ONLY monitor_schedule_epochs FORCE ROW LEVEL SECURITY;
CREATE INDEX monitor_schedule_epochs_location_idx ON monitor_schedule_epochs USING btree (location_id) WHERE (active_until IS NULL);
CREATE UNIQUE INDEX monitor_schedule_epochs_open_idx ON monitor_schedule_epochs USING btree (monitor_id, location_id) WHERE (active_until IS NULL);
ALTER TABLE monitor_schedule_epochs ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_schedule_epochs ON monitor_schedule_epochs TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON monitor_schedule_epochs USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON monitor_schedule_epochs FROM proreta_api, proreta_worker;
GRANT SELECT ON monitor_schedule_epochs TO proreta_worker;

CREATE TABLE monitor_state (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    status_changed_at timestamptz DEFAULT now() NOT NULL,
    last_error_kind text,
    last_checked_at timestamptz,
    tls_expires_at timestamptz,
    tls_warned_not_after timestamptz,
    current_incident_id uuid,
    CONSTRAINT monitor_state_last_error_kind_check CHECK (((length(last_error_kind) >= 1) AND (length(last_error_kind) <= 64))),
    CONSTRAINT monitor_state_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'up'::text, 'down'::text]))),
    CONSTRAINT monitor_state_pkey PRIMARY KEY (monitor_id)
)
WITH (fillfactor='80');

ALTER TABLE ONLY monitor_state FORCE ROW LEVEL SECURITY;
ALTER TABLE monitor_state ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_specula_unreachable_read ON monitor_state FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_specula_unreachable_write ON monitor_state FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON monitor_state USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON monitor_state FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON monitor_state TO proreta_api;
GRANT SELECT, UPDATE ON monitor_state TO proreta_worker;
GRANT UPDATE(status) ON TABLE monitor_state TO proreta_api;
GRANT UPDATE(status_changed_at) ON TABLE monitor_state TO proreta_api;
GRANT UPDATE(last_error_kind) ON TABLE monitor_state TO proreta_api;
GRANT UPDATE(tls_expires_at) ON TABLE monitor_state TO proreta_api;
GRANT UPDATE(tls_warned_not_after) ON TABLE monitor_state TO proreta_api;
GRANT UPDATE(current_incident_id) ON TABLE monitor_state TO proreta_api;

CREATE TABLE monitors (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    type text DEFAULT 'http'::text NOT NULL,
    config jsonb NOT NULL,
    interval_seconds integer DEFAULT 60 NOT NULL,
    timeout_seconds integer DEFAULT 10 NOT NULL,
    failure_threshold integer DEFAULT 2 NOT NULL,
    notifications_enabled boolean DEFAULT true NOT NULL,
    paused boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    project_id uuid NOT NULL,
    CONSTRAINT monitors_check CHECK (((timeout_seconds > 0) AND (timeout_seconds < interval_seconds))),
    CONSTRAINT monitors_config_check CHECK ((jsonb_typeof(config) = 'object'::text)),
    CONSTRAINT monitors_failure_threshold_check CHECK (((failure_threshold >= 1) AND (failure_threshold <= 3))),
    CONSTRAINT monitors_heartbeat_check CHECK (((type <> 'heartbeat'::text) OR ((interval_seconds BETWEEN 60 AND 86400) AND (failure_threshold = 1) AND
CASE
    WHEN ((jsonb_typeof((config -> 'graceSeconds'::text)) = 'number'::text) AND ((config ->> 'graceSeconds'::text) ~ '^[0-9]+$'::text)) THEN ((((config ->> 'graceSeconds'::text))::numeric >= (0)::numeric) AND (((config ->> 'graceSeconds'::text))::numeric <= (3600)::numeric))
    ELSE false
END))),
    CONSTRAINT monitors_interval_seconds_check CHECK ((interval_seconds > 0)),
    CONSTRAINT monitors_fixed_interval CHECK (((type = 'heartbeat'::text) OR (interval_seconds = 60))),
    CONSTRAINT monitors_fixed_timeout CHECK (((type = 'heartbeat'::text) OR (timeout_seconds = 10))),
    CONSTRAINT monitors_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 80))),
    CONSTRAINT monitors_tcp_check CHECK (((type <> 'tcp'::text) OR ((COALESCE(jsonb_typeof((config -> 'host'::text)), ''::text) = 'string'::text) AND
CASE
    WHEN ((jsonb_typeof((config -> 'port'::text)) = 'number'::text) AND ((config ->> 'port'::text) ~ '^[0-9]+$'::text)) THEN ((((config ->> 'port'::text))::numeric >= (1)::numeric) AND (((config ->> 'port'::text))::numeric <= (65535)::numeric))
    ELSE false
END))),
    CONSTRAINT monitors_type_check CHECK ((type = ANY (ARRAY['http'::text, 'tcp'::text, 'heartbeat'::text]))),
    CONSTRAINT monitors_pkey PRIMARY KEY (id),
    CONSTRAINT monitors_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY monitors FORCE ROW LEVEL SECURITY;
CREATE INDEX monitors_project_idx ON monitors USING btree (tenant_id, project_id) WHERE (deleted_at IS NULL);
CREATE INDEX monitors_tenant_live_idx ON monitors USING btree (tenant_id, created_at) WHERE (deleted_at IS NULL);
ALTER TABLE monitors ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_claim_read ON monitors FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON monitors USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON monitors FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON monitors TO proreta_api;
GRANT SELECT ON monitors TO proreta_worker;

CREATE TABLE probe_locations (
    id uuid NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    provider text NOT NULL,
    region text NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT probe_locations_code_check CHECK ((code ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text)),
    CONSTRAINT probe_locations_kind_check CHECK ((kind = ANY (ARRAY['platform'::text, 'agent'::text]))),
    CONSTRAINT probe_locations_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200))),
    CONSTRAINT probe_locations_provider_check CHECK (((length(provider) >= 1) AND (length(provider) <= 100))),
    CONSTRAINT probe_locations_region_check CHECK (((length(region) >= 1) AND (length(region) <= 100))),
    CONSTRAINT probe_locations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text]))),
    CONSTRAINT probe_locations_code_key UNIQUE (code),
    CONSTRAINT probe_locations_pkey PRIMARY KEY (id)
);
REVOKE ALL ON probe_locations FROM proreta_api, proreta_worker;
GRANT SELECT ON probe_locations TO proreta_api;
GRANT SELECT ON probe_locations TO proreta_worker;

ALTER TABLE ONLY monitor_location_state
    ADD CONSTRAINT monitor_location_state_tenant_id_monitor_id_location_id_fkey FOREIGN KEY (tenant_id, monitor_id, location_id) REFERENCES monitor_locations(tenant_id, monitor_id, location_id);

ALTER TABLE ONLY monitor_locations
    ADD CONSTRAINT monitor_locations_location_id_fkey FOREIGN KEY (location_id) REFERENCES probe_locations(id);

ALTER TABLE ONLY monitor_locations
    ADD CONSTRAINT monitor_locations_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY monitor_schedule_epochs
    ADD CONSTRAINT monitor_schedule_epochs_tenant_id_monitor_id_location_id_fkey FOREIGN KEY (tenant_id, monitor_id, location_id) REFERENCES monitor_locations(tenant_id, monitor_id, location_id);

ALTER TABLE ONLY monitor_state
    ADD CONSTRAINT monitor_state_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY monitors
    ADD CONSTRAINT monitors_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id);

ALTER TABLE ONLY monitors
    ADD CONSTRAINT monitors_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

-- +goose StatementBegin
CREATE FUNCTION monitor_location_state_epoch_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM specula_epoch_open(NEW.tenant_id, NEW.monitor_id, NEW.location_id);
    ELSE
        PERFORM specula_epoch_close(OLD.tenant_id, OLD.monitor_id, OLD.location_id);
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION monitor_location_state_epoch_sync() FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION monitor_set_location(p_monitor_id uuid, p_location_id uuid) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    mon_project   uuid;
    mon_paused    boolean;
    loc_kind      text;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'monitor_set_location: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    SELECT project_id, paused INTO mon_project, mon_paused FROM monitors
    WHERE tenant_id = caller_tenant AND id = p_monitor_id AND deleted_at IS NULL AND type IN ('http', 'tcp');
    IF mon_project IS NULL THEN
        RETURN 'no_monitor';
    END IF;
    SELECT kind INTO loc_kind FROM probe_locations WHERE id = p_location_id AND status = 'active';
    IF loc_kind IS NULL THEN
        RETURN 'bad_location';
    END IF;
    IF loc_kind = 'agent' AND NOT EXISTS (
        SELECT 1 FROM specula_agents AS a
        WHERE a.tenant_id = caller_tenant AND a.location_id = p_location_id AND a.revoked_at IS NULL
          AND (a.all_projects OR EXISTS (
                SELECT 1 FROM specula_projects AS sp WHERE sp.specula_id = a.id AND sp.project_id = mon_project)))
    THEN
        RETURN 'bad_location';
    END IF;
    DELETE FROM monitor_location_state
    WHERE tenant_id = caller_tenant AND monitor_id = p_monitor_id AND location_id <> p_location_id;
    INSERT INTO monitor_locations (tenant_id, monitor_id, location_id)
    VALUES (caller_tenant, p_monitor_id, p_location_id) ON CONFLICT DO NOTHING;
    INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at)
    VALUES (caller_tenant, p_monitor_id, p_location_id,
            CASE WHEN mon_paused THEN 'infinity'::timestamptz ELSE now() + random() * interval '30 seconds' END)
    ON CONFLICT DO NOTHING;
    RETURN 'ok';
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION monitor_set_location(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION monitor_set_location(uuid,uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION monitors_epoch_sync() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    loc uuid;
BEGIN
    FOR loc IN SELECT s.location_id FROM monitor_location_state AS s
               WHERE s.tenant_id = NEW.tenant_id AND s.monitor_id = NEW.id
    LOOP
        IF NEW.paused OR NEW.deleted_at IS NOT NULL THEN
            PERFORM specula_epoch_close(NEW.tenant_id, NEW.id, loc);
        ELSE
            IF NEW.config IS DISTINCT FROM OLD.config OR NEW.type IS DISTINCT FROM OLD.type
               OR NEW.interval_seconds IS DISTINCT FROM OLD.interval_seconds
               OR NEW.timeout_seconds IS DISTINCT FROM OLD.timeout_seconds THEN
                PERFORM specula_epoch_close(NEW.tenant_id, NEW.id, loc);
            END IF;
            PERFORM specula_epoch_open(NEW.tenant_id, NEW.id, loc);
        END IF;
    END LOOP;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION monitors_epoch_sync() FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION reset_monitor_runtime(p_monitor_id uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    n             integer;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'reset_monitor_runtime: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    UPDATE monitor_location_state AS s
    SET status = 'unknown',
        consecutive_failures = 0,
        lease_token = NULL,
        lease_until = NULL,
        next_run_at = CASE WHEN m.type IN ('http', 'tcp') AND NOT m.paused AND m.deleted_at IS NULL
                           THEN now() + random() * interval '30 seconds'
                           ELSE 'infinity'::timestamptz END
    FROM monitors AS m
    WHERE m.tenant_id = caller_tenant AND m.id = p_monitor_id
      AND s.tenant_id = m.tenant_id AND s.monitor_id = m.id;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION reset_monitor_runtime(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION reset_monitor_runtime(uuid) TO proreta_api;

CREATE TRIGGER monitor_location_state_epoch_sync AFTER INSERT OR DELETE ON monitor_location_state FOR EACH ROW EXECUTE FUNCTION monitor_location_state_epoch_sync();

CREATE TRIGGER monitors_epoch_sync AFTER UPDATE OF paused, deleted_at, config, type, interval_seconds, timeout_seconds ON monitors FOR EACH ROW WHEN (((old.paused IS DISTINCT FROM new.paused) OR (old.deleted_at IS DISTINCT FROM new.deleted_at) OR (old.config IS DISTINCT FROM new.config) OR (old.type IS DISTINCT FROM new.type) OR (old.interval_seconds IS DISTINCT FROM new.interval_seconds) OR (old.timeout_seconds IS DISTINCT FROM new.timeout_seconds))) EXECUTE FUNCTION monitors_epoch_sync();

-- =============================================================================================
-- Check results and rollups (internal/results)
-- =============================================================================================

-- check_results is partitioned by month (ensure_partitions creates them, retention detaches and
-- drops them); hourly rollups are computed by the jobs process from a dirty-hour queue.

CREATE SEQUENCE rollup_dirty_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
REVOKE ALL ON SEQUENCE rollup_dirty_seq FROM proreta_api, proreta_worker;

CREATE TABLE check_results (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    location_id uuid NOT NULL,
    checked_at timestamptz NOT NULL,
    ok boolean NOT NULL,
    status_code smallint,
    latency_ms integer,
    error_kind text,
    tls_not_after timestamptz,
    CONSTRAINT check_results_check CHECK ((ok = (error_kind IS NULL))),
    CONSTRAINT check_results_error_kind_check CHECK (((length(error_kind) >= 1) AND (length(error_kind) <= 64))),
    CONSTRAINT check_results_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT check_results_status_code_check CHECK (((status_code >= 100) AND (status_code <= 599))),
    CONSTRAINT check_results_pkey PRIMARY KEY (monitor_id, checked_at, location_id)
)
PARTITION BY RANGE (checked_at);

ALTER TABLE ONLY check_results FORCE ROW LEVEL SECURITY;
CREATE INDEX check_results_checked_at_brin ON ONLY check_results USING brin (checked_at);
ALTER TABLE check_results ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_rollup_read ON check_results FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON check_results USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON check_results FROM proreta_api, proreta_worker;
GRANT SELECT ON check_results TO proreta_api;
GRANT SELECT, INSERT ON check_results TO proreta_worker;

CREATE TABLE result_rollups_hourly (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    location_id uuid NOT NULL,
    hour timestamptz NOT NULL,
    total integer NOT NULL,
    ok_count integer NOT NULL,
    latency_sum_ms bigint NOT NULL,
    latency_histogram integer[] NOT NULL,
    computed_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT result_rollups_hourly_check CHECK (((ok_count >= 0) AND (ok_count <= total))),
    CONSTRAINT result_rollups_hourly_hour_check CHECK ((hour = date_trunc('hour'::text, hour, 'UTC'::text))),
    CONSTRAINT result_rollups_hourly_latency_histogram_check CHECK ((cardinality(latency_histogram) = 11)),
    CONSTRAINT result_rollups_hourly_latency_sum_ms_check CHECK ((latency_sum_ms >= 0)),
    CONSTRAINT result_rollups_hourly_total_check CHECK ((total > 0)),
    CONSTRAINT result_rollups_hourly_pkey PRIMARY KEY (monitor_id, location_id, hour)
);

ALTER TABLE ONLY result_rollups_hourly FORCE ROW LEVEL SECURITY;
CREATE INDEX result_rollups_hourly_hour_idx ON result_rollups_hourly USING btree (hour);
ALTER TABLE result_rollups_hourly ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_rollup_write ON result_rollups_hourly TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON result_rollups_hourly USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON result_rollups_hourly FROM proreta_api, proreta_worker;
GRANT SELECT ON result_rollups_hourly TO proreta_api;
GRANT SELECT ON result_rollups_hourly TO proreta_worker;

CREATE TABLE rollup_dirty_hours (
    hour timestamptz NOT NULL,
    seq bigint NOT NULL,
    marked_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT rollup_dirty_hours_hour_check CHECK ((hour = date_trunc('hour'::text, hour, 'UTC'::text))),
    CONSTRAINT rollup_dirty_hours_pkey PRIMARY KEY (hour)
);
ALTER TABLE rollup_dirty_hours ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON rollup_dirty_hours FROM proreta_api, proreta_worker;

CREATE TABLE rollup_state (
    singleton boolean DEFAULT true NOT NULL,
    watermark timestamptz NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT rollup_state_singleton_check CHECK (singleton),
    CONSTRAINT rollup_state_watermark_check CHECK ((watermark = date_trunc('hour'::text, watermark, 'UTC'::text))),
    CONSTRAINT rollup_state_pkey PRIMARY KEY (singleton)
);
ALTER TABLE rollup_state ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON rollup_state FROM proreta_api, proreta_worker;

ALTER TABLE check_results
    ADD CONSTRAINT check_results_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY result_rollups_hourly
    ADD CONSTRAINT result_rollups_hourly_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

-- +goose StatementBegin
CREATE FUNCTION compute_rollups(from_hour timestamp with time zone, to_hour timestamp with time zone) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    AS $$
DECLARE
    lo      timestamptz := date_trunc('hour', from_hour);
    hi      timestamptz := least(date_trunc('hour', to_hour), date_trunc('hour', now()));
    written integer;
BEGIN
    IF lo IS NULL OR hi IS NULL OR lo >= hi THEN
        RETURN 0;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.compute_rollups'));
    INSERT INTO result_rollups_hourly AS r
        (tenant_id, monitor_id, location_id, hour, total, ok_count, latency_sum_ms, latency_histogram, computed_at)
    SELECT c.tenant_id, c.monitor_id, c.location_id, date_trunc('hour', c.checked_at),
           count(*), count(*) FILTER (WHERE c.ok), COALESCE(sum(c.latency_ms), 0),
           ARRAY[
               count(*) FILTER (WHERE c.latency_ms <= 10),
               count(*) FILTER (WHERE c.latency_ms > 10 AND c.latency_ms <= 25),
               count(*) FILTER (WHERE c.latency_ms > 25 AND c.latency_ms <= 50),
               count(*) FILTER (WHERE c.latency_ms > 50 AND c.latency_ms <= 100),
               count(*) FILTER (WHERE c.latency_ms > 100 AND c.latency_ms <= 250),
               count(*) FILTER (WHERE c.latency_ms > 250 AND c.latency_ms <= 500),
               count(*) FILTER (WHERE c.latency_ms > 500 AND c.latency_ms <= 1000),
               count(*) FILTER (WHERE c.latency_ms > 1000 AND c.latency_ms <= 2500),
               count(*) FILTER (WHERE c.latency_ms > 2500 AND c.latency_ms <= 5000),
               count(*) FILTER (WHERE c.latency_ms > 5000 AND c.latency_ms <= 10000),
               count(*) FILTER (WHERE c.latency_ms > 10000)
           ]::integer[],
           now()
    FROM check_results AS c
    WHERE c.checked_at >= lo AND c.checked_at < hi
    GROUP BY c.tenant_id, c.monitor_id, c.location_id, date_trunc('hour', c.checked_at)
    ON CONFLICT (monitor_id, location_id, hour) DO UPDATE
    SET total = EXCLUDED.total,
        ok_count = EXCLUDED.ok_count,
        latency_sum_ms = EXCLUDED.latency_sum_ms,
        latency_histogram = EXCLUDED.latency_histogram,
        computed_at = EXCLUDED.computed_at;
    GET DIAGNOSTICS written = ROW_COUNT;
    RETURN written;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION compute_rollups(timestamp with time zone,timestamp with time zone) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION compute_rollups(timestamp with time zone,timestamp with time zone) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION compute_rollups_catch_up(p_max_hours integer, p_lookback_hours integer) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    AS $$
DECLARE
    closed_end timestamptz := date_trunc('hour', now());
    floor_hour timestamptz := date_trunc('hour', now()) - interval '7 days';
    w          timestamptz;
    lo         timestamptz;
    hi         timestamptz;
    written    integer;
BEGIN
    IF p_max_hours IS NULL OR p_max_hours NOT BETWEEN 1 AND 168
       OR p_lookback_hours IS NULL OR p_lookback_hours NOT BETWEEN 0 AND 24 THEN
        RAISE EXCEPTION 'compute_rollups_catch_up: p_max_hours must be 1..168 and p_lookback_hours 0..24'
            USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.compute_rollups'));
    SELECT s.watermark INTO w FROM rollup_state AS s WHERE s.singleton;
    IF w IS NULL THEN
        SELECT date_trunc('hour', min(c.checked_at)) INTO w
        FROM check_results AS c
        WHERE c.checked_at >= floor_hour AND c.checked_at < closed_end;
        w := COALESCE(w, closed_end);
    END IF;
    w  := least(greatest(w, floor_hour), closed_end);
    hi := least(w + make_interval(hours => p_max_hours), closed_end);
    lo := least(w, closed_end - make_interval(hours => p_lookback_hours));
    written := compute_rollups(lo, hi);
    INSERT INTO rollup_state AS s (singleton, watermark, updated_at) VALUES (true, hi, now())
    ON CONFLICT (singleton) DO UPDATE SET watermark = EXCLUDED.watermark, updated_at = EXCLUDED.updated_at;
    RETURN written;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION compute_rollups_catch_up(integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION compute_rollups_catch_up(integer,integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION detach_expired_partition(p_name text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    SET lock_timeout TO '1s'
    AS $_$
DECLARE
    lo         timestamptz;
    hi         timestamptz;
    incomplete boolean;
BEGIN
    IF p_name IS NULL OR p_name !~ '^check_results_p[0-9]{4}(0[1-9]|1[0-2])$' THEN
        RAISE EXCEPTION 'detach_expired_partition: invalid partition name' USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.ensure_partitions'));
    IF NOT EXISTS (
        SELECT 1
        FROM pg_inherits AS i
        JOIN pg_class AS c ON c.oid = i.inhrelid
        JOIN pg_namespace AS ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
        WHERE i.inhparent = 'public.check_results'::regclass
          AND c.relname = p_name
          AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
    ) THEN
        RETURN false;
    END IF;
    lo := make_timestamptz(substr(p_name, 16, 4)::integer, substr(p_name, 20, 2)::integer, 1, 0, 0, 0, 'UTC');
    hi := lo + interval '1 month';
    IF hi >= now() - interval '30 days' OR lo = date_trunc('month', now()) THEN
        RETURN false;
    END IF;
    IF EXISTS (SELECT 1 FROM rollup_dirty_hours WHERE hour >= lo AND hour < hi) THEN
        RETURN false;
    END IF;
    -- Every raw row of the partition must be in a rollup. A rollup hour counts the raw rows it
    -- saw and never more than exist (raw rows are not deleted before the partition), so equal
    -- totals mean every hour matches. Two scalar aggregates instead of a group by over the whole
    -- partition (one row per monitor, location and hour).
    SELECT (SELECT count(*) FROM check_results WHERE checked_at >= lo AND checked_at < hi)
        IS DISTINCT FROM
           (SELECT COALESCE(sum(total), 0) FROM result_rollups_hourly WHERE hour >= lo AND hour < hi)
    INTO incomplete;
    IF incomplete THEN
        RETURN false;
    END IF;
    EXECUTE format('ALTER TABLE check_results DETACH PARTITION %I', p_name);
    RETURN true;
END
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION detach_expired_partition(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION detach_expired_partition(text) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION drop_detached_partition(p_name text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    SET lock_timeout TO '5s'
    AS $_$
DECLARE
    lo timestamptz;
BEGIN
    IF p_name IS NULL OR p_name !~ '^check_results_p[0-9]{4}(0[1-9]|1[0-2])$' THEN
        RAISE EXCEPTION 'drop_detached_partition: invalid partition name' USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.ensure_partitions'));
    lo := make_timestamptz(substr(p_name, 16, 4)::integer, substr(p_name, 20, 2)::integer, 1, 0, 0, 0, 'UTC');
    IF lo + interval '1 month' >= now() - interval '30 days' OR lo = date_trunc('month', now()) THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class AS c
        JOIN pg_namespace AS ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
        WHERE c.relname = p_name
          AND c.relkind = 'r'
          AND NOT c.relispartition
          AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
    ) THEN
        RETURN false;
    END IF;
    EXECUTE format('DROP TABLE %I', p_name);
    RETURN true;
END
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION drop_detached_partition(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION drop_detached_partition(text) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION ensure_partitions() RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    SET lock_timeout TO '5s'
    AS $_$
DECLARE
    first_month timestamptz := date_trunc('month', now());
    lo          timestamptz;
    hi          timestamptz;
    part        text;
    created     integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('proreta.ensure_partitions'));
    FOR i IN 0..2 LOOP
        lo := first_month + make_interval(months => i);
        hi := first_month + make_interval(months => i + 1);
        part := 'check_results_p' || to_char(lo, 'YYYYMM');
        CONTINUE WHEN to_regclass('public.' || quote_ident(part)) IS NOT NULL;
        EXECUTE format('CREATE TABLE %I PARTITION OF check_results FOR VALUES FROM (%L) TO (%L)', part, lo, hi);
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', part);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', part);
        EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
            WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)$p$, part);
        EXECUTE format('REVOKE ALL ON %I FROM proreta_api, proreta_worker', part);
        EXECUTE format('GRANT SELECT ON %I TO proreta_api', part);
        EXECUTE format('GRANT SELECT, INSERT ON %I TO proreta_worker', part);
        created := created + 1;
    END LOOP;
    RETURN created;
END
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION ensure_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ensure_partitions() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION list_droppable_partitions() RETURNS TABLE(name text, detached boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    AS $_$
    SELECT c.relname::text, NOT c.relispartition
    FROM pg_class AS c
    JOIN pg_namespace AS ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
    WHERE c.relkind = 'r'
      AND c.relname ~ '^check_results_p[0-9]{4}(0[1-9]|1[0-2])$'
      AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
      AND (NOT c.relispartition
           OR EXISTS (SELECT 1 FROM pg_inherits AS i WHERE i.inhrelid = c.oid AND i.inhparent = 'public.check_results'::regclass))
      AND make_timestamptz(substr(c.relname, 16, 4)::integer, substr(c.relname, 20, 2)::integer, 1, 0, 0, 0, 'UTC')
          + interval '1 month' < now() - interval '30 days'
      AND make_timestamptz(substr(c.relname, 16, 4)::integer, substr(c.relname, 20, 2)::integer, 1, 0, 0, 0, 'UTC')
          <> date_trunc('month', now())
    ORDER BY 1
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION list_droppable_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_droppable_partitions() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION purge_result_rollups(p_months integer) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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

-- +goose StatementBegin
CREATE FUNCTION purge_specula_slot_losses(p_days integer) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    v_n integer;
BEGIN
    IF p_days IS NULL OR p_days < 1 THEN
        RAISE EXCEPTION 'purge: slot loss retention below the minimum' USING ERRCODE = '22023';
    END IF;
    WITH gone AS (
        DELETE FROM specula_slot_losses AS l
        WHERE l.ctid IN (
            SELECT e.ctid FROM specula_slot_losses AS e
            WHERE e.hour < now() - make_interval(days => p_days)
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
REVOKE ALL ON FUNCTION purge_specula_slot_losses(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_specula_slot_losses(integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION rollup_drain_dirty(p_max integer) RETURNS TABLE(processed integer, remaining bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    SET "TimeZone" TO 'UTC'
    AS $$
DECLARE
    r    record;
    done integer := 0;
BEGIN
    IF p_max IS NULL OR p_max NOT BETWEEN 1 AND 48 THEN
        RAISE EXCEPTION 'rollup_drain_dirty: p_max must be 1..48' USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.compute_rollups'));
    FOR r IN SELECT d.hour, d.seq FROM rollup_dirty_hours AS d ORDER BY d.hour LIMIT p_max LOOP
        PERFORM compute_rollups(r.hour, r.hour + interval '1 hour');
        DELETE FROM rollup_dirty_hours AS d WHERE d.hour = r.hour AND d.seq = r.seq;
        done := done + 1;
    END LOOP;
    RETURN QUERY SELECT done, (SELECT count(*) FROM rollup_dirty_hours);
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rollup_drain_dirty(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION rollup_drain_dirty(integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION rollup_mark_dirty(p_hour timestamp with time zone) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    h      timestamptz;
    closed timestamptz := date_trunc('hour', now(), 'UTC');
BEGIN
    IF p_hour IS NULL THEN
        RETURN false;
    END IF;
    h := date_trunc('hour', p_hour, 'UTC');
    IF h >= closed OR h < closed - interval '7 days' THEN
        RETURN false;
    END IF;
    INSERT INTO rollup_dirty_hours AS d (hour, seq) VALUES (h, nextval('rollup_dirty_seq'))
    ON CONFLICT (hour) DO UPDATE SET seq = nextval('rollup_dirty_seq'), marked_at = now();
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rollup_mark_dirty(timestamp with time zone) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION rollup_mark_dirty(timestamp with time zone) TO proreta_worker;

-- =============================================================================================
-- Incidents and notifications (internal/incidents, internal/notify)
-- =============================================================================================

-- Incident transitions write notification_deliveries (the outbox) in the same transaction; delivery
-- happens later in jobs through the claim functions.

CREATE TABLE incident_monitors (
    tenant_id uuid NOT NULL,
    incident_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    CONSTRAINT incident_monitors_pkey PRIMARY KEY (incident_id, monitor_id)
);

ALTER TABLE ONLY incident_monitors FORCE ROW LEVEL SECURITY;
CREATE INDEX incident_monitors_monitor_idx ON incident_monitors USING btree (tenant_id, monitor_id);
ALTER TABLE incident_monitors ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_specula_offline_incident_monitors ON incident_monitors FOR INSERT TO proreta_owner WITH CHECK (true);
CREATE POLICY tenant_isolation ON incident_monitors USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON incident_monitors FROM proreta_api, proreta_worker;
GRANT SELECT ON incident_monitors TO proreta_api;
GRANT SELECT, INSERT ON incident_monitors TO proreta_worker;

CREATE TABLE incidents (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    started_at timestamptz NOT NULL,
    resolved_at timestamptz,
    resolve_reason text,
    reason text,
    created_at timestamptz DEFAULT now() NOT NULL,
    monitor_id uuid,
    specula_id uuid,
    opened_late boolean DEFAULT false NOT NULL,
    resolved_late boolean DEFAULT false NOT NULL,
    CONSTRAINT incidents_check CHECK (((resolved_at IS NULL) = (resolve_reason IS NULL))),
    CONSTRAINT incidents_one_owner_check CHECK (((monitor_id IS NULL) OR (specula_id IS NULL))),
    CONSTRAINT incidents_reason_check CHECK (((length(reason) >= 1) AND (length(reason) <= 64))),
    CONSTRAINT incidents_resolve_reason_check CHECK ((resolve_reason = ANY (ARRAY['recovered'::text, 'paused'::text, 'deleted'::text, 'url_changed'::text, 'location_changed'::text, 'revoked'::text]))),
    CONSTRAINT incidents_pkey PRIMARY KEY (id),
    CONSTRAINT incidents_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY incidents FORCE ROW LEVEL SECURITY;
CREATE UNIQUE INDEX incidents_one_open_per_monitor_idx ON incidents USING btree (monitor_id) WHERE ((resolved_at IS NULL) AND (monitor_id IS NOT NULL));
CREATE UNIQUE INDEX incidents_one_open_per_specula_idx ON incidents USING btree (specula_id) WHERE ((resolved_at IS NULL) AND (specula_id IS NOT NULL));
CREATE INDEX incidents_monitor_started_idx ON incidents USING btree (tenant_id, monitor_id, started_at DESC);
CREATE INDEX incidents_tenant_started_idx ON incidents USING btree (tenant_id, started_at DESC);
ALTER TABLE incidents ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_reminder_read ON incidents FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_specula_offline_incident_insert ON incidents FOR INSERT TO proreta_owner WITH CHECK (true);
CREATE POLICY owner_specula_offline_incident_resolve ON incidents FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON incidents USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON incidents FROM proreta_api, proreta_worker;
GRANT SELECT, UPDATE ON incidents TO proreta_api;
GRANT SELECT, INSERT, UPDATE ON incidents TO proreta_worker;

CREATE TABLE monitor_channels (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    channel_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT monitor_channels_pkey PRIMARY KEY (monitor_id, channel_id)
);

ALTER TABLE ONLY monitor_channels FORCE ROW LEVEL SECURITY;
CREATE INDEX monitor_channels_channel_idx ON monitor_channels USING btree (tenant_id, channel_id);
ALTER TABLE monitor_channels ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_reminder_read ON monitor_channels FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON monitor_channels USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON monitor_channels FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, DELETE ON monitor_channels TO proreta_api;
GRANT SELECT ON monitor_channels TO proreta_worker;

CREATE TABLE notification_channels (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    type text NOT NULL,
    config_encrypted bytea,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    name text DEFAULT 'Slack'::text NOT NULL,
    events text[] DEFAULT ARRAY['down'::text, 'recovered'::text, 'tls_expiring'::text] NOT NULL,
    mention text[] DEFAULT '{}'::text[] NOT NULL,
    reminder_minutes integer DEFAULT 0 NOT NULL,
    deleted_at timestamptz,
    project_id uuid,
    content_type text,
    body_template text,
    CONSTRAINT notification_channels_body_template_check CHECK ((char_length(body_template) <= 8192)),
    CONSTRAINT notification_channels_config_encrypted_check CHECK (((length(config_encrypted) >= 1) AND (length(config_encrypted) <= 4096))),
    CONSTRAINT notification_channels_config_present CHECK (((deleted_at IS NOT NULL) OR (config_encrypted IS NOT NULL))),
    CONSTRAINT notification_channels_content_type_check CHECK ((content_type = ANY (ARRAY['json'::text, 'text'::text]))),
    CONSTRAINT notification_channels_events_check CHECK (((cardinality(events) BETWEEN 1 AND 5) AND (events <@ ARRAY['down'::text, 'recovered'::text, 'tls_expiring'::text, 'agent_offline'::text, 'agent_online'::text]))),
    CONSTRAINT notification_channels_mention_check CHECK (((cardinality(mention) <= 3) AND (array_to_string(mention, ' '::text) ~ '^((@here|@channel|<!subteam\^[A-Z0-9]{2,20}>|<@[UW][A-Z0-9]{2,20}>)( |$))*$'::text))),
    CONSTRAINT notification_channels_name_check CHECK (((length(name) >= 1) AND (length(name) <= 60))),
    CONSTRAINT notification_channels_reminder_minutes_check CHECK (((reminder_minutes = 0) OR ((reminder_minutes >= 5) AND (reminder_minutes <= 1440)))),
    CONSTRAINT notification_channels_status_check CHECK ((status = ANY (ARRAY['active'::text, 'failed'::text]))),
    CONSTRAINT notification_channels_type_check CHECK ((type = ANY (ARRAY['slack_webhook'::text, 'webhook'::text]))),
    CONSTRAINT notification_channels_webhook_shape CHECK ((((type = 'webhook'::text) AND (content_type IS NOT NULL) AND (body_template IS NOT NULL) AND (mention = '{}'::text[])) OR ((type = 'slack_webhook'::text) AND (content_type IS NULL) AND (body_template IS NULL)))),
    CONSTRAINT notification_channels_pkey PRIMARY KEY (id),
    CONSTRAINT notification_channels_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY notification_channels FORCE ROW LEVEL SECURITY;
CREATE INDEX notification_channels_project_idx ON notification_channels USING btree (tenant_id, project_id) WHERE (deleted_at IS NULL);
ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_reminder_read ON notification_channels FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON notification_channels USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON notification_channels FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON notification_channels TO proreta_api;
GRANT SELECT ON notification_channels TO proreta_worker;
GRANT UPDATE(status) ON TABLE notification_channels TO proreta_worker;
GRANT UPDATE(updated_at) ON TABLE notification_channels TO proreta_worker;

CREATE TABLE notification_deliveries (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    kind text NOT NULL,
    monitor_id uuid,
    incident_id uuid,
    channel_id uuid NOT NULL,
    payload jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz DEFAULT now() NOT NULL,
    lease_until timestamptz,
    last_error text,
    created_at timestamptz DEFAULT now() NOT NULL,
    sent_at timestamptz,
    claim_token uuid,
    specula_id uuid,
    CONSTRAINT notification_deliveries_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT notification_deliveries_check CHECK (((status = 'in_progress'::text) = (lease_until IS NOT NULL))),
    CONSTRAINT notification_deliveries_check1 CHECK (((status = 'sent'::text) = (sent_at IS NOT NULL))),
    CONSTRAINT notification_deliveries_check2 CHECK (((kind = 'tls_expiring'::text) OR (incident_id IS NOT NULL))),
    CONSTRAINT notification_deliveries_claim_token_check CHECK (((status = 'in_progress'::text) = (claim_token IS NOT NULL))),
    CONSTRAINT notification_deliveries_kind_check CHECK ((kind = ANY (ARRAY['opened'::text, 'resolved'::text, 'tls_expiring'::text, 'reminder'::text, 'agent_offline'::text, 'agent_online'::text]))),
    CONSTRAINT notification_deliveries_last_error_check CHECK ((length(last_error) <= 500)),
    CONSTRAINT notification_deliveries_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT notification_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'in_progress'::text, 'sent'::text, 'failed'::text]))),
    CONSTRAINT notification_deliveries_subject_check CHECK ((((kind = ANY (ARRAY['agent_offline'::text, 'agent_online'::text])) = (specula_id IS NOT NULL)) AND ((specula_id IS NOT NULL) <> (monitor_id IS NOT NULL)))),
    CONSTRAINT notification_deliveries_pkey PRIMARY KEY (id)
);

ALTER TABLE ONLY notification_deliveries FORCE ROW LEVEL SECURITY;
CREATE INDEX notification_deliveries_channel_open_idx ON notification_deliveries USING btree (channel_id) WHERE (status = 'in_progress'::text);
CREATE INDEX notification_deliveries_finished_idx ON notification_deliveries USING btree (created_at) WHERE (status = ANY (ARRAY['sent'::text, 'failed'::text]));
CREATE INDEX notification_deliveries_incident_channel_idx ON notification_deliveries USING btree (incident_id, channel_id, created_at) WHERE (incident_id IS NOT NULL);
CREATE INDEX notification_deliveries_lease_idx ON notification_deliveries USING btree (lease_until) WHERE (status = 'in_progress'::text);
CREATE INDEX notification_deliveries_monitor_open_idx ON notification_deliveries USING btree (monitor_id, created_at, id) WHERE (status = ANY (ARRAY['pending'::text, 'in_progress'::text]));
CREATE INDEX notification_deliveries_ready_idx ON notification_deliveries USING btree (next_attempt_at) WHERE (status = 'pending'::text);
CREATE INDEX notification_deliveries_specula_open_idx ON notification_deliveries USING btree (specula_id, created_at, id) WHERE ((specula_id IS NOT NULL) AND (status = ANY (ARRAY['pending'::text, 'in_progress'::text])));
CREATE INDEX notification_deliveries_tenant_created_idx ON notification_deliveries USING btree (tenant_id, created_at DESC);
ALTER TABLE notification_deliveries ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_delivery_purge ON notification_deliveries FOR DELETE TO proreta_owner USING (true);
CREATE POLICY owner_delivery_read ON notification_deliveries FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_delivery_update ON notification_deliveries FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_reminder_insert ON notification_deliveries FOR INSERT TO proreta_owner WITH CHECK (true);
CREATE POLICY tenant_isolation ON notification_deliveries USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON notification_deliveries FROM proreta_api, proreta_worker;
GRANT SELECT ON notification_deliveries TO proreta_api;
GRANT SELECT, INSERT ON notification_deliveries TO proreta_worker;

ALTER TABLE ONLY incident_monitors
    ADD CONSTRAINT incident_monitors_tenant_id_incident_id_fkey FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents(tenant_id, id);

ALTER TABLE ONLY incident_monitors
    ADD CONSTRAINT incident_monitors_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY incidents
    ADD CONSTRAINT incidents_monitor_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY incidents
    ADD CONSTRAINT incidents_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY monitor_channels
    ADD CONSTRAINT monitor_channels_tenant_id_channel_id_fkey FOREIGN KEY (tenant_id, channel_id) REFERENCES notification_channels(tenant_id, id);

ALTER TABLE ONLY monitor_channels
    ADD CONSTRAINT monitor_channels_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY monitor_state
    ADD CONSTRAINT monitor_state_current_incident_fkey FOREIGN KEY (tenant_id, current_incident_id) REFERENCES incidents(tenant_id, id);

ALTER TABLE ONLY notification_channels
    ADD CONSTRAINT notification_channels_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id);

ALTER TABLE ONLY notification_channels
    ADD CONSTRAINT notification_channels_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_tenant_id_channel_id_fkey FOREIGN KEY (tenant_id, channel_id) REFERENCES notification_channels(tenant_id, id);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_tenant_id_incident_id_fkey FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents(tenant_id, id);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

-- +goose StatementBegin
CREATE FUNCTION claim_deliveries(n integer) RETURNS TABLE(id uuid, tenant_id uuid, kind text, monitor_id uuid, incident_id uuid, channel_id uuid, payload jsonb, attempts integer, created_at timestamp with time zone, claim_token uuid, specula_id uuid)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    UPDATE notification_deliveries AS d
    SET status = CASE WHEN d.attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END,
        attempts = d.attempts + 1,
        last_error = 'lease_expired',
        lease_until = NULL,
        claim_token = NULL
    WHERE d.id IN (
        SELECT e.id FROM notification_deliveries AS e
        WHERE e.status = 'in_progress' AND e.lease_until <= now()
        FOR UPDATE SKIP LOCKED
    );

    UPDATE notification_deliveries AS d
    SET status = 'failed', last_error = 'tenant_deleted'
    WHERE d.id IN (
        SELECT e.id FROM notification_deliveries AS e
        JOIN tenants AS t ON t.id = e.tenant_id
        WHERE e.status = 'pending' AND t.deleted_at IS NOT NULL
        FOR UPDATE OF e SKIP LOCKED
    );

    WITH eligible AS (
        SELECT d.id, d.created_at,
               row_number() OVER (PARTITION BY d.channel_id ORDER BY d.created_at, d.id) AS rn
        FROM notification_deliveries AS d
        WHERE d.status = 'pending'
          AND d.next_attempt_at <= now()
          AND EXISTS (
              SELECT 1 FROM tenants AS t
              WHERE t.id = d.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
          )
          AND NOT EXISTS (
              SELECT 1
              FROM notification_deliveries AS o
              WHERE o.monitor_id IS NOT DISTINCT FROM d.monitor_id
                AND o.specula_id IS NOT DISTINCT FROM d.specula_id
                AND o.channel_id = d.channel_id
                AND o.status IN ('pending', 'in_progress')
                AND (o.created_at, o.id) < (d.created_at, d.id)
          )
          AND NOT EXISTS (
              SELECT 1 FROM notification_deliveries AS p
              WHERE p.channel_id = d.channel_id AND p.status = 'in_progress'
          )
    ), picked AS (
        SELECT e.id FROM eligible AS e
        WHERE e.rn <= 2
        ORDER BY e.created_at, e.id
        LIMIT least(greatest(n, 0), 100)
    ), ready AS (
        -- Locked after picking; a row another claimer took meanwhile is skipped or no longer pending.
        SELECT d.id
        FROM notification_deliveries AS d
        JOIN picked ON picked.id = d.id
        WHERE d.status = 'pending'
        FOR UPDATE OF d SKIP LOCKED
    )
    UPDATE notification_deliveries AS d
    SET status = 'in_progress',
        lease_until = now() + interval '2 minutes',
        claim_token = gen_random_uuid()
    FROM ready
    WHERE d.id = ready.id
    RETURNING d.id, d.tenant_id, d.kind, d.monitor_id, d.incident_id, d.channel_id, d.payload, d.attempts, d.created_at,
              d.claim_token, d.specula_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_deliveries(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_deliveries(integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION enqueue_incident_reminders(p_limit integer) RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT pg_advisory_xact_lock(hashtext('proreta.enqueue_incident_reminders'));

    WITH due AS (
        SELECT i.tenant_id, i.id AS incident_id, i.monitor_id, i.started_at, c.id AS channel_id, m.name AS monitor_name
        FROM incidents AS i
        JOIN tenants AS t ON t.id = i.tenant_id
        JOIN monitors AS m ON m.tenant_id = i.tenant_id AND m.id = i.monitor_id
        JOIN notification_channels AS c ON c.tenant_id = i.tenant_id
        WHERE i.resolved_at IS NULL
          AND t.status = 'active' AND t.deleted_at IS NULL
          AND m.notifications_enabled AND NOT m.paused AND m.deleted_at IS NULL
          AND c.deleted_at IS NULL AND c.status = 'active' AND c.reminder_minutes > 0
          AND 'down' = ANY (c.events)
          AND (
              (EXISTS (SELECT 1 FROM monitor_channels AS mc
                       WHERE mc.tenant_id = i.tenant_id AND mc.monitor_id = m.id AND mc.channel_id = c.id)
               AND (c.project_id IS NULL OR c.project_id = m.project_id))
              OR (NOT EXISTS (SELECT 1 FROM monitor_channels AS mc
                              JOIN notification_channels AS c2 ON c2.tenant_id = mc.tenant_id AND c2.id = mc.channel_id
                              WHERE mc.tenant_id = i.tenant_id AND mc.monitor_id = m.id
                                AND (c2.project_id IS NULL OR c2.project_id = m.project_id))
                  AND c.project_id = m.project_id)
          )
          AND EXISTS (SELECT 1 FROM notification_deliveries AS d
                      WHERE d.incident_id = i.id AND d.channel_id = c.id AND d.kind = 'opened' AND d.status <> 'failed')
          AND (SELECT max(d.created_at) FROM notification_deliveries AS d
               WHERE d.incident_id = i.id AND d.channel_id = c.id AND d.kind IN ('opened', 'reminder'))
              <= now() - make_interval(mins => c.reminder_minutes)
        ORDER BY i.started_at, i.id, c.id
        LIMIT least(greatest(p_limit, 0), 1000)
    ),
    queued AS (
        INSERT INTO notification_deliveries (id, tenant_id, kind, monitor_id, incident_id, channel_id, payload)
        SELECT uuidv7_now(),
               due.tenant_id, 'reminder', due.monitor_id, due.incident_id, due.channel_id,
               jsonb_build_object(
                   'monitorId', due.monitor_id,
                   'monitorName', due.monitor_name,
                   'incidentId', due.incident_id,
                   'startedAt', due.started_at,
                   'durationSeconds', greatest(floor(extract(epoch FROM now() - due.started_at)), 0)::bigint
               )
        FROM due
        RETURNING 1
    )
    SELECT count(*)::integer FROM queued
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION enqueue_incident_reminders(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION enqueue_incident_reminders(integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION fail_deleted_channel_deliveries(p_channel_id uuid) RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH failed AS (
        UPDATE notification_deliveries AS d
        SET status = 'failed', last_error = 'channel_deleted'
        WHERE d.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
          AND d.channel_id = p_channel_id
          AND d.status = 'pending'
          AND EXISTS (
              SELECT 1 FROM notification_channels AS c
              WHERE c.tenant_id = d.tenant_id AND c.id = p_channel_id AND c.deleted_at IS NOT NULL
          )
        RETURNING 1
    )
    SELECT count(*)::integer FROM failed
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION fail_deleted_channel_deliveries(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION fail_deleted_channel_deliveries(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION finish_delivery(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamp with time zone) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending', 'throttled') THEN
        RAISE EXCEPTION 'finish_delivery: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status IN ('pending', 'throttled') AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_delivery: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE notification_deliveries AS d
    SET status = CASE
            WHEN p_status = 'throttled' THEN
                CASE WHEN d.created_at < now() - interval '1 hour' THEN 'failed' ELSE 'pending' END
            ELSE p_status
        END,
        attempts = d.attempts + CASE
            WHEN p_status = 'throttled' AND d.created_at >= now() - interval '1 hour' THEN 0
            ELSE 1
        END,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        sent_at = CASE WHEN p_status = 'sent' THEN now() END,
        next_attempt_at = CASE WHEN p_status IN ('pending', 'throttled') THEN p_retry_at ELSE d.next_attempt_at END
    WHERE d.id = p_id AND d.status = 'in_progress' AND d.claim_token = p_token;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION finish_delivery(uuid,uuid,text,text,timestamp with time zone) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION finish_delivery(uuid,uuid,text,text,timestamp with time zone) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION outbox_pending() RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT count(*) FROM notification_deliveries WHERE status = 'pending'
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION outbox_pending() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION outbox_pending() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION purge_notification_deliveries(p_days integer) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
              -- the opened row of a running incident routes its reminders and its recovered message
              AND NOT EXISTS (SELECT 1 FROM incidents AS i WHERE i.tenant_id = e.tenant_id AND i.id = e.incident_id AND i.resolved_at IS NULL)
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

-- +goose StatementBegin
CREATE FUNCTION webhook_delivery_context(p_monitor_id uuid) RETURNS TABLE(monitor_type text, project_name text, organization_name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT m.type, p.name, t.name
    FROM monitors AS m
    JOIN projects AS p ON p.tenant_id = m.tenant_id AND p.id = m.project_id
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
      AND m.id = p_monitor_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION webhook_delivery_context(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_delivery_context(uuid) TO proreta_worker;

-- =============================================================================================
-- Heartbeats
-- =============================================================================================

-- Heartbeat tokens (only a hash is stored) and the functions for pings and missed beats.

CREATE TABLE heartbeat_tokens (
    tenant_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    waiting_since timestamptz DEFAULT now() NOT NULL,
    last_ping_at timestamptz,
    applied_ping_at timestamptz,
    missed_deadline timestamptz,
    CONSTRAINT heartbeat_tokens_token_hash_check CHECK ((length(token_hash) = 32)),
    CONSTRAINT heartbeat_tokens_pkey PRIMARY KEY (monitor_id),
    CONSTRAINT heartbeat_tokens_tenant_id_monitor_id_key UNIQUE (tenant_id, monitor_id),
    CONSTRAINT heartbeat_tokens_token_hash_key UNIQUE (token_hash)
);

ALTER TABLE ONLY heartbeat_tokens FORCE ROW LEVEL SECURITY;
ALTER TABLE heartbeat_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_heartbeat_ping ON heartbeat_tokens FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_heartbeat_read ON heartbeat_tokens FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON heartbeat_tokens USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON heartbeat_tokens FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON heartbeat_tokens TO proreta_api;
GRANT SELECT ON heartbeat_tokens TO proreta_worker;
GRANT UPDATE(token_hash) ON TABLE heartbeat_tokens TO proreta_api;
GRANT UPDATE(created_at) ON TABLE heartbeat_tokens TO proreta_api;
GRANT UPDATE(waiting_since) ON TABLE heartbeat_tokens TO proreta_api;
GRANT UPDATE(applied_ping_at) ON TABLE heartbeat_tokens TO proreta_api;
GRANT UPDATE(applied_ping_at) ON TABLE heartbeat_tokens TO proreta_worker;
GRANT UPDATE(missed_deadline) ON TABLE heartbeat_tokens TO proreta_worker;

ALTER TABLE ONLY heartbeat_tokens
    ADD CONSTRAINT heartbeat_tokens_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

-- +goose StatementBegin
CREATE FUNCTION heartbeat_ping(p_token_hash bytea) RETURNS TABLE(tenant_id uuid, monitor_id uuid, recorded boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    v_tenant  uuid;
    v_monitor uuid;
BEGIN
    IF p_token_hash IS NULL OR length(p_token_hash) <> 32 THEN
        RETURN;
    END IF;
    PERFORM 1 FROM heartbeat_tokens AS h WHERE h.token_hash = p_token_hash FOR UPDATE;
    UPDATE heartbeat_tokens AS h
    SET last_ping_at = greatest(h.last_ping_at, clock_timestamp())
    FROM monitors AS m, tenants AS t
    WHERE h.token_hash = p_token_hash
      AND m.tenant_id = h.tenant_id AND m.id = h.monitor_id
      AND NOT m.paused AND m.deleted_at IS NULL
      AND t.id = h.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
    RETURNING h.tenant_id, h.monitor_id INTO v_tenant, v_monitor;
    IF FOUND THEN
        RETURN QUERY SELECT v_tenant, v_monitor, true;
        RETURN;
    END IF;
    RETURN QUERY
        SELECT h.tenant_id, h.monitor_id, false
        FROM heartbeat_tokens AS h
        WHERE h.token_hash = p_token_hash;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION heartbeat_ping(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION heartbeat_ping(bytea) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION list_due_heartbeats(n integer) RETURNS TABLE(tenant_id uuid, monitor_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT h.tenant_id, h.monitor_id
    FROM heartbeat_tokens AS h
    JOIN monitors AS m ON m.tenant_id = h.tenant_id AND m.id = h.monitor_id
    JOIN tenants AS t ON t.id = h.tenant_id
    CROSS JOIN LATERAL (
        SELECT greatest(h.waiting_since, coalesce(h.last_ping_at, h.waiting_since))
               + make_interval(secs => m.interval_seconds
                   + CASE WHEN m.type = 'heartbeat' THEN (m.config ->> 'graceSeconds')::numeric::integer ELSE 0 END) AS deadline
    ) AS d
    WHERE m.type = 'heartbeat'
      AND NOT m.paused
      AND m.deleted_at IS NULL
      AND t.status = 'active'
      AND t.deleted_at IS NULL
      AND ((h.last_ping_at IS NOT NULL AND (h.applied_ping_at IS NULL OR h.last_ping_at > h.applied_ping_at))
           OR (d.deadline < now() AND (h.missed_deadline IS NULL OR h.missed_deadline < d.deadline)))
    ORDER BY d.deadline, h.monitor_id
    LIMIT least(greatest(n, 0), 1000)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION list_due_heartbeats(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_due_heartbeats(integer) TO proreta_worker;

-- =============================================================================================
-- Status pages and domains (internal/statuspages)
-- =============================================================================================

-- Public status pages, their components, the rendered snapshots, released slugs and custom domains.

CREATE TABLE status_page_components (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    page_id uuid NOT NULL,
    monitor_id uuid NOT NULL,
    public_name text NOT NULL,
    group_name text,
    "position" integer NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT status_page_components_group_name_check CHECK (((group_name IS NULL) OR ((char_length(group_name) >= 1) AND (char_length(group_name) <= 60)))),
    CONSTRAINT status_page_components_position_check CHECK ((("position" >= 0) AND ("position" <= 999))),
    CONSTRAINT status_page_components_public_name_check CHECK (((char_length(public_name) >= 1) AND (char_length(public_name) <= 60))),
    CONSTRAINT status_page_components_page_id_monitor_id_key UNIQUE (page_id, monitor_id),
    CONSTRAINT status_page_components_page_id_position_key UNIQUE (page_id, "position"),
    CONSTRAINT status_page_components_pkey PRIMARY KEY (id),
    CONSTRAINT status_page_components_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY status_page_components FORCE ROW LEVEL SECURITY;
CREATE INDEX status_page_components_monitor_idx ON status_page_components USING btree (tenant_id, monitor_id);
ALTER TABLE status_page_components ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON status_page_components USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON status_page_components FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON status_page_components TO proreta_api;
GRANT SELECT, DELETE ON status_page_components TO proreta_worker;

CREATE TABLE status_page_domains (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    page_id uuid NOT NULL,
    host text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    token text NOT NULL,
    verified_at timestamptz,
    last_checked_at timestamptz,
    last_proof_at timestamptz,
    check_failures integer DEFAULT 0 NOT NULL,
    last_error text,
    disabled_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    released_at timestamptz,
    CONSTRAINT status_page_domains_check_failures_check CHECK ((check_failures >= 0)),
    CONSTRAINT status_page_domains_host_check CHECK (((host = lower(host)) AND ((char_length(host) >= 4) AND (char_length(host) <= 253)) AND (host ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'::text))),
    CONSTRAINT status_page_domains_last_error_check CHECK (((last_error IS NULL) OR (last_error = ANY (ARRAY['txt-missing'::text, 'txt-mismatch'::text, 'cname-missing'::text, 'cname-wrong'::text, 'dns-error'::text])))),
    CONSTRAINT status_page_domains_released_not_primary CHECK (((released_at IS NULL) OR (NOT is_primary))),
    CONSTRAINT status_page_domains_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'verified'::text, 'active'::text, 'failed'::text]))),
    CONSTRAINT status_page_domains_token_check CHECK ((token ~ '^[0-9a-f]{32}$'::text)),
    CONSTRAINT status_page_domains_pkey PRIMARY KEY (id),
    CONSTRAINT status_page_domains_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY status_page_domains FORCE ROW LEVEL SECURITY;
CREATE INDEX status_page_domains_due_idx ON status_page_domains USING btree (last_checked_at NULLS FIRST) WHERE (released_at IS NULL);
CREATE UNIQUE INDEX status_page_domains_host_idx ON status_page_domains USING btree (host) WHERE ((state = ANY (ARRAY['verified'::text, 'active'::text])) AND (released_at IS NULL));
CREATE UNIQUE INDEX status_page_domains_primary_idx ON status_page_domains USING btree (page_id) WHERE (is_primary AND (released_at IS NULL));
CREATE UNIQUE INDEX status_page_domains_tenant_host_idx ON status_page_domains USING btree (tenant_id, host) WHERE (released_at IS NULL);
CREATE INDEX status_page_domains_tenant_idx ON status_page_domains USING btree (tenant_id, page_id) WHERE (released_at IS NULL);
ALTER TABLE status_page_domains ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_domain_read ON status_page_domains FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_domain_update ON status_page_domains FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON status_page_domains USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON status_page_domains FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON status_page_domains TO proreta_api;

CREATE TABLE status_page_released_slugs (
    tenant_id uuid NOT NULL,
    slug text NOT NULL,
    released_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT status_page_released_slugs_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{2,62}$'::text)),
    CONSTRAINT status_page_released_slugs_pkey PRIMARY KEY (tenant_id, slug)
);

ALTER TABLE ONLY status_page_released_slugs FORCE ROW LEVEL SECURITY;
CREATE INDEX status_page_released_slugs_slug_idx ON status_page_released_slugs USING btree (slug, released_at);
ALTER TABLE status_page_released_slugs ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_slug_hold_read ON status_page_released_slugs FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON status_page_released_slugs USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON status_page_released_slugs FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON status_page_released_slugs TO proreta_api;

CREATE TABLE status_page_snapshots (
    page_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    slug text NOT NULL,
    payload jsonb NOT NULL,
    etag text NOT NULL,
    generated_at timestamptz NOT NULL,
    CONSTRAINT status_page_snapshots_etag_check CHECK ((etag ~ '^[0-9a-f]{32}$'::text)),
    CONSTRAINT status_page_snapshots_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT status_page_snapshots_pkey PRIMARY KEY (page_id)
);

ALTER TABLE ONLY status_page_snapshots FORCE ROW LEVEL SECURITY;
ALTER TABLE status_page_snapshots ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_statuspage_read ON status_page_snapshots FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON status_page_snapshots USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON status_page_snapshots FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON status_page_snapshots TO proreta_api, proreta_worker;

CREATE TABLE status_pages (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    logo_url text,
    accent_color text,
    password_hash text,
    published boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    project_id uuid,
    CONSTRAINT status_pages_accent_color_check CHECK (((accent_color IS NULL) OR (accent_color ~ '^#[0-9a-f]{6}$'::text))),
    CONSTRAINT status_pages_deleted_unpublished CHECK (((deleted_at IS NULL) OR (NOT published))),
    CONSTRAINT status_pages_description_check CHECK ((char_length(description) <= 500)),
    CONSTRAINT status_pages_logo_url_check CHECK (((logo_url IS NULL) OR ((char_length(logo_url) <= 500) AND (logo_url ~ '^https://[^\s/?#@]+'::text)))),
    CONSTRAINT status_pages_password_hash_check CHECK (((password_hash IS NULL) OR (password_hash ~~ '$argon2id$%'::text))),
    CONSTRAINT status_pages_slug_check CHECK (((slug ~ '^[a-z0-9][a-z0-9-]{2,62}$'::text) AND (slug <> ALL (ARRAY['admin'::text, 'api'::text, 'app'::text, 'assets'::text, 'auth'::text, 'badge'::text, 'dashboard'::text, 'docs'::text, 'embed'::text, 'feed'::text, 'health'::text, 'healthz'::text, 'help'::text, 'login'::text, 'logout'::text, 'metrics'::text, 'proreta'::text, 'public'::text, 'readyz'::text, 'register'::text, 'robots'::text, 'root'::text, 'rss'::text, 'settings'::text, 'signup'::text, 'static'::text, 'status'::text, 'support'::text, 'system'::text, 'www'::text])))),
    CONSTRAINT status_pages_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 80))),
    CONSTRAINT status_pages_pkey PRIMARY KEY (id),
    CONSTRAINT status_pages_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY status_pages FORCE ROW LEVEL SECURITY;
CREATE INDEX status_pages_project_idx ON status_pages USING btree (tenant_id, project_id) WHERE ((deleted_at IS NULL) AND (project_id IS NOT NULL));
CREATE UNIQUE INDEX status_pages_slug_idx ON status_pages USING btree (slug) WHERE (deleted_at IS NULL);
CREATE INDEX status_pages_tenant_idx ON status_pages USING btree (tenant_id) WHERE (deleted_at IS NULL);
ALTER TABLE status_pages ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_statuspage_read ON status_pages FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON status_pages USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON status_pages FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON status_pages TO proreta_api;
GRANT SELECT ON status_pages TO proreta_worker;

ALTER TABLE ONLY status_page_components
    ADD CONSTRAINT status_page_components_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id);

ALTER TABLE ONLY status_page_components
    ADD CONSTRAINT status_page_components_tenant_id_page_id_fkey FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY status_page_domains
    ADD CONSTRAINT status_page_domains_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY status_page_domains
    ADD CONSTRAINT status_page_domains_tenant_id_page_id_fkey FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY status_page_released_slugs
    ADD CONSTRAINT status_page_released_slugs_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY status_page_snapshots
    ADD CONSTRAINT status_page_snapshots_tenant_id_page_id_fkey FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE ONLY status_pages
    ADD CONSTRAINT status_pages_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id);

ALTER TABLE ONLY status_pages
    ADD CONSTRAINT status_pages_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

-- +goose StatementBegin
CREATE FUNCTION status_page_domain_allowed(p_host text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
GRANT EXECUTE ON FUNCTION status_page_domain_allowed(text) TO proreta_statuspage;

-- +goose StatementBegin
CREATE FUNCTION status_page_domain_counts() RETURNS TABLE(state text, n bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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

-- +goose StatementBegin
CREATE FUNCTION status_page_domain_held_by_other(p_host text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT EXISTS (
        SELECT 1 FROM status_page_domains AS d
        WHERE d.host = p_host AND d.state IN ('verified', 'active') AND d.released_at IS NULL
          AND d.tenant_id IS DISTINCT FROM NULLIF(current_setting('app.tenant_id', true), '')::uuid)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domain_held_by_other(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_held_by_other(text) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION status_page_domain_lookup(p_host text) RETURNS TABLE(slug text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
GRANT EXECUTE ON FUNCTION status_page_domain_lookup(text) TO proreta_statuspage;

-- +goose StatementBegin
CREATE FUNCTION status_page_domain_record_check(p_tenant uuid, p_id uuid, p_txt text, p_cname text, p_error text) RETURNS TABLE(state text, events text[])
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
    -- the worker works across tenants; the api only inside its own tenant transaction
    IF (v_ctx IS NULL AND session_user = 'proreta_api') OR (v_ctx IS NOT NULL AND v_ctx <> p_tenant::text) THEN
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
    ELSIF d.state = 'failed' AND p_txt = 'present' THEN
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
REVOKE ALL ON FUNCTION status_page_domain_record_check(uuid,uuid,text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_record_check(uuid,uuid,text,text,text) TO proreta_worker;
GRANT EXECUTE ON FUNCTION status_page_domain_record_check(uuid,uuid,text,text,text) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION status_page_domain_takeover(p_host text, p_claimant uuid, p_dns_tokens text[]) RETURNS TABLE(granted boolean, previous_tenant_id uuid, previous_page_id uuid)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION status_page_domain_takeover(text,uuid,text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domain_takeover(text,uuid,text[]) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION status_page_domains_due(p_limit integer) RETURNS TABLE(tenant_id uuid, id uuid, page_id uuid, host text, token text, state text, created_at timestamp with time zone, last_proof_at timestamp with time zone, check_failures integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
BEGIN
    RETURN QUERY
    SELECT d.tenant_id, d.id, d.page_id, d.host, d.token, d.state, d.created_at, d.last_proof_at, d.check_failures
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

-- +goose StatementBegin
CREATE FUNCTION status_page_domains_export(p_all boolean) RETURNS TABLE(host text, state text, is_primary boolean, disabled boolean, tenant_slug text, page_slug text, verified_at timestamp with time zone)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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

-- +goose StatementBegin
CREATE FUNCTION status_page_published_pages() RETURNS TABLE(tenant_id uuid, page_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT p.tenant_id, p.id
    FROM status_pages AS p
    JOIN tenants AS t ON t.id = p.tenant_id
    WHERE p.published AND p.deleted_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
    ORDER BY p.tenant_id, p.id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_published_pages() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_published_pages() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION status_page_slug_held(p_slug text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT EXISTS (
        SELECT 1 FROM status_page_released_slugs AS r
        WHERE r.slug = p_slug
          AND r.tenant_id IS DISTINCT FROM NULLIF(current_setting('app.tenant_id', true), '')::uuid
          AND r.released_at > now() - interval '30 days')
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_slug_held(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_slug_held(text) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION status_page_snapshot(p_slug text) RETURNS TABLE(payload jsonb, etag text, generated_at timestamp with time zone, password_hash text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT s.payload, s.etag, s.generated_at, p.password_hash
    FROM status_pages AS p
    JOIN status_page_snapshots AS s ON s.tenant_id = p.tenant_id AND s.page_id = p.id AND s.slug = p.slug
    JOIN tenants AS t ON t.id = p.tenant_id
    WHERE p.slug = p_slug
      AND p.published AND p.deleted_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_snapshot(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_snapshot(text) TO proreta_statuspage;

-- +goose StatementBegin
CREATE FUNCTION status_pages_release_domains() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'public', 'pg_temp'
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

CREATE TRIGGER status_pages_release_domains AFTER UPDATE OF deleted_at ON status_pages FOR EACH ROW WHEN (((old.deleted_at IS NULL) AND (new.deleted_at IS NOT NULL))) EXECUTE FUNCTION status_pages_release_domains();

-- =============================================================================================
-- Invitations and OAuth domains
-- =============================================================================================

-- Invitations to organizations and projects, and the e-mail domains an organization allows for OAuth sign-in.

CREATE TABLE invitations (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    role text NOT NULL,
    label text DEFAULT ''::text NOT NULL,
    token_hash bytea NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    accepted_by uuid,
    revoked_at timestamptz,
    project_id uuid,
    CONSTRAINT invitations_accepted_pair CHECK (((accepted_at IS NULL) = (accepted_by IS NULL))),
    CONSTRAINT invitations_expiry CHECK ((expires_at > created_at)),
    CONSTRAINT invitations_label_check CHECK ((length(label) <= 80)),
    CONSTRAINT invitations_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'maintainer'::text, 'member'::text, 'viewer'::text]))),
    CONSTRAINT invitations_single_outcome CHECK (((accepted_at IS NULL) OR (revoked_at IS NULL))),
    CONSTRAINT invitations_token_hash_check CHECK ((length(token_hash) = 32)),
    CONSTRAINT invitations_pkey PRIMARY KEY (id),
    CONSTRAINT invitations_tenant_id_id_key UNIQUE (tenant_id, id),
    CONSTRAINT invitations_token_hash_key UNIQUE (token_hash)
);

ALTER TABLE ONLY invitations FORCE ROW LEVEL SECURITY;
CREATE INDEX invitations_project_idx ON invitations USING btree (tenant_id, project_id, created_at DESC) WHERE (project_id IS NOT NULL);
CREATE INDEX invitations_tenant_created_idx ON invitations USING btree (tenant_id, created_at DESC);
ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_invitation_lookup ON invitations FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON invitations USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON invitations FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE ON invitations TO proreta_api;

CREATE TABLE org_auth_domains (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    domain text NOT NULL,
    default_role text DEFAULT 'viewer'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT org_auth_domains_default_role_check CHECK ((default_role = ANY (ARRAY['maintainer'::text, 'member'::text, 'viewer'::text]))),
    CONSTRAINT org_auth_domains_domain_check CHECK (((length(domain) BETWEEN 3 AND 253) AND (domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'::text))),
    CONSTRAINT org_auth_domains_domain_key UNIQUE (domain),
    CONSTRAINT org_auth_domains_pkey PRIMARY KEY (id),
    CONSTRAINT org_auth_domains_tenant_id_id_key UNIQUE (tenant_id, id)
);

ALTER TABLE ONLY org_auth_domains FORCE ROW LEVEL SECURITY;
CREATE INDEX org_auth_domains_tenant_idx ON org_auth_domains USING btree (tenant_id, domain);
ALTER TABLE org_auth_domains ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_domain_lookup ON org_auth_domains FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON org_auth_domains USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON org_auth_domains FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON org_auth_domains TO proreta_api;

ALTER TABLE ONLY invitations
    ADD CONSTRAINT invitations_accepted_by_fkey FOREIGN KEY (accepted_by) REFERENCES users(id);

ALTER TABLE ONLY invitations
    ADD CONSTRAINT invitations_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);

ALTER TABLE ONLY invitations
    ADD CONSTRAINT invitations_project_fk FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id);

ALTER TABLE ONLY invitations
    ADD CONSTRAINT invitations_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY org_auth_domains
    ADD CONSTRAINT org_auth_domains_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);

ALTER TABLE ONLY org_auth_domains
    ADD CONSTRAINT org_auth_domains_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

-- +goose StatementBegin
CREATE FUNCTION auth_domain_lookup(p_domain text) RETURNS TABLE(tenant_id uuid, default_role text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT d.tenant_id, d.default_role
    FROM org_auth_domains AS d
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.domain = p_domain
      AND d.enabled
      AND t.status = 'active'
      AND t.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_domain_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_domain_lookup(text) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION auth_invitation_lookup(p_token_hash bytea) RETURNS TABLE(invitation_id uuid, tenant_id uuid, tenant_name text, role text, expires_at timestamp with time zone, project_id uuid, project_name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT i.id, i.tenant_id, t.name, i.role, i.expires_at, i.project_id, p.name
    FROM invitations AS i
    JOIN tenants AS t ON t.id = i.tenant_id
    LEFT JOIN projects AS p ON p.tenant_id = i.tenant_id AND p.id = i.project_id
    WHERE i.token_hash = p_token_hash
      AND length(p_token_hash) = 32
      AND i.accepted_at IS NULL
      AND i.revoked_at IS NULL
      AND i.expires_at > now()
      AND t.status = 'active'
      AND t.deleted_at IS NULL
      AND (i.project_id IS NULL OR p.deleted_at IS NULL)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_invitation_lookup(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_invitation_lookup(bytea) TO proreta_api;

-- =============================================================================================
-- Plans
-- =============================================================================================

-- Plans carry the limits and features of a tenant; tenants.plan_key references them.

CREATE TABLE plans (
    key text NOT NULL,
    name text NOT NULL,
    limits jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    features text[] DEFAULT '{}'::text[] NOT NULL,
    is_system boolean DEFAULT false NOT NULL,
    CONSTRAINT plans_features_names CHECK (((array_position(features, NULL::text) IS NULL) AND ((cardinality(features) = 0) OR (array_to_string(features, ','::text) ~ '^[a-z0-9_-]{1,64}(,[a-z0-9_-]{1,64})*$'::text)))),
    CONSTRAINT plans_key_check CHECK ((key ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text)),
    CONSTRAINT plans_limits_check CHECK ((jsonb_typeof(limits) = 'object'::text)),
    CONSTRAINT plans_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200))),
    CONSTRAINT plans_pkey PRIMARY KEY (key)
);
REVOKE ALL ON plans FROM proreta_api, proreta_worker;
GRANT SELECT ON plans TO proreta_api;
GRANT SELECT ON plans TO proreta_worker;

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_plan_key_fkey FOREIGN KEY (plan_key) REFERENCES plans(key);

-- +goose StatementBegin
CREATE FUNCTION plan_limits_valid(l jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'public', 'pg_temp'
    AS $_$
    SELECT jsonb_typeof(l) = 'object'
        AND (SELECT count(*) FROM jsonb_object_keys(l)) = 8
        AND NOT EXISTS (
            SELECT 1
            FROM unnest(ARRAY['max_monitors', 'max_heartbeats', 'max_channels', 'max_members', 'max_projects', 'max_status_pages', 'max_agents', 'max_status_page_domains']) AS k
            WHERE jsonb_typeof(l -> k) IS DISTINCT FROM 'number'
               OR CASE WHEN (l ->> k) ~ '^(0|[1-9][0-9]{0,9})$' THEN (l ->> k)::bigint > 2147483647 ELSE true END
        )
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION plan_limits_valid(jsonb) FROM PUBLIC;

-- =============================================================================================
-- Platform admin (internal/admin)
-- =============================================================================================

-- Everything the platform admin panel does goes through admin_* functions that check the actor and
-- write an audit row in the same transaction.

-- +goose StatementBegin
CREATE FUNCTION admin_audit(p_tenant_id uuid, p_actor uuid, p_audit_id uuid, p_action text, p_entity_type text, p_entity_id text, p_metadata jsonb, p_ip inet) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    caller text := current_setting('app.tenant_id', true);
BEGIN
    PERFORM set_config('app.tenant_id', p_tenant_id::text, true);
    INSERT INTO audit_log (id, tenant_id, actor_user_id, action, entity_type, entity_id, metadata, ip)
    VALUES (p_audit_id, p_tenant_id, p_actor, p_action, p_entity_type, p_entity_id, p_metadata, p_ip);
    PERFORM set_config('app.tenant_id', COALESCE(caller, ''), true);
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_audit(uuid,uuid,uuid,text,text,text,jsonb,inet) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION admin_create_tenant(p_actor uuid, p_id uuid, p_slug text, p_name text, p_plan_key text, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_create_tenant(uuid,uuid,text,text,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_create_tenant(uuid,uuid,text,text,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_delete_tenant(p_actor uuid, p_tenant_id uuid, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    v_status  text;
    v_slug    text;
    v_plan    text;
    v_deleted timestamptz;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT t.status, t.slug, t.plan_key, t.deleted_at INTO v_status, v_slug, v_plan, v_deleted
    FROM tenants AS t WHERE t.id = p_tenant_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RETURN false;
    END IF;
    IF v_slug = 'default' THEN
        RAISE EXCEPTION 'admin: the default tenant cannot be deleted' USING ERRCODE = 'PRA05';
    END IF;
    IF EXISTS (SELECT 1 FROM memberships AS m WHERE m.tenant_id = p_tenant_id AND m.user_id = p_actor) THEN
        RAISE EXCEPTION 'admin: cannot delete a tenant the admin belongs to' USING ERRCODE = 'PRA06';
    END IF;
    UPDATE tenants SET deleted_at = now(), status = 'suspended', updated_at = now() WHERE id = p_tenant_id;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id, 'admin.organization.deleted', 'organization', p_tenant_id::text,
                        jsonb_build_object('status', v_status, 'plan', v_plan), p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_delete_tenant(uuid,uuid,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_delete_tenant(uuid,uuid,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_delete_user(p_actor uuid, p_user_id uuid, p_audit_id uuid, p_ip inet) RETURNS TABLE(changed boolean, sessions_revoked integer, memberships_removed integer, identities_removed integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    caller   text := current_setting('app.tenant_id', true);
    v_status text;
    v_admin  boolean;
    v_system boolean;
    v_tenant uuid;
    t        uuid;
    n        integer;
    v_sessions   integer;
    v_members    integer := 0;
    v_identities integer;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT u.status, u.is_platform_admin, u.is_system_admin INTO v_status, v_admin, v_system
    FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_status = 'deleted' THEN
        RETURN QUERY SELECT false, 0, 0, 0;
        RETURN;
    END IF;
    IF v_system THEN
        RAISE EXCEPTION 'admin: the system admin cannot be deleted' USING ERRCODE = 'PRA07';
    END IF;
    -- also covers deleting oneself: the actor is a platform admin
    IF v_admin THEN
        RAISE EXCEPTION 'admin: revoke platform admin before deleting the user' USING ERRCODE = 'PRA36';
    END IF;
    -- an organization must keep an active owner (the predicate of admin_set_user_status)
    IF EXISTS (
        SELECT 1 FROM memberships AS m JOIN tenants AS tn ON tn.id = m.tenant_id
        WHERE m.user_id = p_user_id AND m.role = 'owner' AND tn.deleted_at IS NULL
          AND NOT EXISTS (SELECT 1 FROM memberships AS o JOIN users AS ou ON ou.id = o.user_id
                          WHERE o.tenant_id = m.tenant_id AND o.role = 'owner' AND o.user_id <> p_user_id AND ou.status = 'active')) THEN
        RAISE EXCEPTION 'admin: the user is the only active owner of an organization' USING ERRCODE = 'PRA35';
    END IF;
    -- the audit tenant is read while the memberships still exist
    v_tenant := admin_user_audit_tenant(p_user_id);
    SELECT count(*)::integer INTO v_sessions FROM sessions AS s WHERE s.user_id = p_user_id AND s.expires_at > now();
    PERFORM auth_revoke_user_sessions(p_user_id);
    -- tenant tables are row-level secured: one tenant at a time, deleted organizations included
    FOR t IN SELECT m.tenant_id FROM memberships AS m WHERE m.user_id = p_user_id
             UNION SELECT i.tenant_id FROM invitations AS i WHERE i.accepted_by = p_user_id LOOP
        PERFORM set_config('app.tenant_id', t::text, true);
        DELETE FROM project_members AS pm WHERE pm.tenant_id = t AND pm.user_id = p_user_id;
        -- the label of an accepted invitation names a person
        UPDATE invitations AS i SET label = '' WHERE i.tenant_id = t AND i.accepted_by = p_user_id AND i.label <> '';
        DELETE FROM memberships AS m WHERE m.tenant_id = t AND m.user_id = p_user_id;
        GET DIAGNOSTICS n = ROW_COUNT;
        v_members := v_members + n;
    END LOOP;
    PERFORM set_config('app.tenant_id', COALESCE(caller, ''), true);
    DELETE FROM auth_identities AS ai WHERE ai.user_id = p_user_id;
    GET DIAGNOSTICS v_identities = ROW_COUNT;
    DELETE FROM email_verifications AS ev WHERE ev.user_id = p_user_id;
    DELETE FROM password_resets AS pr WHERE pr.user_id = p_user_id;
    UPDATE users
    SET email = 'deleted-' || p_user_id::text || '@deleted.invalid', display_name = '', password_hash = NULL,
        configured_password_hash = NULL, email_verified_at = NULL, password_changed_at = NULL, password_must_change = false,
        status = 'deleted', deleted_at = now(), updated_at = now()
    WHERE id = p_user_id;
    PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.user.deleted', 'user', p_user_id::text,
                        jsonb_build_object('sessionsRevoked', v_sessions, 'membershipsRemoved', v_members, 'identitiesRemoved', v_identities), p_ip);
    RETURN QUERY SELECT true, v_sessions, v_members, v_identities;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_delete_user(uuid,uuid,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_delete_user(uuid,uuid,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_get_tenant(p_actor uuid, p_tenant_id uuid) RETURNS TABLE(id uuid, slug text, name text, plan_key text, plan_name text, status text, created_at timestamp with time zone, deleted_at timestamp with time zone, user_count integer, monitor_count integer, heartbeat_count integer)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    SELECT t.id, t.slug, t.name, t.plan_key, pl.name, t.status, t.created_at, t.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = t.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type <> 'heartbeat' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL)
    FROM tenants AS t
    JOIN plans AS pl ON pl.key = t.plan_key
    WHERE t.id = p_tenant_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_get_tenant(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_get_tenant(uuid,uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_get_user(p_actor uuid, p_user_id uuid) RETURNS TABLE(id uuid, login text, display_name text, status text, is_platform_admin boolean, is_system_admin boolean, email_verified boolean, must_change_password boolean, created_at timestamp with time zone, organization_count integer, open_sessions integer)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_get_user(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_get_user(uuid,uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_list_all_users(p_actor uuid, p_query text, p_status text, p_limit integer, p_offset integer) RETURNS TABLE(id uuid, login text, display_name text, status text, is_platform_admin boolean, is_system_admin boolean, email_verified boolean, must_change_password boolean, created_at timestamp with time zone, organization_count integer, total_count integer)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    q  text := lower(btrim(COALESCE(p_query, '')));
    st text := COALESCE(p_status, '');
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF st NOT IN ('', 'active', 'disabled', 'deleted') THEN
        RAISE EXCEPTION 'admin: invalid status' USING ERRCODE = 'PRA04';
    END IF;
    RETURN QUERY
    SELECT u.id, u.email::text, u.display_name, u.status, u.is_platform_admin, u.is_system_admin, u.email_verified_at IS NOT NULL,
           u.password_must_change, u.created_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.user_id = u.id),
           (count(*) OVER ())::integer
    FROM users AS u
    WHERE (u.status = st OR (st = '' AND u.status <> 'deleted'))
      AND (q = '' OR strpos(lower(u.email::text), q) > 0 OR strpos(lower(u.display_name), q) > 0)
    ORDER BY u.created_at DESC, u.id
    LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
    OFFSET greatest(COALESCE(p_offset, 0), 0);
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_list_all_users(uuid,text,text,integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_all_users(uuid,text,text,integer,integer) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_list_tenants(p_actor uuid, p_query text, p_include_deleted boolean, p_limit integer, p_offset integer) RETURNS TABLE(id uuid, slug text, name text, plan_key text, plan_name text, status text, created_at timestamp with time zone, deleted_at timestamp with time zone, user_count integer, monitor_count integer, heartbeat_count integer, total_count integer)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    q text := lower(btrim(COALESCE(p_query, '')));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    WITH matched AS (
        SELECT t.id, t.slug, t.name, t.plan_key, t.status, t.created_at, t.deleted_at
        FROM tenants AS t
        WHERE (p_include_deleted OR t.deleted_at IS NULL)
          AND (q = ''
               OR strpos(lower(t.slug), q) > 0
               OR strpos(lower(t.name), q) > 0
               OR t.id::text = q
               OR EXISTS (SELECT 1 FROM memberships AS m JOIN users AS u ON u.id = m.user_id
                          WHERE m.tenant_id = t.id AND strpos(lower(u.email::text), q) > 0))
    ), page AS (
        SELECT mt.*, count(*) OVER () AS total
        FROM matched AS mt
        ORDER BY mt.created_at DESC, mt.id DESC
        LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
        OFFSET greatest(COALESCE(p_offset, 0), 0)
    )
    SELECT p.id, p.slug, p.name, p.plan_key, pl.name, p.status, p.created_at, p.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = p.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type <> 'heartbeat' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL),
           p.total::integer
    FROM page AS p
    JOIN plans AS pl ON pl.key = p.plan_key
    ORDER BY p.created_at DESC, p.id DESC;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_list_tenants(uuid,text,boolean,integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_tenants(uuid,text,boolean,integer,integer) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_list_user_memberships(p_actor uuid, p_user_id uuid) RETURNS TABLE(organization_id uuid, organization_name text, organization_slug text, organization_status text, role text, joined_at timestamp with time zone)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_list_user_memberships(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_user_memberships(uuid,uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_list_users(p_actor uuid, p_tenant_id uuid, p_query text, p_limit integer, p_offset integer) RETURNS TABLE(id uuid, login text, status text, created_at timestamp with time zone, is_platform_admin boolean, role text, total_count integer)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    q text := lower(btrim(COALESCE(p_query, '')));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF NOT EXISTS (SELECT 1 FROM tenants AS t WHERE t.id = p_tenant_id) THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    RETURN QUERY
    SELECT u.id, u.email::text, u.status, u.created_at, u.is_platform_admin, m.role,
           (count(*) OVER ())::integer
    FROM memberships AS m
    JOIN users AS u ON u.id = m.user_id
    WHERE m.tenant_id = p_tenant_id
      AND (q = '' OR strpos(lower(u.email::text), q) > 0)
    ORDER BY m.created_at, u.id
    LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
    OFFSET greatest(COALESCE(p_offset, 0), 0);
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_list_users(uuid,uuid,text,integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_users(uuid,uuid,text,integer,integer) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_plan_create(p_actor uuid, p_key text, p_name text, p_limits jsonb, p_audit_id uuid, p_ip inet) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $_$
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
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_plan_create(uuid,text,text,jsonb,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_create(uuid,text,text,jsonb,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_plan_delete(p_actor uuid, p_key text, p_audit_id uuid, p_ip inet) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_plan_delete(uuid,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_delete(uuid,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_plan_peak_usage(p_actor uuid, p_key text) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_plan_peak_usage(uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_peak_usage(uuid,text) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_plan_update(p_actor uuid, p_key text, p_name text, p_limits jsonb, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_plan_update(uuid,text,text,jsonb,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_plan_update(uuid,text,text,jsonb,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_plans_list(p_actor uuid) RETURNS TABLE(key text, name text, limits jsonb, is_system boolean, organizations integer, deletable boolean)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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

-- +goose StatementBegin
CREATE FUNCTION admin_require_platform_admin(p_actor uuid) RETURNS void
    LANGUAGE plpgsql STABLE
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    IF p_actor IS NULL OR NOT EXISTS (
        SELECT 1 FROM users AS u WHERE u.id = p_actor AND u.status = 'active' AND u.is_platform_admin
    ) THEN
        RAISE EXCEPTION 'admin: actor is not a platform admin' USING ERRCODE = 'PRA01';
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_require_platform_admin(uuid) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION admin_reset_user_password(p_actor uuid, p_user_id uuid, p_password_hash text, p_audit_id uuid, p_ip inet) RETURNS TABLE(tenant_id uuid, sessions_revoked integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $_$
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
    UPDATE password_resets SET used_at = now() WHERE user_id = p_user_id AND used_at IS NULL;
    PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.user.password_reset', 'user', p_user_id::text,
                        jsonb_build_object('sessionsRevoked', v_n), p_ip);
    RETURN QUERY SELECT v_tenant, v_n;
END
$_$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_reset_user_password(uuid,uuid,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_reset_user_password(uuid,uuid,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_revoke_user_sessions(p_actor uuid, p_user_id uuid, p_audit_id uuid, p_ip inet) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_revoke_user_sessions(uuid,uuid,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_revoke_user_sessions(uuid,uuid,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_set_platform_admin(p_actor uuid, p_user_id uuid, p_grant boolean, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_set_platform_admin(uuid,uuid,boolean,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_platform_admin(uuid,uuid,boolean,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_set_tenant_plan(p_actor uuid, p_tenant_id uuid, p_plan_key text, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    v_old     text;
    v_deleted timestamptz;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT t.plan_key, t.deleted_at INTO v_old, v_deleted FROM tenants AS t WHERE t.id = p_tenant_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RAISE EXCEPTION 'admin: tenant is deleted' USING ERRCODE = 'PRA08';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM plans AS p WHERE p.key = p_plan_key) THEN
        RAISE EXCEPTION 'admin: unknown plan' USING ERRCODE = 'PRA03';
    END IF;
    IF v_old = p_plan_key THEN
        RETURN false;
    END IF;
    UPDATE tenants SET plan_key = p_plan_key, updated_at = now() WHERE id = p_tenant_id;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id, 'admin.organization.plan_changed', 'organization', p_tenant_id::text,
                        jsonb_build_object('from', v_old, 'to', p_plan_key), p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_set_tenant_plan(uuid,uuid,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_tenant_plan(uuid,uuid,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_set_tenant_status(p_actor uuid, p_tenant_id uuid, p_status text, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_set_tenant_status(uuid,uuid,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_tenant_status(uuid,uuid,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_set_user_status(p_actor uuid, p_user_id uuid, p_status text, p_audit_id uuid, p_ip inet) RETURNS TABLE(changed boolean, sessions_revoked integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
    -- a deleted account is gone for the panel, it cannot be enabled again
    IF NOT FOUND OR v_status = 'deleted' THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_status = p_status THEN
        RETURN QUERY SELECT false, 0;
        RETURN;
    END IF;
    IF p_status = 'disabled' AND v_system THEN
        RAISE EXCEPTION 'admin: the system admin cannot be disabled' USING ERRCODE = 'PRA07';
    END IF;
    -- an organization must keep an active owner (the same rule as removing or demoting the last one)
    IF p_status = 'disabled' AND EXISTS (
        SELECT 1 FROM memberships AS m JOIN tenants AS t ON t.id = m.tenant_id
        WHERE m.user_id = p_user_id AND m.role = 'owner' AND t.deleted_at IS NULL
          AND NOT EXISTS (SELECT 1 FROM memberships AS o JOIN users AS ou ON ou.id = o.user_id
                          WHERE o.tenant_id = m.tenant_id AND o.role = 'owner' AND o.user_id <> p_user_id AND ou.status = 'active')) THEN
        RAISE EXCEPTION 'admin: the user is the only active owner of an organization' USING ERRCODE = 'PRA35';
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
REVOKE ALL ON FUNCTION admin_set_user_status(uuid,uuid,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_user_status(uuid,uuid,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_stats(p_actor uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN jsonb_build_object(
        'tenants', (SELECT jsonb_build_object(
                        'total', count(*) FILTER (WHERE t.deleted_at IS NULL),
                        'active', count(*) FILTER (WHERE t.deleted_at IS NULL AND t.status = 'active'),
                        'suspended', count(*) FILTER (WHERE t.deleted_at IS NULL AND t.status = 'suspended'),
                        'deleted', count(*) FILTER (WHERE t.deleted_at IS NOT NULL))
                    FROM tenants AS t),
        'users', (SELECT count(*) FROM users AS u WHERE u.status = 'active'),
        'monitors', (SELECT count(*) FROM monitors AS m JOIN tenants AS t ON t.id = m.tenant_id
                     WHERE m.type <> 'heartbeat' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
        'heartbeats', (SELECT count(*) FROM monitors AS m JOIN tenants AS t ON t.id = m.tenant_id
                       WHERE m.type = 'heartbeat' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
        'plans', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                         'key', p.key, 'name', p.name,
                         'tenants', (SELECT count(*) FROM tenants AS t WHERE t.plan_key = p.key AND t.deleted_at IS NULL))
                      ORDER BY p.key), '[]'::jsonb)
                  FROM plans AS p)
    );
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_stats(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_stats(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_status_page_domain_set_disabled(p_actor uuid, p_id uuid, p_disabled boolean, p_reason text, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_status_page_domain_set_disabled(uuid,uuid,boolean,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_status_page_domain_set_disabled(uuid,uuid,boolean,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_status_page_domains_list(p_actor uuid, p_id uuid, p_query text, p_states text[], p_limit integer, p_offset integer) RETURNS TABLE(id uuid, host text, state text, is_primary boolean, disabled_at timestamp with time zone, tenant_id uuid, tenant_slug text, page_id uuid, page_slug text, verified_at timestamp with time zone, last_checked_at timestamp with time zone, last_error text, created_at timestamp with time zone, total_count integer)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_status_page_domains_list(uuid,uuid,text,text[],integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_status_page_domains_list(uuid,uuid,text,text[],integer,integer) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_user_audit_tenant(p_user_id uuid) RETURNS uuid
    LANGUAGE sql STABLE
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT COALESCE(auth_login_tenant(p_user_id), (SELECT t.id FROM tenants AS t WHERE t.slug = 'default'))
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_user_audit_tenant(uuid) FROM PUBLIC;

-- =============================================================================================
-- Specula (executors in customer networks)
-- =============================================================================================

-- Agents, their project access, and the functions the specula API and the jobs process use.

CREATE TABLE specula_agents (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    name text NOT NULL,
    token_hash bytea NOT NULL,
    all_projects boolean DEFAULT true NOT NULL,
    location_id uuid NOT NULL,
    version text,
    hostname text,
    last_seen_at timestamptz,
    revoked_at timestamptz,
    created_by uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    capabilities text[] DEFAULT '{}'::text[] NOT NULL,
    instance_id text,
    schedule_issued_at timestamptz,
    buffer_depth integer,
    oldest_unsent_at timestamptz,
    buffer_degraded boolean DEFAULT false NOT NULL,
    clock_offset_ms bigint,
    last_hello_at timestamptz,
    duplicate_instance_at timestamptz,
    slot_counters jsonb,
    CONSTRAINT specula_agents_buffer_depth_check CHECK ((buffer_depth >= 0)),
    CONSTRAINT specula_agents_capabilities_check CHECK ((cardinality(capabilities) <= 16)),
    CONSTRAINT specula_agents_hostname_check CHECK ((length(hostname) <= 253)),
    CONSTRAINT specula_agents_instance_id_check CHECK ((length(instance_id) <= 64)),
    CONSTRAINT specula_agents_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 80))),
    CONSTRAINT specula_agents_token_hash_check CHECK ((length(token_hash) = 32)),
    CONSTRAINT specula_agents_version_check CHECK ((length(version) <= 64)),
    CONSTRAINT specula_agents_id_key UNIQUE (id),
    CONSTRAINT specula_agents_location_id_key UNIQUE (location_id),
    CONSTRAINT specula_agents_pkey PRIMARY KEY (tenant_id, id),
    CONSTRAINT specula_agents_token_hash_key UNIQUE (token_hash)
);

ALTER TABLE ONLY specula_agents FORCE ROW LEVEL SECURITY;
CREATE UNIQUE INDEX specula_agents_name_idx ON specula_agents USING btree (tenant_id, lower(name)) WHERE (revoked_at IS NULL);
ALTER TABLE specula_agents ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_specula ON specula_agents TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON specula_agents USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON specula_agents FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT ON specula_agents TO proreta_api;
GRANT UPDATE(name) ON TABLE specula_agents TO proreta_api;
GRANT UPDATE(all_projects) ON TABLE specula_agents TO proreta_api;
GRANT UPDATE(updated_at) ON TABLE specula_agents TO proreta_api;

CREATE TABLE specula_projects (
    tenant_id uuid NOT NULL,
    specula_id uuid NOT NULL,
    project_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT specula_projects_pkey PRIMARY KEY (specula_id, project_id)
);

ALTER TABLE ONLY specula_projects FORCE ROW LEVEL SECURITY;
ALTER TABLE specula_projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_specula_read ON specula_projects FOR SELECT TO proreta_owner USING (true);
CREATE POLICY tenant_isolation ON specula_projects USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON specula_projects FROM proreta_api, proreta_worker;
GRANT SELECT, INSERT, DELETE ON specula_projects TO proreta_api;

CREATE TABLE specula_slot_losses (
    tenant_id uuid NOT NULL,
    specula_id uuid NOT NULL,
    hour timestamptz NOT NULL,
    kind text NOT NULL,
    reason text NOT NULL,
    count bigint NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT specula_slot_losses_count_check CHECK ((count > 0)),
    CONSTRAINT specula_slot_losses_hour_check CHECK ((hour = date_trunc('hour'::text, hour, 'UTC'::text))),
    CONSTRAINT specula_slot_losses_kind_check CHECK ((kind = ANY (ARRAY['missed'::text, 'dropped'::text]))),
    CONSTRAINT specula_slot_losses_reason_check CHECK ((((kind = 'missed'::text) AND (reason = ANY (ARRAY['busy'::text, 'late'::text, 'scheduleExpired'::text]))) OR ((kind = 'dropped'::text) AND (reason = ANY (ARRAY['bufferSize'::text, 'bufferAge'::text, 'rejected'::text, 'superseded'::text, 'tooOld'::text, 'unreadable'::text]))))),
    CONSTRAINT specula_slot_losses_pkey PRIMARY KEY (tenant_id, specula_id, hour, kind, reason)
);

ALTER TABLE ONLY specula_slot_losses FORCE ROW LEVEL SECURITY;
CREATE INDEX specula_slot_losses_hour_idx ON specula_slot_losses USING btree (hour);
ALTER TABLE specula_slot_losses ENABLE ROW LEVEL SECURITY;
CREATE POLICY owner_slot_losses ON specula_slot_losses TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY tenant_isolation ON specula_slot_losses USING ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)) WITH CHECK ((tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid));
REVOKE ALL ON specula_slot_losses FROM proreta_api, proreta_worker;
GRANT SELECT ON specula_slot_losses TO proreta_api;

ALTER TABLE ONLY incidents
    ADD CONSTRAINT incidents_specula_fkey FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents(tenant_id, id);

ALTER TABLE ONLY notification_deliveries
    ADD CONSTRAINT notification_deliveries_specula_fkey FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents(tenant_id, id);

ALTER TABLE ONLY specula_slot_losses
    ADD CONSTRAINT specula_slot_losses_specula_fkey FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents(tenant_id, id);

ALTER TABLE ONLY specula_agents
    ADD CONSTRAINT specula_agents_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);

ALTER TABLE ONLY specula_agents
    ADD CONSTRAINT specula_agents_location_id_fkey FOREIGN KEY (location_id) REFERENCES probe_locations(id);

ALTER TABLE ONLY specula_agents
    ADD CONSTRAINT specula_agents_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE ONLY specula_projects
    ADD CONSTRAINT specula_projects_tenant_id_project_id_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id);

ALTER TABLE ONLY specula_projects
    ADD CONSTRAINT specula_projects_tenant_id_specula_id_fkey FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents(tenant_id, id);

-- +goose StatementBegin
CREATE FUNCTION claim_due_monitors(n integer, p_location text) RETURNS TABLE(tenant_id uuid, monitor_id uuid, location_id uuid, lease_token uuid, lease_until timestamp with time zone, url text, expected_status text, failure_threshold integer, interval_seconds integer, timeout_seconds integer, next_run_at timestamp with time zone, method text, request text, monitor_type text, host text, port integer)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH due AS (
        SELECT s.monitor_id, s.location_id
        FROM monitor_location_state AS s
        JOIN monitors AS m ON m.tenant_id = s.tenant_id AND m.id = s.monitor_id
        JOIN tenants AS t ON t.id = s.tenant_id
        JOIN probe_locations AS l ON l.id = s.location_id
        WHERE l.code = p_location
          AND l.status = 'active'
          AND t.status = 'active'
          AND t.deleted_at IS NULL
          AND s.next_run_at <= now()
          AND (s.lease_token IS NULL OR s.lease_until <= now())
          AND m.type IN ('http', 'tcp')
          AND NOT m.paused
          AND m.deleted_at IS NULL
          AND (l.kind = 'platform' OR EXISTS (
                SELECT 1 FROM specula_agents AS a
                WHERE a.location_id = l.id AND a.tenant_id = s.tenant_id AND a.revoked_at IS NULL
                  AND NOT ('schedule/1' = ANY (a.capabilities))
                  AND (m.type = 'http' OR 'tcp' = ANY (a.capabilities))
                  AND (a.all_projects OR EXISTS (
                        SELECT 1 FROM specula_projects AS sp
                        WHERE sp.specula_id = a.id AND sp.project_id = m.project_id))))
        ORDER BY s.next_run_at
        LIMIT least(greatest(n, 0), 1000)
        FOR UPDATE OF s SKIP LOCKED
    )
    UPDATE monitor_location_state AS s
    SET lease_token = gen_random_uuid(),
        lease_until = now() + interval '30 seconds'
    FROM due, monitors AS m
    WHERE s.monitor_id = due.monitor_id AND s.location_id = due.location_id
      AND m.tenant_id = s.tenant_id AND m.id = s.monitor_id
    RETURNING s.tenant_id, s.monitor_id, s.location_id, s.lease_token, s.lease_until,
              COALESCE(m.config ->> 'url', ''), COALESCE(m.config ->> 'expectedStatus', '2xx'),
              m.failure_threshold, m.interval_seconds, m.timeout_seconds, s.next_run_at,
              COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config ->> 'request', ''),
              m.type, COALESCE(m.config ->> 'host', ''), COALESCE((m.config ->> 'port')::integer, 0)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_due_monitors(integer,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_due_monitors(integer,text) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_authenticate(p_token_hash bytea) RETURNS TABLE(specula_id uuid, tenant_id uuid, location_id uuid, location_code text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    RETURN QUERY
    WITH hit AS (
        SELECT a.id, a.tenant_id, a.location_id, l.code
        FROM specula_agents AS a
        JOIN tenants AS t ON t.id = a.tenant_id
        JOIN probe_locations AS l ON l.id = a.location_id
        WHERE a.token_hash = p_token_hash
          AND a.revoked_at IS NULL
          AND t.status = 'active' AND t.deleted_at IS NULL
          AND l.status = 'active'
    ), seen AS (
        UPDATE specula_agents AS a SET last_seen_at = now()
        FROM hit
        WHERE a.tenant_id = hit.tenant_id AND a.id = hit.id
          AND (a.last_seen_at IS NULL OR a.last_seen_at < now() - interval '10 seconds')
    )
    SELECT hit.id, hit.tenant_id, hit.location_id, hit.code FROM hit;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_authenticate(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_authenticate(bytea) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_create(p_id uuid, p_name text, p_token_hash bytea, p_all_projects boolean, p_project_ids uuid[], p_actor uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    loc           uuid := p_id;
    project       uuid;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'specula_create: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- probe_locations is shared by all organizations: the customer's name stays in specula_agents (RLS)
    INSERT INTO probe_locations (id, code, name, provider, region, kind)
    VALUES (loc, 'specula-' || replace(p_id::text, '-', ''), 'specula-' || replace(p_id::text, '-', ''), 'customer', 'private-network', 'agent');
    INSERT INTO specula_agents (tenant_id, id, name, token_hash, all_projects, location_id, created_by)
    VALUES (caller_tenant, p_id, p_name, p_token_hash, p_all_projects, loc, p_actor);
    IF NOT p_all_projects THEN
        FOREACH project IN ARRAY COALESCE(p_project_ids, '{}') LOOP
            INSERT INTO specula_projects (tenant_id, specula_id, project_id) VALUES (caller_tenant, p_id, project)
            ON CONFLICT DO NOTHING;
        END LOOP;
    END IF;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_create(uuid,text,bytea,boolean,uuid[],uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_create(uuid,text,bytea,boolean,uuid[],uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION specula_delivery_context(p_specula_id uuid, p_channel_id uuid) RETURNS TABLE(routed boolean, organization_name text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT (c.project_id IS NULL OR s.all_projects
            OR EXISTS (SELECT 1 FROM specula_projects AS sp
                       WHERE sp.specula_id = s.id AND sp.project_id = c.project_id)),
           t.name
    FROM specula_agents AS s
    JOIN tenants AS t ON t.id = s.tenant_id
    JOIN notification_channels AS c ON c.tenant_id = s.tenant_id AND c.id = p_channel_id
    WHERE s.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid AND s.id = p_specula_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_delivery_context(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_delivery_context(uuid,uuid) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_epoch_close(p_tenant uuid, p_monitor uuid, p_location uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    n integer;
BEGIN
    UPDATE monitor_schedule_epochs SET active_until = now()
    WHERE tenant_id = p_tenant AND monitor_id = p_monitor AND location_id = p_location AND active_until IS NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_epoch_close(uuid,uuid,uuid) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION specula_epoch_open(p_tenant uuid, p_monitor uuid, p_location uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    ival   integer;
    cur    integer;
    nxt    integer;
    start  bigint := ceil(extract(epoch FROM now()))::bigint;
    phase  bigint;
BEGIN
    SELECT m.interval_seconds INTO ival
    FROM monitors AS m
    JOIN probe_locations AS l ON l.id = p_location AND l.kind = 'agent'
    WHERE m.tenant_id = p_tenant AND m.id = p_monitor
      AND m.type IN ('http', 'tcp') AND NOT m.paused AND m.deleted_at IS NULL;
    IF ival IS NULL THEN
        RETURN NULL;
    END IF;
    SELECT e.epoch INTO cur FROM monitor_schedule_epochs AS e
    WHERE e.monitor_id = p_monitor AND e.location_id = p_location AND e.active_until IS NULL;
    IF cur IS NOT NULL THEN
        RETURN cur;
    END IF;
    SELECT COALESCE(max(e.epoch), 0) + 1 INTO nxt FROM monitor_schedule_epochs AS e
    WHERE e.monitor_id = p_monitor AND e.location_id = p_location;
    phase := ((('x' || encode(substring(sha256(convert_to(p_monitor::text || p_location::text, 'UTF8')) FROM 1 FOR 8), 'hex'))::bit(64)::bigint)
              % ival + ival) % ival;
    INSERT INTO monitor_schedule_epochs (tenant_id, monitor_id, location_id, epoch, anchor, interval_seconds, active_from)
    VALUES (p_tenant, p_monitor, p_location, nxt, start + phase, ival, now());
    RETURN nxt;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_epoch_open(uuid,uuid,uuid) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION specula_hello(p_specula_id uuid, p_version text, p_hostname text, p_capabilities text[], p_instance_id text, p_schedule_issued_at timestamp with time zone, p_buffer_depth integer, p_oldest_unsent_at timestamp with time zone, p_buffer_degraded boolean, p_clock_offset_ms bigint) RETURNS TABLE(found boolean, duplicate boolean)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH old AS (
        SELECT id,
               (instance_id IS NOT NULL AND left(p_instance_id, 64) IS NOT NULL
                AND instance_id <> left(p_instance_id, 64)
                AND last_hello_at > now() - interval '3 minutes') AS dup
        FROM specula_agents
        WHERE id = p_specula_id AND revoked_at IS NULL
        FOR UPDATE
    ), u AS (
        UPDATE specula_agents AS a SET
            version = left(p_version, 64),
            hostname = left(p_hostname, 253),
            capabilities = coalesce(p_capabilities, '{}'),
            instance_id = left(p_instance_id, 64),
            slot_counters = CASE WHEN a.instance_id IS NOT DISTINCT FROM left(p_instance_id, 64) THEN a.slot_counters END,
            schedule_issued_at = p_schedule_issued_at,
            buffer_depth = p_buffer_depth,
            oldest_unsent_at = p_oldest_unsent_at,
            buffer_degraded = coalesce(p_buffer_degraded, false),
            clock_offset_ms = p_clock_offset_ms,
            last_hello_at = now(),
            duplicate_instance_at = CASE WHEN old.dup THEN now() ELSE a.duplicate_instance_at END,
            last_seen_at = now()
        FROM old
        WHERE a.id = old.id
        RETURNING old.dup
    )
    SELECT EXISTS (SELECT 1 FROM u), coalesce((SELECT bool_or(dup) FROM u), false)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_hello(uuid,text,text,text[],text,timestamp with time zone,integer,timestamp with time zone,boolean,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_hello(uuid,text,text,text[],text,timestamp with time zone,integer,timestamp with time zone,boolean,bigint) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_record_slot_losses(p_specula_id uuid, p_counters jsonb) RETURNS TABLE(kind text, reason text, delta bigint)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    a specula_agents%ROWTYPE;
BEGIN
    -- Called right after specula_hello in the same transaction, which has the row locked and has
    -- already cleared slot_counters when the instance id changed: what is stored belongs to this process.
    SELECT * INTO a FROM specula_agents AS s WHERE s.id = p_specula_id AND s.revoked_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    RETURN QUERY
    WITH d AS (
        SELECT m.kind, m.reason,
               CASE WHEN o.v IS NOT NULL AND c.v >= o.v THEN c.v - o.v ELSE c.v END AS delta
        FROM (VALUES ('missed', 'busy'), ('missed', 'late'), ('missed', 'scheduleExpired'),
                     ('dropped', 'bufferSize'), ('dropped', 'bufferAge'), ('dropped', 'rejected'),
                     ('dropped', 'superseded'), ('dropped', 'tooOld'), ('dropped', 'unreadable')) AS m(kind, reason)
        CROSS JOIN LATERAL (SELECT coalesce((p_counters ->> m.reason)::bigint, 0) AS v) AS c
        CROSS JOIN LATERAL (SELECT (a.slot_counters ->> m.reason)::bigint AS v) AS o
    ), ins AS (
        INSERT INTO specula_slot_losses AS l (tenant_id, specula_id, hour, kind, reason, count)
        SELECT a.tenant_id, a.id, date_trunc('hour', now(), 'UTC'), d.kind, d.reason, d.delta FROM d WHERE d.delta > 0
        ON CONFLICT (tenant_id, specula_id, hour, kind, reason)
        DO UPDATE SET count = l.count + excluded.count, updated_at = now()
        RETURNING 1
    ), upd AS (
        UPDATE specula_agents AS s SET slot_counters = p_counters WHERE s.id = a.id RETURNING 1
    )
    SELECT d.kind, d.reason, d.delta FROM d WHERE d.delta > 0;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_record_slot_losses(uuid,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_record_slot_losses(uuid,jsonb) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_mark_unreachable(p_after interval DEFAULT '00:03:00'::interval) RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH gone AS (
        SELECT a.location_id
        FROM specula_agents AS a
        WHERE a.revoked_at IS NOT NULL OR a.last_seen_at < now() - p_after
    ), flagged AS (
        UPDATE monitor_location_state AS s
        SET status = 'unknown', consecutive_failures = 0
        FROM gone
        WHERE s.location_id = gone.location_id AND s.status <> 'unknown'
        RETURNING s.tenant_id, s.monitor_id
    ), changed AS (
        UPDATE monitor_state AS m
        SET status = 'unknown', status_changed_at = now()
        FROM flagged
        WHERE m.tenant_id = flagged.tenant_id AND m.monitor_id = flagged.monitor_id AND m.status <> 'unknown'
        RETURNING 1
    )
    SELECT count(*)::integer FROM changed
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_mark_unreachable(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_mark_unreachable(interval) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_revoke(p_id uuid) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    loc           uuid;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'specula_revoke: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    UPDATE specula_agents SET revoked_at = now(), updated_at = now()
    WHERE tenant_id = caller_tenant AND id = p_id AND revoked_at IS NULL
    RETURNING location_id INTO loc;
    IF loc IS NULL THEN
        RETURN false;
    END IF;
    UPDATE probe_locations SET status = 'disabled' WHERE id = loc;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_revoke(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_revoke(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION specula_schedule(p_tenant uuid, p_specula uuid) RETURNS TABLE(monitor_id uuid, epoch integer, anchor bigint, interval_seconds integer, timeout_seconds integer, monitor_type text, url text, expected_status text, method text, request text, host text, port integer)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT e.monitor_id, e.epoch, e.anchor, e.interval_seconds, m.timeout_seconds, m.type,
           COALESCE(m.config ->> 'url', ''), COALESCE(m.config ->> 'expectedStatus', '2xx'),
           COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config ->> 'request', ''),
           COALESCE(m.config ->> 'host', ''), COALESCE((m.config ->> 'port')::integer, 0)
    FROM specula_agents AS a
    JOIN tenants AS t ON t.id = a.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
    JOIN probe_locations AS l ON l.id = a.location_id AND l.status = 'active'
    JOIN monitor_schedule_epochs AS e ON e.tenant_id = a.tenant_id AND e.location_id = a.location_id AND e.active_until IS NULL
    JOIN monitors AS m ON m.tenant_id = e.tenant_id AND m.id = e.monitor_id
    WHERE a.tenant_id = p_tenant AND a.id = p_specula AND a.revoked_at IS NULL
      AND m.type IN ('http', 'tcp') AND NOT m.paused AND m.deleted_at IS NULL
      AND (a.all_projects OR EXISTS (
            SELECT 1 FROM specula_projects AS sp WHERE sp.specula_id = a.id AND sp.project_id = m.project_id))
    ORDER BY e.monitor_id
    LIMIT 5001
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_schedule(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_schedule(uuid,uuid) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_status_counts(p_after interval DEFAULT '00:03:00'::interval) RETURNS TABLE(status text, n bigint)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT CASE WHEN revoked_at IS NOT NULL THEN 'revoked'
                WHEN last_seen_at IS NULL THEN 'pending'
                WHEN last_seen_at < now() - p_after THEN 'offline'
                ELSE 'online' END AS status,
           count(*) AS n
    FROM specula_agents
    GROUP BY 1
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_status_counts(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_status_counts(interval) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION specula_sync_offline(p_after interval DEFAULT '00:03:00'::interval) RETURNS TABLE(opened integer, resolved integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    a record;
    v_id uuid;
    v_started timestamptz;
    v_count integer;
    v_now timestamptz := now();
BEGIN
    opened := 0;
    resolved := 0;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.specula_sync_offline'));

    FOR a IN
        SELECT s.tenant_id, s.id, s.name, s.all_projects, s.location_id
        FROM specula_agents AS s
        JOIN tenants AS t ON t.id = s.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
        WHERE s.revoked_at IS NULL AND s.last_seen_at < v_now - p_after
          AND NOT EXISTS (SELECT 1 FROM incidents AS i WHERE i.specula_id = s.id AND i.resolved_at IS NULL)
        ORDER BY s.id
    LOOP
        v_id := uuidv7_now();
        INSERT INTO incidents (id, tenant_id, specula_id, started_at, reason)
        VALUES (v_id, a.tenant_id, a.id, v_now, 'agent_offline');
        INSERT INTO incident_monitors (tenant_id, incident_id, monitor_id)
        SELECT a.tenant_id, v_id, m.id
        FROM monitor_locations AS ml
        JOIN monitors AS m ON m.tenant_id = ml.tenant_id AND m.id = ml.monitor_id AND m.deleted_at IS NULL
        WHERE ml.tenant_id = a.tenant_id AND ml.location_id = a.location_id;
        GET DIAGNOSTICS v_count = ROW_COUNT;
        INSERT INTO notification_deliveries (id, tenant_id, kind, specula_id, incident_id, channel_id, payload)
        SELECT uuidv7_now(), a.tenant_id, 'agent_offline', a.id, v_id, c.id,
               jsonb_build_object('speculaId', a.id, 'speculaName', a.name, 'incidentId', v_id,
                                  'reason', 'agent_offline', 'startedAt', v_now, 'monitorCount', v_count)
        FROM notification_channels AS c
        WHERE c.tenant_id = a.tenant_id AND c.deleted_at IS NULL AND c.status = 'active'
          AND 'agent_offline' = ANY (c.events)
          AND (c.project_id IS NULL OR a.all_projects
               OR EXISTS (SELECT 1 FROM specula_projects AS sp
                          WHERE sp.specula_id = a.id AND sp.project_id = c.project_id));
        opened := opened + 1;
    END LOOP;

    FOR a IN
        SELECT s.tenant_id, s.id, s.name, s.all_projects, i.id AS incident_id, i.started_at,
               s.revoked_at IS NOT NULL AS revoked
        FROM incidents AS i
        JOIN specula_agents AS s ON s.tenant_id = i.tenant_id AND s.id = i.specula_id
        WHERE i.resolved_at IS NULL
          AND (s.revoked_at IS NOT NULL OR s.last_seen_at >= v_now - p_after)
        ORDER BY i.id
    LOOP
        v_started := a.started_at;
        UPDATE incidents SET resolved_at = greatest(v_now, v_started),
                             resolve_reason = CASE WHEN a.revoked THEN 'revoked' ELSE 'recovered' END
        WHERE tenant_id = a.tenant_id AND id = a.incident_id;
        IF NOT a.revoked THEN
            INSERT INTO notification_deliveries (id, tenant_id, kind, specula_id, incident_id, channel_id, payload)
            SELECT uuidv7_now(), a.tenant_id, 'agent_online', a.id, a.incident_id, c.id,
                   jsonb_build_object('speculaId', a.id, 'speculaName', a.name, 'incidentId', a.incident_id,
                                      'reason', 'agent_offline', 'startedAt', v_started, 'resolvedAt', v_now,
                                      'durationSeconds', greatest(floor(extract(epoch FROM v_now - v_started)), 0)::bigint)
            FROM notification_channels AS c
            WHERE c.tenant_id = a.tenant_id AND c.deleted_at IS NULL AND c.status = 'active'
              AND 'agent_online' = ANY (c.events)
              AND (c.project_id IS NULL OR a.all_projects
                   OR EXISTS (SELECT 1 FROM specula_projects AS sp
                              WHERE sp.specula_id = a.id AND sp.project_id = c.project_id));
        END IF;
        resolved := resolved + 1;
    END LOOP;
    RETURN NEXT;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_sync_offline(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_sync_offline(interval) TO proreta_worker;

-- =============================================================================================
-- Email and platform settings
-- =============================================================================================

-- The single-row SMTP settings, the outgoing mail outbox, the platform-wide settings and the functions around them.

CREATE TABLE email_settings (
    id boolean DEFAULT true NOT NULL,
    host text DEFAULT ''::text NOT NULL,
    port integer DEFAULT 587 NOT NULL,
    tls text DEFAULT 'starttls'::text NOT NULL,
    from_header text DEFAULT ''::text NOT NULL,
    username text DEFAULT ''::text NOT NULL,
    password_sealed bytea DEFAULT '\x'::bytea NOT NULL,
    password_key_id text DEFAULT ''::text NOT NULL,
    connection_version uuid DEFAULT gen_random_uuid() NOT NULL,
    tested_version uuid,
    tested_at timestamptz,
    send_invitations boolean DEFAULT false NOT NULL,
    send_email_verification boolean DEFAULT false NOT NULL,
    allow_password_reset boolean DEFAULT false NOT NULL,
    test_requested_at timestamptz,
    test_finished_at timestamptz,
    test_ok boolean,
    test_error text,
    test_error_code integer,
    updated_at timestamptz DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT email_settings_check CHECK (((username = ''::text) = (octet_length(password_sealed) = 0))),
    CONSTRAINT email_settings_check1 CHECK (((username = ''::text) OR (tls <> 'none'::text))),
    CONSTRAINT email_settings_check2 CHECK (((host = ''::text) = (from_header = ''::text))),
    CONSTRAINT email_settings_check3 CHECK (((tested_version IS NULL) = (tested_at IS NULL))),
    CONSTRAINT email_settings_check4 CHECK (((host <> ''::text) OR ((tested_version IS NULL) AND (NOT (send_invitations OR send_email_verification OR allow_password_reset))))),
    CONSTRAINT email_settings_check5 CHECK (((NOT (send_invitations OR send_email_verification OR allow_password_reset)) OR (tested_version = connection_version))),
    CONSTRAINT email_settings_from_header_check CHECK ((length(from_header) <= 320)),
    CONSTRAINT email_settings_host_check CHECK ((length(host) <= 253)),
    CONSTRAINT email_settings_id_check CHECK (id),
    CONSTRAINT email_settings_password_key_id_check CHECK ((length(password_key_id) <= 64)),
    CONSTRAINT email_settings_password_sealed_check CHECK ((octet_length(password_sealed) <= 1024)),
    CONSTRAINT email_settings_port_check CHECK (((port >= 1) AND (port <= 65535))),
    CONSTRAINT email_settings_test_error_check CHECK ((length(test_error) <= 40)),
    CONSTRAINT email_settings_tls_check CHECK ((tls = ANY (ARRAY['starttls'::text, 'tls'::text, 'none'::text]))),
    CONSTRAINT email_settings_username_check CHECK ((length(username) <= 256)),
    CONSTRAINT email_settings_pkey PRIMARY KEY (id)
);
REVOKE ALL ON email_settings FROM proreta_api, proreta_worker;

CREATE TABLE mail_outbox (
    id uuid NOT NULL,
    kind text NOT NULL,
    payload bytea NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamptz DEFAULT now() NOT NULL,
    lease_until timestamptz,
    claim_token uuid,
    last_error text,
    created_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    finished_at timestamptz,
    CONSTRAINT mail_outbox_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT mail_outbox_check CHECK ((expires_at > created_at)),
    CONSTRAINT mail_outbox_check1 CHECK (((status = 'in_progress'::text) = (lease_until IS NOT NULL))),
    CONSTRAINT mail_outbox_check2 CHECK (((status = 'in_progress'::text) = (claim_token IS NOT NULL))),
    CONSTRAINT mail_outbox_check3 CHECK (((status = ANY (ARRAY['sent'::text, 'failed'::text])) = (finished_at IS NOT NULL))),
    CONSTRAINT mail_outbox_check4 CHECK (((status = ANY (ARRAY['pending'::text, 'in_progress'::text])) = (octet_length(payload) > 0))),
    CONSTRAINT mail_outbox_kind_check CHECK ((kind ~ '^[a-z][a-z_]{0,39}$'::text)),
    CONSTRAINT mail_outbox_last_error_check CHECK ((length(last_error) <= 500)),
    CONSTRAINT mail_outbox_payload_check CHECK ((octet_length(payload) <= 8192)),
    CONSTRAINT mail_outbox_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'in_progress'::text, 'sent'::text, 'failed'::text]))),
    CONSTRAINT mail_outbox_pkey PRIMARY KEY (id)
);
CREATE INDEX mail_outbox_finished_idx ON mail_outbox USING btree (finished_at) WHERE (finished_at IS NOT NULL);
CREATE INDEX mail_outbox_lease_idx ON mail_outbox USING btree (lease_until) WHERE (status = 'in_progress'::text);
CREATE INDEX mail_outbox_ready_idx ON mail_outbox USING btree (next_attempt_at) WHERE (status = 'pending'::text);
REVOKE ALL ON mail_outbox FROM proreta_api, proreta_worker;
GRANT INSERT ON mail_outbox TO proreta_api;

CREATE TABLE platform_settings (
    id boolean DEFAULT true NOT NULL,
    registration_enabled boolean,
    updated_at timestamptz,
    updated_by uuid,
    delivery_retention_days integer,
    rollup_retention_months integer,
    retention_updated_at timestamptz,
    CONSTRAINT platform_settings_id_check CHECK (id),
    CONSTRAINT platform_settings_pkey PRIMARY KEY (id)
);
REVOKE ALL ON platform_settings FROM proreta_api, proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_clear(p_actor uuid, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    s        email_settings%ROWTYPE;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF s.host = '' THEN
        RETURN false;
    END IF;
    UPDATE email_settings AS e
    SET host = '', port = 587, tls = 'starttls', from_header = '', username = '',
        password_sealed = ''::bytea, password_key_id = '',
        connection_version = gen_random_uuid(), tested_version = NULL, tested_at = NULL,
        send_invitations = false, send_email_verification = false, allow_password_reset = false,
        test_requested_at = NULL, test_finished_at = NULL, test_ok = NULL, test_error = NULL, test_error_code = NULL,
        updated_at = now(), updated_by = p_actor
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_settings.updated', 'email_settings', 'email',
                            jsonb_build_object('changed', jsonb_build_array('removed'),
                                               'switchesDisabled', s.send_invitations OR s.send_email_verification OR s.allow_password_reset), p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_clear(uuid,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_clear(uuid,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_get(p_actor uuid) RETURNS TABLE(host text, port integer, tls text, from_header text, username text, password_set boolean, send_invitations boolean, send_email_verification boolean, allow_password_reset boolean, tested boolean, tested_at timestamp with time zone, test_state text, test_requested_at timestamp with time zone, test_finished_at timestamp with time zone, test_error text, test_error_code integer, updated_at timestamp with time zone)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    SELECT s.host, s.port, s.tls, s.from_header, s.username, octet_length(s.password_sealed) > 0,
           s.send_invitations, s.send_email_verification, s.allow_password_reset,
           s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version, s.tested_at,
           CASE WHEN s.host = '' THEN 'none' ELSE email_settings_test_state(s) END,
           s.test_requested_at, s.test_finished_at,
           CASE WHEN email_settings_test_state(s) = 'failed' AND s.test_finished_at IS NULL THEN 'not_delivered' ELSE s.test_error END,
           s.test_error_code, s.updated_at
    FROM email_settings AS s;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_get(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_get(uuid) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_set_features(p_actor uuid, p_invitations boolean, p_verification boolean, p_reset boolean, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    s        email_settings%ROWTYPE;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_invitations IS NULL OR p_verification IS NULL OR p_reset IS NULL THEN
        RAISE EXCEPTION 'admin: invalid switches' USING ERRCODE = 'PRA04';
    END IF;
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF (p_invitations OR p_verification OR p_reset) AND NOT (s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version) THEN
        RAISE EXCEPTION 'admin: the settings have not passed a test' USING ERRCODE = 'PRA10';
    END IF;
    IF s.send_invitations = p_invitations AND s.send_email_verification = p_verification AND s.allow_password_reset = p_reset THEN
        RETURN false;
    END IF;
    UPDATE email_settings AS e
    SET send_invitations = p_invitations, send_email_verification = p_verification, allow_password_reset = p_reset,
        updated_at = now(), updated_by = p_actor
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_settings.updated', 'email_settings', 'email',
                            jsonb_build_object('changed', jsonb_build_array('switches'), 'sendInvitations', p_invitations,
                                               'sendEmailVerification', p_verification, 'allowPasswordReset', p_reset), p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_set_features(uuid,boolean,boolean,boolean,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_set_features(uuid,boolean,boolean,boolean,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_update(p_actor uuid, p_host text, p_port integer, p_tls text, p_from text, p_username text, p_password_sealed bytea, p_key_id text, p_audit_id uuid, p_ip inet) RETURNS TABLE(changed text[], switches_disabled boolean)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
#variable_conflict use_column
DECLARE
    s        email_settings%ROWTYPE;
    v_keep   boolean := p_password_sealed IS NULL;
    v_pw     bytea;
    v_key    text;
    v_changed text[] := ARRAY[]::text[];
    v_off    boolean;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF COALESCE(p_host, '') = '' OR COALESCE(p_from, '') = '' OR p_port IS NULL OR p_tls IS NULL
       OR p_tls NOT IN ('starttls', 'tls', 'none') OR p_port NOT BETWEEN 1 AND 65535 OR p_username IS NULL
       OR (p_tls = 'none' AND p_username <> '') THEN
        RAISE EXCEPTION 'admin: invalid email settings' USING ERRCODE = 'PRA04';
    END IF;
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF p_username = '' THEN
        v_pw := ''::bytea;
        v_key := '';
    ELSIF v_keep THEN
        IF octet_length(s.password_sealed) = 0 THEN
            RAISE EXCEPTION 'admin: a password is required with a username' USING ERRCODE = 'PRA13';
        END IF;
        IF p_host IS DISTINCT FROM s.host OR p_port IS DISTINCT FROM s.port OR p_username IS DISTINCT FROM s.username THEN
            RAISE EXCEPTION 'admin: enter the password again' USING ERRCODE = 'PRA09';
        END IF;
        v_pw := s.password_sealed;
        v_key := s.password_key_id;
    ELSE
        IF octet_length(p_password_sealed) = 0 THEN
            RAISE EXCEPTION 'admin: empty password' USING ERRCODE = 'PRA04';
        END IF;
        v_pw := p_password_sealed;
        v_key := COALESCE(p_key_id, '');
    END IF;

    IF p_host IS DISTINCT FROM s.host THEN v_changed := array_append(v_changed, 'host'::text); END IF;
    IF p_port IS DISTINCT FROM s.port THEN v_changed := array_append(v_changed, 'port'::text); END IF;
    IF p_tls IS DISTINCT FROM s.tls THEN v_changed := array_append(v_changed, 'tls'::text); END IF;
    IF p_from IS DISTINCT FROM s.from_header THEN v_changed := array_append(v_changed, 'from'::text); END IF;
    IF p_username IS DISTINCT FROM s.username THEN v_changed := array_append(v_changed, 'username'::text); END IF;
    IF (p_username <> '' AND NOT v_keep) OR (p_username = '' AND octet_length(s.password_sealed) > 0) THEN v_changed := array_append(v_changed, 'password'::text); END IF;
    IF cardinality(v_changed) = 0 THEN
        RETURN QUERY SELECT v_changed, false;
        RETURN;
    END IF;

    v_off := s.send_invitations OR s.send_email_verification OR s.allow_password_reset;
    UPDATE email_settings AS e
    SET host = p_host, port = p_port, tls = p_tls, from_header = p_from, username = p_username,
        password_sealed = v_pw, password_key_id = v_key,
        connection_version = gen_random_uuid(), tested_version = NULL, tested_at = NULL,
        send_invitations = false, send_email_verification = false, allow_password_reset = false,
        test_requested_at = NULL, test_finished_at = NULL, test_ok = NULL, test_error = NULL, test_error_code = NULL,
        updated_at = now(), updated_by = p_actor
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_settings.updated', 'email_settings', 'email',
                            jsonb_build_object('changed', to_jsonb(v_changed), 'switchesDisabled', v_off), p_ip);
    END IF;
    RETURN QUERY SELECT v_changed, v_off;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_update(uuid,text,integer,text,text,text,bytea,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_update(uuid,text,integer,text,text,text,bytea,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_email_test_request(p_actor uuid, p_to_domain text, p_audit_id uuid, p_ip inet) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
DECLARE
    s        email_settings%ROWTYPE;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF s.host = '' THEN
        RAISE EXCEPTION 'admin: no email server is configured' USING ERRCODE = 'PRA11';
    END IF;
    IF email_settings_test_state(s) = 'pending' THEN
        RAISE EXCEPTION 'admin: a test message is already on its way' USING ERRCODE = 'PRA12';
    END IF;
    UPDATE email_settings AS e
    SET test_requested_at = now(), test_finished_at = NULL, test_ok = NULL, test_error = NULL, test_error_code = NULL
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_test.sent', 'email_settings', 'email',
                            jsonb_build_object('toDomain', left(COALESCE(p_to_domain, ''), 253)), p_ip);
    END IF;
    RETURN s.connection_version;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_test_request(uuid,text,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_test_request(uuid,text,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_registration_get(p_actor uuid) RETURNS TABLE(registration_enabled boolean, updated_at timestamp with time zone)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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

-- +goose StatementBegin
CREATE FUNCTION admin_registration_set(p_actor uuid, p_enabled boolean, p_audit_id uuid, p_ip inet) RETURNS TABLE(from_value text, to_value text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_registration_set(uuid,boolean,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_registration_set(uuid,boolean,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION admin_retention_get(p_actor uuid) RETURNS TABLE(delivery_days integer, rollup_months integer, updated_at timestamp with time zone)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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

-- +goose StatementBegin
CREATE FUNCTION admin_retention_set(p_actor uuid, p_delivery_days integer, p_rollup_months integer, p_audit_id uuid, p_ip inet) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
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
REVOKE ALL ON FUNCTION admin_retention_set(uuid,integer,integer,uuid,inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_retention_set(uuid,integer,integer,uuid,inet) TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION claim_mail(n integer) RETURNS TABLE(id uuid, kind text, payload bytea, attempts integer, claim_token uuid)
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    UPDATE mail_outbox AS m
    SET status = CASE WHEN m.attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END,
        attempts = m.attempts + 1,
        last_error = 'lease_expired',
        lease_until = NULL,
        claim_token = NULL,
        finished_at = CASE WHEN m.attempts + 1 >= 5 THEN now() END,
        payload = CASE WHEN m.attempts + 1 >= 5 THEN ''::bytea ELSE m.payload END
    WHERE m.id IN (
        SELECT e.id FROM mail_outbox AS e
        WHERE e.status = 'in_progress' AND e.lease_until <= now()
        FOR UPDATE SKIP LOCKED
    );

    UPDATE mail_outbox AS m
    SET status = 'failed', last_error = 'expired', finished_at = now(), payload = ''::bytea
    WHERE m.id IN (
        SELECT e.id FROM mail_outbox AS e
        WHERE e.status = 'pending' AND e.expires_at <= now()
        FOR UPDATE SKIP LOCKED
    );

    WITH ready AS (
        SELECT e.id
        FROM mail_outbox AS e
        WHERE e.status = 'pending' AND e.next_attempt_at <= now()
        ORDER BY e.created_at, e.id
        LIMIT least(greatest(n, 0), 100)
        FOR UPDATE SKIP LOCKED
    )
    UPDATE mail_outbox AS m
    SET status = 'in_progress', lease_until = now() + interval '2 minutes', claim_token = gen_random_uuid()
    FROM ready
    WHERE m.id = ready.id
    RETURNING m.id, m.kind, m.payload, m.attempts, m.claim_token
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_mail(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_mail(integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION email_settings_features() RETURNS TABLE(enabled boolean, send_invitations boolean, send_email_verification boolean, allow_password_reset boolean)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version,
           s.send_invitations, s.send_email_verification, s.allow_password_reset
    FROM email_settings AS s
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_features() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION email_settings_features() TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION email_settings_for_jobs() RETURNS TABLE(host text, port integer, tls text, from_header text, username text, password_sealed bytea, connection_version uuid)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT s.host, s.port, s.tls, s.from_header, s.username, s.password_sealed, s.connection_version
    FROM email_settings AS s
    WHERE s.host <> ''
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_for_jobs() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION email_settings_for_jobs() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION email_settings_record_test(p_version uuid, p_ok boolean, p_error text, p_code integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    UPDATE email_settings AS s
    SET test_finished_at = now(),
        test_ok = p_ok,
        test_error = CASE WHEN p_ok THEN NULL ELSE left(COALESCE(p_error, 'other'), 40) END,
        test_error_code = CASE WHEN p_ok OR COALESCE(p_code, 0) = 0 THEN NULL ELSE p_code END,
        tested_version = CASE WHEN p_ok THEN p_version ELSE s.tested_version END,
        tested_at = CASE WHEN p_ok THEN now() ELSE s.tested_at END
    WHERE s.connection_version = p_version AND s.host <> '' AND s.test_requested_at IS NOT NULL;
    RETURN FOUND;
END
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_record_test(uuid,boolean,text,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION email_settings_record_test(uuid,boolean,text,integer) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION email_settings_test_state(s email_settings) RETURNS text
    LANGUAGE sql STABLE
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT CASE
        WHEN s.test_requested_at IS NULL THEN 'none'
        WHEN s.test_finished_at IS NOT NULL AND s.test_ok THEN 'succeeded'
        WHEN s.test_finished_at IS NOT NULL THEN 'failed'
        WHEN s.test_requested_at > now() - interval '3 minutes' THEN 'pending'
        ELSE 'failed'
    END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION finish_mail(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamp with time zone) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending') THEN
        RAISE EXCEPTION 'finish_mail: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status = 'pending' AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_mail: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE mail_outbox AS m
    SET status = p_status,
        attempts = m.attempts + 1,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        finished_at = CASE WHEN p_status = 'pending' THEN NULL ELSE now() END,
        payload = CASE WHEN p_status = 'pending' THEN m.payload ELSE ''::bytea END,
        next_attempt_at = CASE WHEN p_status = 'pending' THEN p_retry_at ELSE m.next_attempt_at END
    WHERE m.id = p_id AND m.status = 'in_progress' AND m.claim_token = p_token;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION finish_mail(uuid,uuid,text,text,timestamp with time zone) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION finish_mail(uuid,uuid,text,text,timestamp with time zone) TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION mail_pending() RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT count(*) FROM mail_outbox WHERE status = 'pending'
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION mail_pending() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mail_pending() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION platform_registration_override() RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT s.registration_enabled FROM platform_settings AS s
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION platform_registration_override() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform_registration_override() TO proreta_api;

-- +goose StatementBegin
CREATE FUNCTION platform_retention_overrides() RETURNS TABLE(delivery_days integer, rollup_months integer)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    SELECT s.delivery_retention_days, s.rollup_retention_months FROM platform_settings AS s
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION platform_retention_overrides() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform_retention_overrides() TO proreta_worker;

-- +goose StatementBegin
CREATE FUNCTION purge_mail_outbox(p_keep interval) RETURNS integer
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public', 'pg_temp'
    AS $$
    WITH gone AS (
        DELETE FROM mail_outbox AS m
        WHERE m.id IN (
            SELECT e.id FROM mail_outbox AS e
            WHERE e.finished_at IS NOT NULL AND e.finished_at < now() - p_keep
            ORDER BY e.finished_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_mail_outbox(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_mail_outbox(interval) TO proreta_worker;

-- =============================================================================================
-- Migration table
-- =============================================================================================

-- api, worker and jobs read the newest applied migration at start and refuse to run when the
-- schema is newer than the binary (migrations.CheckNotAhead). The version table holds no tenant
-- data and no secrets; SELECT is all they get. goose creates it before this migration runs, so
-- the default privileges above did not apply to it. The status page renderer has no table
-- privileges at all and does not check.
GRANT SELECT ON goose_db_version TO proreta_api, proreta_worker;

-- =============================================================================================
-- Seeds
-- =============================================================================================

-- The platform location of the installation's own worker. The id is fixed so every environment
-- has the same one; PRORETA_WORKER_LOCATION defaults to its code.
INSERT INTO probe_locations (id, code, name, provider, region, kind)
VALUES ('01920000-0000-7000-8000-000000000001', 'default', 'Default', 'self-hosted', 'default', 'platform');

-- The only seeded plan: no quotas (every limit is the largest value a limit may hold). Every
-- tenant starts on it (tenants.plan_key default and auth_create_tenant). It is a system plan: the
-- admin panel can edit its limits but not delete it. Other plans are created in the admin panel.
INSERT INTO plans (key, name, limits, is_system) VALUES
    ('internal', 'Internal', '{"max_monitors": 2147483647, "max_heartbeats": 2147483647, "max_channels": 2147483647,
      "max_members": 2147483647, "max_projects": 2147483647, "max_status_pages": 2147483647,
      "max_status_page_domains": 2147483647, "max_agents": 2147483647}', true);

-- The single rows of the settings tables; the admin panel updates them, nothing inserts more.
INSERT INTO email_settings (id) VALUES (true);
INSERT INTO platform_settings (id) VALUES (true);

-- The first three monthly partitions of check_results. Last, so they are created with every
-- grant and policy of the parent in place.
SELECT ensure_partitions();
