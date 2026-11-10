-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The complete schema of a new installation. The database, the roles and the citext extension
-- are created by `proretactl db bootstrap` before this runs; migrations never create them.
-- Sections: privileges, tenancy and auth, monitors and projects, results, incidents and
-- notifications, heartbeats, status pages, invitations and OAuth, plans, platform admin,
-- grants on the migration table, seeds. Within a section: tables, then indexes, RLS and grants,
-- then functions.

-- =============================================================================================
-- Default privileges
-- =============================================================================================

-- Every object proreta_owner creates from here on. Runtime roles get DML on tables (RLS still
-- filters tenant tables) and sequence usage; tables that need less revoke it explicitly.
-- Functions get no EXECUTE by default: SECURITY DEFINER functions are granted one by one. The
-- function default is global (not IN SCHEMA) because a per-schema default can only add to the
-- global one, and the global one grants EXECUTE to PUBLIC.
ALTER DEFAULT PRIVILEGES FOR ROLE proreta_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO proreta_api, proreta_worker;
ALTER DEFAULT PRIVILEGES FOR ROLE proreta_owner IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO proreta_api, proreta_worker;
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
-- only. Tenants are created only through auth_create_tenant, which picks the plan.
CREATE TABLE tenants (
    id          uuid        PRIMARY KEY,
    slug        text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    -- Per-tenant overrides of the plan limits, resolved key by key.
    limits      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,
    -- References plans (key); the foreign key is added in the plans section.
    plan_key    text        NOT NULL DEFAULT 'free',
    CONSTRAINT tenants_limits_is_object CHECK (jsonb_typeof(limits) = 'object')
);
-- The api never rewrites a tenant's plan, limits, status or deletion. SELECT ... FOR UPDATE
-- (tenants.LockQuota) needs UPDATE on at least one column, hence updated_at.
REVOKE INSERT, UPDATE, DELETE ON tenants FROM proreta_api;
GRANT UPDATE (updated_at) ON tenants TO proreta_api;
REVOKE INSERT, UPDATE, DELETE ON tenants FROM proreta_worker;

-- Users are global (one person can belong to several tenants). users has no RLS, so grants are
-- the only guard: the worker needs nothing; the api may not delete users, rewrite id or
-- created_at, or write is_platform_admin, which only auth_sync_platform_admin changes.
CREATE TABLE users (
    id                 uuid        PRIMARY KEY,
    email              citext      NOT NULL UNIQUE CHECK (length(email) BETWEEN 3 AND 320),
    password_hash      text,
    display_name       text        NOT NULL DEFAULT '' CHECK (length(display_name) <= 200),
    status             text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    is_platform_admin  boolean     NOT NULL DEFAULT false,
    -- When an identity provider verified the email (an OAuth sign-up, or a password user who
    -- linked a provider with the same verified address). NULL: never verified.
    email_verified_at  timestamptz
);
REVOKE ALL ON users FROM proreta_worker;
REVOKE INSERT, UPDATE, DELETE ON users FROM proreta_api;
GRANT INSERT (id, email, password_hash, display_name, email_verified_at) ON users TO proreta_api;
GRANT UPDATE (email, password_hash, display_name, status, updated_at, email_verified_at) ON users TO proreta_api;

CREATE TABLE memberships (
    tenant_id     uuid        NOT NULL REFERENCES tenants (id),
    user_id       uuid        NOT NULL REFERENCES users (id),
    role          text        NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- Set whenever a session is created in this tenant; a login enters the organization the
    -- user used last. NULL falls back to the default tenant, then the oldest membership.
    last_used_at  timestamptz,
    PRIMARY KEY (tenant_id, user_id)
);
CREATE INDEX memberships_user_id_idx ON memberships (user_id);
REVOKE ALL ON memberships FROM proreta_worker;

-- OAuth identities (Google, GitHub). No provider tokens are stored: only the stable subject, the
-- verified email the provider reported last and, for Google, the Workspace domain (hd) of the
-- last sign-in, which is the proof of control for claiming a domain.
CREATE TABLE auth_identities (
    user_id        uuid        NOT NULL REFERENCES users (id),
    provider       text        NOT NULL CHECK (length(provider) BETWEEN 1 AND 64),
    subject        text        NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
    created_at     timestamptz NOT NULL DEFAULT now(),
    email          citext      CHECK (length(email) BETWEEN 3 AND 320),
    hosted_domain  text        CHECK (length(hosted_domain) BETWEEN 1 AND 253),
    last_login_at  timestamptz,
    PRIMARY KEY (provider, subject),
    CONSTRAINT auth_identities_known_provider CHECK (provider IN ('google', 'github'))
);
CREATE INDEX auth_identities_user_id_idx ON auth_identities (user_id);
-- One identity per provider and user: linking a second Google account means unlinking first.
CREATE UNIQUE INDEX auth_identities_user_provider_key ON auth_identities (user_id, provider);
REVOKE ALL ON auth_identities FROM proreta_worker;
-- The api refreshes what the provider reported at each sign-in; subject and owner never change.
REVOKE UPDATE ON auth_identities FROM proreta_api;
GRANT UPDATE (email, hosted_domain, last_login_at) ON auth_identities TO proreta_api;

-- Only the SHA-256 of the session token is stored. A session belongs to one membership.
CREATE TABLE sessions (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL,
    tenant_id     uuid        NOT NULL,
    token_hash    bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    csrf_token    text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    ip            inet,
    user_agent    text        CHECK (length(user_agent) <= 512),
    FOREIGN KEY (tenant_id, user_id) REFERENCES memberships (tenant_id, user_id) ON DELETE CASCADE
);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
CREATE INDEX sessions_tenant_user_idx ON sessions (tenant_id, user_id);
REVOKE ALL ON sessions FROM proreta_worker;

CREATE TABLE audit_log (
    id             uuid        PRIMARY KEY,
    tenant_id      uuid        NOT NULL REFERENCES tenants (id),
    actor_user_id  uuid        REFERENCES users (id),
    action         text        NOT NULL CHECK (length(action) BETWEEN 1 AND 100),
    entity_type    text        NOT NULL CHECK (length(entity_type) BETWEEN 1 AND 100),
    entity_id      text,
    metadata       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    ip             inet,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_tenant_created_idx ON audit_log (tenant_id, created_at DESC);
-- Insert-only for runtime roles.
REVOKE SELECT, UPDATE, DELETE ON audit_log FROM proreta_api, proreta_worker;

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memberships
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- The login and the organization switcher read the user's own memberships before the tenant is
-- known (auth_login_tenant, auth_user_organizations, auth_count_other_memberships).
CREATE POLICY owner_membership_lookup ON memberships FOR SELECT TO proreta_owner USING (true);

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sessions
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- auth_lookup_session must find a session before the tenant is known; purge_expired_sessions
-- deletes expired ones across tenants.
CREATE POLICY owner_session_lookup ON sessions FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_session_purge ON sessions FOR DELETE TO proreta_owner USING (expires_at < now());

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- Session lookup by token hash. SECURITY DEFINER makes it run as proreta_owner (allowed by
-- owner_session_lookup); search_path is pinned so a caller cannot shadow objects; it returns
-- only a live session for an exact 32-byte hash, so it cannot be used to enumerate sessions.
-- Sessions of disabled users and of suspended or deleted tenants are not returned, so disabling
-- either takes effect on the next request (users and tenants have no RLS; the owner reads them).
-- +goose StatementBegin
CREATE FUNCTION auth_lookup_session(p_token_hash bytea)
RETURNS TABLE (
    id            uuid,
    user_id       uuid,
    tenant_id     uuid,
    csrf_token    text,
    created_at    timestamptz,
    expires_at    timestamptz,
    last_seen_at  timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- The tenant a login binds to, or NULL: live tenants first, then active before suspended, then
-- the last used organization, then the default tenant (the system admin), then the oldest
-- membership. Suspended and deleted tenants are returned when nothing better exists, so the
-- login can answer "account suspended" after a correct password (it checks the status after the
-- audit). It returns only a tenant id for a user id the caller already resolved from the
-- credentials.
-- +goose StatementBegin
CREATE FUNCTION auth_login_tenant(p_user_id uuid)
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- Creates a tenant and returns its id, or NULL when the slug is taken. The plan is decided here,
-- not by the caller: internal for the default tenant, free for every other one, so the api role
-- cannot create a tenant on a bigger plan. The id is a UUIDv7 generated by the app.
-- +goose StatementBegin
CREATE FUNCTION auth_create_tenant(p_id uuid, p_slug text, p_name text)
RETURNS uuid
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    INSERT INTO tenants (id, slug, name, plan_key)
    VALUES (p_id, p_slug, p_name, CASE WHEN p_slug = 'default' THEN 'internal' ELSE 'free' END)
    ON CONFLICT (slug) DO NOTHING
    RETURNING id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_create_tenant(uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_create_tenant(uuid, text, text) TO proreta_api;

-- The number of the user's memberships in tenants other than p_tenant_id, deleted tenants
-- included. BootstrapAdmin refuses to adopt an existing user as the admin of the default tenant
-- when this is not zero (a stranger who registered the configured email).
-- +goose StatementBegin
CREATE FUNCTION auth_count_other_memberships(p_user_id uuid, p_tenant_id uuid)
RETURNS integer
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT count(*)::integer
    FROM memberships AS m
    WHERE m.user_id = p_user_id AND m.tenant_id <> p_tenant_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_count_other_memberships(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_count_other_memberships(uuid, uuid) TO proreta_api;

-- Deletes every session of the user in every tenant and returns how many. The sessions are found
-- through owner_session_lookup and deleted tenant by tenant under the tenant isolation policy.
-- The caller's app.tenant_id is restored before returning (a function SET clause cannot name a
-- custom setting without superuser), so it does not leak into the caller's transaction.
-- +goose StatementBegin
CREATE FUNCTION auth_revoke_user_sessions(p_user_id uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- The organizations of a user for the switcher: every membership in a tenant that is not
-- deleted, with the tenant's name and status and the user's role. Cross-tenant read of one
-- user's own memberships only.
-- +goose StatementBegin
CREATE FUNCTION auth_user_organizations(p_user_id uuid)
RETURNS TABLE (
    tenant_id  uuid,
    slug       text,
    name       text,
    status     text,
    role       text,
    joined_at  timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- =============================================================================================
-- Projects, monitors and probe locations (internal/projects, internal/monitors, internal/scheduler)
-- =============================================================================================

-- Projects inside an organization. Every tenant has exactly one Default project, created by the
-- tenants_create_default_project trigger below.
CREATE TABLE projects (
    id           uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL REFERENCES tenants (id),
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    slug         text        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    -- The Default project is never deleted; every member of the organization is a member of a
    -- project with all_members, without a project_members row.
    is_default   boolean     NOT NULL DEFAULT false,
    all_members  boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    UNIQUE (tenant_id, id),
    CONSTRAINT projects_default_live CHECK (NOT is_default OR deleted_at IS NULL)
);
CREATE UNIQUE INDEX projects_slug_idx ON projects (tenant_id, slug) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX projects_one_default_idx ON projects (tenant_id) WHERE is_default;
REVOKE DELETE ON projects FROM proreta_api;
REVOKE ALL ON projects FROM proreta_worker;

-- Explicit project membership. role overrides the organization role inside the project (member
-- or viewer; NULL = the organization role); the effective role is the lower of the two.
-- Removing the organization membership removes these rows.
CREATE TABLE project_members (
    tenant_id   uuid        NOT NULL,
    project_id  uuid        NOT NULL,
    user_id     uuid        NOT NULL,
    role        text        CHECK (role IN ('member', 'viewer')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_id, user_id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES memberships (tenant_id, user_id) ON DELETE CASCADE
);
CREATE INDEX project_members_user_idx ON project_members (tenant_id, user_id);
REVOKE ALL ON project_members FROM proreta_worker;

-- Probe locations are global (not tenant-scoped): every tenant may read them, only migrations
-- and the owner change them.
CREATE TABLE probe_locations (
    id          uuid        PRIMARY KEY,
    code        text        NOT NULL UNIQUE CHECK (code ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    provider    text        NOT NULL CHECK (length(provider) BETWEEN 1 AND 100),
    region      text        NOT NULL CHECK (length(region) BETWEEN 1 AND 100),
    kind        text        NOT NULL CHECK (kind IN ('platform', 'agent')),
    status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at  timestamptz NOT NULL DEFAULT now()
);
REVOKE INSERT, UPDATE, DELETE ON probe_locations FROM proreta_api, proreta_worker;

-- Check parameters live in config (http: url, expectedStatus; heartbeat: graceSeconds) so other
-- check types need no schema change. Soft delete only. A heartbeat has no target: the client
-- pings a secret URL, its expected interval is interval_seconds, its grace config.graceSeconds,
-- and it always has failure_threshold 1 (one missed deadline is down); timeout_seconds keeps
-- the default 10.
CREATE TABLE monitors (
    id                     uuid        PRIMARY KEY,
    tenant_id              uuid        NOT NULL REFERENCES tenants (id),
    name                   text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    type                   text        NOT NULL DEFAULT 'http' CHECK (type IN ('http', 'heartbeat')),
    config                 jsonb       NOT NULL CHECK (jsonb_typeof(config) = 'object'),
    interval_seconds       integer     NOT NULL DEFAULT 60 CHECK (interval_seconds > 0),
    timeout_seconds        integer     NOT NULL DEFAULT 10 CHECK (timeout_seconds > 0 AND timeout_seconds < interval_seconds),
    failure_threshold      integer     NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 3),
    notifications_enabled  boolean     NOT NULL DEFAULT true,
    paused                 boolean     NOT NULL DEFAULT false,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    deleted_at             timestamptz,
    project_id             uuid        NOT NULL,
    UNIQUE (tenant_id, id),
    -- A monitor cannot point at another tenant's project.
    CONSTRAINT monitors_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    -- The fixed http schedule as named constraints, so relaxing it is one DROP CONSTRAINT.
    CONSTRAINT monitors_mvp_interval CHECK (type <> 'http' OR interval_seconds = 60),
    CONSTRAINT monitors_mvp_timeout CHECK (type <> 'http' OR timeout_seconds = 10),
    -- graceSeconds must be a plain non-negative integer, so every cast of it to integer succeeds
    -- (60.0 would fail ::integer and json.Unmarshal into an int). CASE guards the casts: SQL does
    -- not promise to evaluate AND operands left to right.
    CONSTRAINT monitors_heartbeat_check CHECK (
        type <> 'heartbeat' OR (
            interval_seconds BETWEEN 60 AND 86400
            AND failure_threshold = 1
            AND CASE WHEN jsonb_typeof(config -> 'graceSeconds') = 'number'
                          AND (config ->> 'graceSeconds') ~ '^[0-9]+$'
                     THEN (config ->> 'graceSeconds')::numeric BETWEEN 0 AND 3600
                     ELSE false END
        )
    )
);
-- Listing and the limit count read live monitors of one tenant.
CREATE INDEX monitors_tenant_live_idx ON monitors (tenant_id, created_at) WHERE deleted_at IS NULL;
CREATE INDEX monitors_project_idx ON monitors (tenant_id, project_id) WHERE deleted_at IS NULL;
REVOKE DELETE ON monitors FROM proreta_api;
REVOKE INSERT, UPDATE, DELETE ON monitors FROM proreta_worker;

-- Which locations run which monitor.
CREATE TABLE monitor_locations (
    tenant_id    uuid        NOT NULL,
    monitor_id   uuid        NOT NULL,
    location_id  uuid        NOT NULL REFERENCES probe_locations (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (monitor_id, location_id),
    UNIQUE (tenant_id, monitor_id, location_id),
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id)
);
CREATE INDEX monitor_locations_location_idx ON monitor_locations (location_id);
REVOKE UPDATE, DELETE ON monitor_locations FROM proreta_api;
REVOKE INSERT, UPDATE, DELETE ON monitor_locations FROM proreta_worker;

-- Schedule and per-location state; very high update rate. Owned by internal/scheduler. A free
-- lease is lease_token IS NULL; an expired one (crashed worker) is found through the second
-- partial index. fillfactor leaves room for in-page updates. The worker writes the lease and the
-- schedule; the api resets them only through reset_monitor_runtime. A heartbeat's row exists only
-- for the incident machine's per-location state and is parked at next_run_at = 'infinity', so it
-- never sits in the due index.
CREATE TABLE monitor_location_state (
    tenant_id             uuid        NOT NULL,
    monitor_id            uuid        NOT NULL,
    location_id           uuid        NOT NULL,
    status                text        NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'up', 'down')),
    consecutive_failures  integer     NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    next_run_at           timestamptz NOT NULL,
    lease_token           uuid,
    lease_until           timestamptz,
    last_checked_at       timestamptz,
    PRIMARY KEY (monitor_id, location_id),
    FOREIGN KEY (tenant_id, monitor_id, location_id) REFERENCES monitor_locations (tenant_id, monitor_id, location_id),
    CHECK ((lease_token IS NULL) = (lease_until IS NULL))
) WITH (fillfactor = 70);
CREATE INDEX monitor_location_state_due_idx ON monitor_location_state (next_run_at) WHERE lease_token IS NULL;
CREATE INDEX monitor_location_state_lease_idx ON monitor_location_state (lease_until) WHERE lease_token IS NOT NULL;
REVOKE UPDATE, DELETE ON monitor_location_state FROM proreta_api;
REVOKE INSERT, DELETE ON monitor_location_state FROM proreta_worker;

-- Aggregated status of a monitor, updated by the worker's state machine in the result
-- transaction. The api writes only the status reset on pause, resume and URL change, and
-- current_incident_id when it resolves an incident. current_incident_id references incidents;
-- the foreign key is added in the incidents section.
CREATE TABLE monitor_state (
    tenant_id             uuid        NOT NULL,
    monitor_id            uuid        PRIMARY KEY,
    status                text        NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'up', 'down')),
    status_changed_at     timestamptz NOT NULL DEFAULT now(),
    last_error_kind       text        CHECK (length(last_error_kind) BETWEEN 1 AND 64),
    last_checked_at       timestamptz,
    tls_expires_at        timestamptz,
    tls_warned_not_after  timestamptz,
    current_incident_id   uuid,
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id)
) WITH (fillfactor = 80);
REVOKE UPDATE, DELETE ON monitor_state FROM proreta_api;
GRANT UPDATE (status, status_changed_at, last_error_kind, tls_expires_at, tls_warned_not_after, current_incident_id)
    ON monitor_state TO proreta_api;
REVOKE INSERT, DELETE ON monitor_state FROM proreta_worker;

ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON projects
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE project_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_members FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_members
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE monitors ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitors FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON monitors
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- Read by the cross-tenant claim, heartbeat, reminder and admin functions.
CREATE POLICY owner_claim_read ON monitors FOR SELECT TO proreta_owner USING (true);

ALTER TABLE monitor_locations ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_locations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON monitor_locations
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE monitor_location_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_location_state FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON monitor_location_state
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_claim_read ON monitor_location_state FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_claim_lease ON monitor_location_state FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);

ALTER TABLE monitor_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_state FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON monitor_state
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- A UUIDv7 from the clock, for the rows the database creates itself (the application generates
-- every other id). Not a definer function: it runs as its caller, and only proreta_owner may
-- execute it (the trigger below and enqueue_incident_reminders run as the owner).
-- +goose StatementBegin
CREATE FUNCTION uuidv7_now()
RETURNS uuid
LANGUAGE sql
VOLATILE
SET search_path = public, pg_temp
AS $$
    SELECT encode(set_bit(set_bit(overlay(uuid_send(gen_random_uuid())
               PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
               FROM 1 FOR 6), 52, 1), 53, 1), 'hex')::uuid
$$;
-- +goose StatementEnd

-- Every new tenant gets its Default project, whoever creates it (auth_create_tenant, or a test
-- fixture). Invoker rights: the insert runs under the tenant isolation policy with app.tenant_id
-- set to the new tenant for the duration of the insert, then restored.
-- +goose StatementBegin
CREATE FUNCTION tenants_create_default_project()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
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
CREATE TRIGGER tenants_create_default_project AFTER INSERT ON tenants
    FOR EACH ROW EXECUTE FUNCTION tenants_create_default_project();

-- Claims up to n due (monitor, location) pairs of one location across all tenants: http only,
-- not paused, not deleted, tenant active and not deleted, location active, next_run_at reached,
-- lease free or expired. Monitors of a suspended tenant keep their schedule and run again once
-- it is active. Each claimed row gets a fresh lease_token and a 30 s lease; the token fences off
-- a stale writer. SKIP LOCKED lets several workers claim concurrently without waiting on each
-- other or double-claiming. next_run_at is returned unchanged so the caller can measure
-- scheduler lag.
-- +goose StatementBegin
CREATE FUNCTION claim_due_monitors(n integer, p_location text)
RETURNS TABLE (
    tenant_id          uuid,
    monitor_id         uuid,
    location_id        uuid,
    lease_token        uuid,
    lease_until        timestamptz,
    url                text,
    expected_status    text,
    failure_threshold  integer,
    interval_seconds   integer,
    timeout_seconds    integer,
    next_run_at        timestamptz
)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
          AND m.type = 'http'
          AND NOT m.paused
          AND m.deleted_at IS NULL
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
              m.config ->> 'url', COALESCE(m.config ->> 'expectedStatus', '2xx'),
              m.failure_threshold, m.interval_seconds, m.timeout_seconds, s.next_run_at
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_due_monitors(integer, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_due_monitors(integer, text) TO proreta_worker;

-- Resets the schedule of one live monitor of the caller's tenant (pause, resume, URL change):
-- every location back to status unknown with zero consecutive failures, the lease dropped (which
-- fences off a check still in flight: its lease check fails and the result is discarded) and the
-- next run after a random start jitter of up to 30 s (scheduler.MaxStartJitter); heartbeats stay
-- parked at 'infinity'. The tenant comes from app.tenant_id, set by db.WithTenantTx, never from
-- an argument: the function runs as proreta_owner, whose owner_claim_* policies see every
-- tenant, so the explicit tenant filter is what keeps it inside the caller's tenant. Returns the
-- number of locations reset (0 when the monitor is not the caller's or is deleted).
-- +goose StatementBegin
CREATE FUNCTION reset_monitor_runtime(p_monitor_id uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
        next_run_at = CASE WHEN m.type = 'http' THEN now() + random() * interval '30 seconds'
                           ELSE 'infinity'::timestamptz END
    FROM monitors AS m
    WHERE m.tenant_id = caller_tenant AND m.id = p_monitor_id AND m.deleted_at IS NULL
      AND s.tenant_id = m.tenant_id AND s.monitor_id = m.id;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION reset_monitor_runtime(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION reset_monitor_runtime(uuid) TO proreta_api;

-- =============================================================================================
-- Check results and rollups (internal/results)
-- =============================================================================================

-- Raw check results, partitioned by month on checked_at. No row id: the key is (monitor, time,
-- location), which also serves the "latest checks of a monitor" query. Retention detaches and
-- drops whole monthly partitions (list_droppable_partitions and the two functions after it); the
-- BRIN index serves the time-range scans of compute_rollups and costs almost nothing to maintain
-- on append-only data. latency_ms is NULL when no response arrived (DNS, connect, TLS, timeout).
CREATE TABLE check_results (
    tenant_id      uuid        NOT NULL,
    monitor_id     uuid        NOT NULL,
    location_id    uuid        NOT NULL,
    checked_at     timestamptz NOT NULL,
    ok             boolean     NOT NULL,
    status_code    smallint    CHECK (status_code BETWEEN 100 AND 599),
    latency_ms     integer     CHECK (latency_ms >= 0),
    error_kind     text        CHECK (length(error_kind) BETWEEN 1 AND 64),
    tls_not_after  timestamptz,
    PRIMARY KEY (monitor_id, checked_at, location_id),
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id),
    CHECK (ok = (error_kind IS NULL))
) PARTITION BY RANGE (checked_at);
CREATE INDEX check_results_checked_at_brin ON check_results USING brin (checked_at);
-- Append-only: the worker inserts, the api reads. ensure_partitions repeats these grants on
-- every partition.
REVOKE INSERT, UPDATE, DELETE ON check_results FROM proreta_api;
REVOKE UPDATE, DELETE ON check_results FROM proreta_worker;

-- Hourly rollups per (monitor, location, hour), kept 13 months. latency_histogram has 11
-- buckets with fixed upper edges in milliseconds (inclusive):
--   10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, and an overflow bucket (> 10000).
-- The edges are mirrored by results.LatencyBucketEdgesMs in Go; a test keeps them in sync.
-- Only results with a latency are counted in the histogram and latency_sum_ms, so the mean is
-- latency_sum_ms / sum(latency_histogram) and p95 can be merged across hours by adding arrays.
CREATE TABLE result_rollups_hourly (
    tenant_id          uuid        NOT NULL,
    monitor_id         uuid        NOT NULL,
    location_id        uuid        NOT NULL,
    hour               timestamptz NOT NULL CHECK (hour = date_trunc('hour', hour, 'UTC')),
    total              integer     NOT NULL CHECK (total > 0),
    ok_count           integer     NOT NULL CHECK (ok_count BETWEEN 0 AND total),
    latency_sum_ms     bigint      NOT NULL CHECK (latency_sum_ms >= 0),
    latency_histogram  integer[]   NOT NULL CHECK (cardinality(latency_histogram) = 11),
    computed_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (monitor_id, location_id, hour),
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id)
);
CREATE INDEX result_rollups_hourly_hour_idx ON result_rollups_hourly (hour);
-- Written only by compute_rollups (as proreta_owner); runtime roles read.
REVOKE INSERT, UPDATE, DELETE ON result_rollups_hourly FROM proreta_api, proreta_worker;

-- The rollup watermark: one row, written only by compute_rollups_catch_up. It is not tenant data
-- (one timestamp for the whole system), so it has no tenant_id and is outside the tenant RLS
-- rules. The runtime roles get no privilege on it, and RLS is enabled without a policy, so even a
-- stray future grant shows them no row. The owner is not subject to non-forced RLS, which is what
-- the definer function needs.
CREATE TABLE rollup_state (
    singleton   boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    -- Every closed UTC hour before the watermark has been rolled up once.
    watermark   timestamptz NOT NULL CHECK (watermark = date_trunc('hour', watermark, 'UTC')),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON rollup_state FROM PUBLIC, proreta_api, proreta_worker;
ALTER TABLE rollup_state ENABLE ROW LEVEL SECURITY;

ALTER TABLE check_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_results FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON check_results
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- Reads of check_results always go through the parent, whose policies apply to all partitions.
CREATE POLICY owner_rollup_read ON check_results FOR SELECT TO proreta_owner USING (true);

ALTER TABLE result_rollups_hourly ENABLE ROW LEVEL SECURITY;
ALTER TABLE result_rollups_hourly FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON result_rollups_hourly
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_rollup_write ON result_rollups_hourly TO proreta_owner USING (true) WITH CHECK (true);

-- Creates the check_results partitions for the current, next and following UTC month if they
-- are missing; a new partition gets RLS (enabled, forced, tenant policy) and the same grants as
-- the parent in the same transaction, so it is never visible without them. Idempotent and
-- serialised by a transaction advisory lock. Returns the number of partitions created.
-- Partition names are check_results_pYYYYMM; the retention functions rely on that.
-- +goose StatementBegin
CREATE FUNCTION ensure_partitions()
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
SET lock_timeout = '5s'
AS $$
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
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION ensure_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ensure_partitions() TO proreta_worker;

-- Recomputes the rollups of the closed UTC hours in [from_hour, to_hour): to_hour is capped at
-- the start of the current hour, so an open hour is never rolled up. Upsert, so running it again
-- (or after late results) gives the same numbers as one run over all raw rows. Concurrent calls
-- (two jobs replicas, or a manual backfill) are serialised by a transaction advisory lock taken
-- before the INSERT, whose snapshot is therefore taken after the other call committed, so an
-- older snapshot never overwrites a newer upsert. jobs reaches it through
-- compute_rollups_catch_up; a manual backfill of hours older than the catch-up window calls it
-- directly. Returns the number of rollup rows written.
-- +goose StatementBegin
CREATE FUNCTION compute_rollups(from_hour timestamptz, to_hour timestamptz)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
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
REVOKE ALL ON FUNCTION compute_rollups(timestamptz, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION compute_rollups(timestamptz, timestamptz) TO proreta_worker;

-- Rolls up the closed UTC hours from the watermark onwards, at most p_max_hours of them, and
-- moves the watermark to the end of what it covered, so a long jobs outage is caught up over
-- several runs (and retention never keeps a partition forever for want of rollups). On the first
-- run (no watermark yet) it starts at the earliest raw hour of the last 7 days. The watermark is
-- never older than 7 days: hours before that are left to a manual compute_rollups(from, to) run.
-- Every run also recomputes the last p_lookback_hours closed hours, so late results are
-- absorbed. The upsert is compute_rollups itself, so idempotency, the advisory lock that
-- serialises concurrent calls and the open-hour cap are the same; the lock is taken before the
-- watermark is read. Returns the number of rollup rows written.
-- +goose StatementBegin
CREATE FUNCTION compute_rollups_catch_up(p_max_hours integer, p_lookback_hours integer)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
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
REVOKE ALL ON FUNCTION compute_rollups_catch_up(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION compute_rollups_catch_up(integer, integer) TO proreta_worker;

-- Retention in three steps, each its own transaction driven by jobs: list the candidates, detach
-- one, drop it. DETACH PARTITION ... CONCURRENTLY cannot run inside a function or a transaction
-- block, and proreta_worker does not own check_results, so the detach is a plain DETACH inside a
-- definer function under a short lock_timeout: the ACCESS EXCLUSIVE lock on the parent is held
-- only for the catalog change, and a busy parent makes the call fail fast (it is retried) instead
-- of queueing inserts behind it. The DROP then touches only the detached table, never the parent.

-- Partitions named check_results_pYYYYMM whose month ended more than 30 days ago (never the
-- current month), attached or already detached by an earlier, interrupted run. Catalog only:
-- rollup completeness is checked by detach_expired_partition.
-- +goose StatementBegin
CREATE FUNCTION list_droppable_partitions()
RETURNS TABLE (name text, detached boolean)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
AS $$
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
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION list_droppable_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION list_droppable_partitions() TO proreta_worker;

-- Detaches one expired partition after checking that its rollups are complete (every
-- (monitor, location, hour) with raw rows has a rollup with the same total). Returns false when
-- the partition is kept (incomplete rollups, or nothing to detach). The name is validated
-- against the naming pattern and must be an attached child of check_results owned by
-- proreta_owner; it reaches the DDL only through format('%I').
-- +goose StatementBegin
CREATE FUNCTION detach_expired_partition(p_name text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
SET lock_timeout = '1s'
AS $$
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
    SELECT EXISTS (
        SELECT 1
        FROM (
            SELECT c.monitor_id, c.location_id, date_trunc('hour', c.checked_at) AS hour, count(*) AS n
            FROM check_results AS c
            WHERE c.checked_at >= lo AND c.checked_at < hi
            GROUP BY 1, 2, 3
        ) AS raw
        LEFT JOIN result_rollups_hourly AS r
          ON r.monitor_id = raw.monitor_id AND r.location_id = raw.location_id AND r.hour = raw.hour
        WHERE r.total IS DISTINCT FROM raw.n
    ) INTO incomplete;
    IF incomplete THEN
        RETURN false;
    END IF;
    EXECUTE format('ALTER TABLE check_results DETACH PARTITION %I', p_name);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION detach_expired_partition(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION detach_expired_partition(text) TO proreta_worker;

-- Drops one detached, expired partition. Refuses anything that is still attached, does not match
-- the naming pattern, is not a plain table owned by proreta_owner or is not expired, so it cannot
-- be used to drop another table. Returns false when there is nothing to drop.
-- +goose StatementBegin
CREATE FUNCTION drop_detached_partition(p_name text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
SET lock_timeout = '5s'
AS $$
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
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION drop_detached_partition(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION drop_detached_partition(text) TO proreta_worker;

-- Deletes expired sessions; run by jobs. Returns the number deleted.
-- +goose StatementBegin
CREATE FUNCTION purge_expired_sessions()
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH d AS (DELETE FROM sessions WHERE expires_at < now() RETURNING 1)
    SELECT count(*)::integer FROM d
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION purge_expired_sessions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_expired_sessions() TO proreta_worker;

-- =============================================================================================
-- Incidents and notifications (internal/incidents, internal/notify)
-- =============================================================================================

-- An automatic incident covers one monitor and records it in monitor_id, so the database can
-- enforce at most one open incident per monitor; incident_monitors is many-to-many so that manual
-- incidents spanning monitors need no schema change (they leave monitor_id NULL). reason is the
-- error kind that opened it; resolve_reason says why it ended.
CREATE TABLE incidents (
    id              uuid        PRIMARY KEY,
    tenant_id       uuid        NOT NULL REFERENCES tenants (id),
    started_at      timestamptz NOT NULL,
    resolved_at     timestamptz,
    resolve_reason  text        CHECK (resolve_reason IN ('recovered', 'paused', 'deleted', 'url_changed')),
    reason          text        CHECK (length(reason) BETWEEN 1 AND 64),
    created_at      timestamptz NOT NULL DEFAULT now(),
    monitor_id      uuid,
    UNIQUE (tenant_id, id),
    CONSTRAINT incidents_monitor_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id),
    CHECK ((resolved_at IS NULL) = (resolve_reason IS NULL))
);
CREATE INDEX incidents_tenant_started_idx ON incidents (tenant_id, started_at DESC);
CREATE UNIQUE INDEX incidents_one_open_per_monitor_idx ON incidents (monitor_id)
    WHERE resolved_at IS NULL AND monitor_id IS NOT NULL;
-- The worker opens and resolves incidents; the api resolves them on pause and delete.
REVOKE DELETE ON incidents FROM proreta_api, proreta_worker;
REVOKE INSERT ON incidents FROM proreta_api;

CREATE TABLE incident_monitors (
    tenant_id    uuid NOT NULL,
    incident_id  uuid NOT NULL,
    monitor_id   uuid NOT NULL,
    PRIMARY KEY (incident_id, monitor_id),
    FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents (tenant_id, id),
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id)
);
CREATE INDEX incident_monitors_monitor_idx ON incident_monitors (tenant_id, monitor_id);
REVOKE UPDATE, DELETE ON incident_monitors FROM proreta_api, proreta_worker;
REVOKE INSERT ON incident_monitors FROM proreta_api;

ALTER TABLE monitor_state ADD CONSTRAINT monitor_state_current_incident_fkey
    FOREIGN KEY (tenant_id, current_incident_id) REFERENCES incidents (tenant_id, id);

-- Notification destinations: Slack webhook channels, each with a name, the events it receives,
-- an optional mention and an optional reminder interval. config_encrypted is the AES-256-GCM
-- envelope (key id prefix) of the channel configuration, for Slack the Incoming Webhook URL; it
-- is never returned by the api. Deleted channels are kept (soft delete) because
-- notification_deliveries references them, but lose their webhook. status 'failed' is set by
-- jobs on a permanent delivery failure (404/410). project_id NULL is an organization-wide
-- channel.
CREATE TABLE notification_channels (
    id                uuid        PRIMARY KEY,
    tenant_id         uuid        NOT NULL REFERENCES tenants (id),
    type              text        NOT NULL CHECK (type IN ('slack_webhook')),
    config_encrypted  bytea       CHECK (length(config_encrypted) BETWEEN 1 AND 4096),
    status            text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'failed')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    name              text        NOT NULL DEFAULT 'Slack' CHECK (length(name) BETWEEN 1 AND 60),
    events            text[]      NOT NULL DEFAULT ARRAY['down', 'recovered', 'tls_expiring']::text[]
        CHECK (cardinality(events) BETWEEN 1 AND 3 AND events <@ ARRAY['down', 'recovered', 'tls_expiring']::text[]),
    -- Slack mention tokens as typed by the user (@here, @channel, <!subteam^ID>, <@UID>), at most
    -- three; the application validates them strictly, the check keeps anything else out.
    mention           text[]      NOT NULL DEFAULT '{}'::text[]
        CHECK (cardinality(mention) <= 3
               AND array_to_string(mention, ' ') ~ '^((@here|@channel|<!subteam\^[A-Z0-9]{2,20}>|<@[UW][A-Z0-9]{2,20}>)( |$))*$'),
    reminder_minutes  integer     NOT NULL DEFAULT 0
        CHECK (reminder_minutes = 0 OR reminder_minutes BETWEEN 5 AND 1440),
    deleted_at        timestamptz,
    project_id        uuid,
    UNIQUE (tenant_id, id),
    CONSTRAINT notification_channels_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    -- Deleting a channel wipes its encrypted webhook URL.
    CONSTRAINT notification_channels_config_present CHECK (deleted_at IS NOT NULL OR config_encrypted IS NOT NULL)
);
CREATE INDEX notification_channels_project_idx ON notification_channels (tenant_id, project_id) WHERE deleted_at IS NULL;
REVOKE DELETE ON notification_channels FROM proreta_api, proreta_worker;
REVOKE INSERT, UPDATE ON notification_channels FROM proreta_worker;
GRANT UPDATE (status, updated_at) ON notification_channels TO proreta_worker;

-- The channels a monitor selected. Routing: a monitor with a valid selection (channels that are
-- organization-wide or of its project) reaches those; a monitor without one reaches the active
-- channels of its own project. The same rule is in enqueue_incident_reminders and in
-- ListDeliveryChannels.
CREATE TABLE monitor_channels (
    tenant_id   uuid        NOT NULL,
    monitor_id  uuid        NOT NULL,
    channel_id  uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (monitor_id, channel_id),
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id),
    FOREIGN KEY (tenant_id, channel_id) REFERENCES notification_channels (tenant_id, id)
);
CREATE INDEX monitor_channels_channel_idx ON monitor_channels (tenant_id, channel_id);
REVOKE UPDATE ON monitor_channels FROM proreta_api;
REVOKE INSERT, UPDATE, DELETE ON monitor_channels FROM proreta_worker;

-- The transactional outbox: rows are inserted in the transaction of the state change and
-- delivered by jobs through claim_deliveries / finish_delivery. payload holds what the message
-- needs and never a secret (the webhook URL stays in the channel). lease_until is set while a
-- delivery is in progress, so rows of a dead jobs process go back to pending; claim_token fences
-- a jobs process whose lease expired (and whose row was claimed again) from recording a second
-- outcome.
CREATE TABLE notification_deliveries (
    id               uuid        PRIMARY KEY,
    tenant_id        uuid        NOT NULL,
    kind             text        NOT NULL CHECK (kind IN ('opened', 'resolved', 'tls_expiring', 'reminder')),
    monitor_id       uuid        NOT NULL,
    incident_id      uuid,
    channel_id       uuid        NOT NULL,
    payload          jsonb       NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status           text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'in_progress', 'sent', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    lease_until      timestamptz,
    last_error       text        CHECK (length(last_error) <= 500),
    created_at       timestamptz NOT NULL DEFAULT now(),
    sent_at          timestamptz,
    claim_token      uuid,
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id),
    FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents (tenant_id, id),
    FOREIGN KEY (tenant_id, channel_id) REFERENCES notification_channels (tenant_id, id),
    CHECK ((status = 'in_progress') = (lease_until IS NOT NULL)),
    CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
    CHECK (kind = 'tls_expiring' OR incident_id IS NOT NULL),
    CONSTRAINT notification_deliveries_claim_token_check CHECK ((status = 'in_progress') = (claim_token IS NOT NULL))
);
CREATE INDEX notification_deliveries_ready_idx ON notification_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX notification_deliveries_lease_idx ON notification_deliveries (lease_until) WHERE status = 'in_progress';
-- "Oldest undelivered row of this monitor" for the per-monitor ordering in claim_deliveries.
CREATE INDEX notification_deliveries_monitor_open_idx ON notification_deliveries (monitor_id, created_at, id)
    WHERE status IN ('pending', 'in_progress');
CREATE INDEX notification_deliveries_tenant_created_idx ON notification_deliveries (tenant_id, created_at DESC);
-- The newest 'opened' or 'reminder' delivery of an incident to a channel, for the reminders.
CREATE INDEX notification_deliveries_incident_channel_idx ON notification_deliveries (incident_id, channel_id, created_at)
    WHERE incident_id IS NOT NULL;
-- The worker inserts; status changes only through the definer functions.
REVOKE INSERT, UPDATE, DELETE ON notification_deliveries FROM proreta_api;
REVOKE UPDATE, DELETE ON notification_deliveries FROM proreta_worker;

ALTER TABLE incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON incidents
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_reminder_read ON incidents FOR SELECT TO proreta_owner USING (true);

ALTER TABLE incident_monitors ENABLE ROW LEVEL SECURITY;
ALTER TABLE incident_monitors FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON incident_monitors
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_channels
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_reminder_read ON notification_channels FOR SELECT TO proreta_owner USING (true);

ALTER TABLE monitor_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON monitor_channels
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_reminder_read ON monitor_channels FOR SELECT TO proreta_owner USING (true);

ALTER TABLE notification_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_deliveries
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_delivery_read ON notification_deliveries FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_delivery_update ON notification_deliveries FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_reminder_insert ON notification_deliveries FOR INSERT TO proreta_owner WITH CHECK (true);

-- Claims up to n deliveries across tenants, oldest first.
-- 1. Rows left in progress by a dead jobs process return to pending when their 2-minute lease
--    expires. The requeue counts as an attempt, so a message that kills or hangs every jobs
--    process (a poison message) ends as failed instead of being claimed forever; 5 is
--    jobs.MaxAttempts, and the jobs integration test fails if they drift apart.
-- 2. Pending deliveries of deleted tenants are marked failed once ('tenant_deleted'), so the
--    scans stop reading them again; suspended tenants keep waiting.
-- 3. Only the oldest undelivered row (pending or in progress) of each monitor and channel is
--    eligible, so a channel never has two deliveries of a monitor in flight and opened/resolved
--    arrive in order, while a channel waiting for its retry does not hold back the others.
--    Deliveries of suspended or deleted tenants are not claimed.
-- Claimed rows are marked in_progress with a new lease and a new claim token.
-- +goose StatementBegin
CREATE FUNCTION claim_deliveries(n integer)
RETURNS TABLE (
    id           uuid,
    tenant_id    uuid,
    kind         text,
    monitor_id   uuid,
    incident_id  uuid,
    channel_id   uuid,
    payload      jsonb,
    attempts     integer,
    created_at   timestamptz,
    claim_token  uuid
)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

    WITH ready AS (
        SELECT d.id
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
              WHERE o.monitor_id = d.monitor_id
                AND o.channel_id = d.channel_id
                AND o.status IN ('pending', 'in_progress')
                AND (o.created_at, o.id) < (d.created_at, d.id)
          )
        ORDER BY d.created_at, d.id
        LIMIT least(greatest(n, 0), 100)
        FOR UPDATE OF d SKIP LOCKED
    )
    UPDATE notification_deliveries AS d
    SET status = 'in_progress', lease_until = now() + interval '2 minutes', claim_token = gen_random_uuid()
    FROM ready
    WHERE d.id = ready.id
    RETURNING d.id, d.tenant_id, d.kind, d.monitor_id, d.incident_id, d.channel_id, d.payload, d.attempts, d.created_at,
              d.claim_token
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION claim_deliveries(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION claim_deliveries(integer) TO proreta_worker;

-- Records the outcome of a claimed delivery: 'sent', 'failed' (permanent) or 'pending' (retry
-- at retry_at). Counts the attempt and ends the lease. Returns false when the row is not in
-- progress under p_token (already finished, or requeued and possibly claimed again).
-- +goose StatementBegin
CREATE FUNCTION finish_delivery(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamptz)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending') THEN
        RAISE EXCEPTION 'finish_delivery: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status = 'pending' AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_delivery: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE notification_deliveries AS d
    SET status = p_status,
        attempts = d.attempts + 1,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        sent_at = CASE WHEN p_status = 'sent' THEN now() END,
        next_attempt_at = CASE WHEN p_status = 'pending' THEN p_retry_at ELSE d.next_attempt_at END
    WHERE d.id = p_id AND d.status = 'in_progress' AND d.claim_token = p_token;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION finish_delivery(uuid, uuid, text, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION finish_delivery(uuid, uuid, text, text, timestamptz) TO proreta_worker;

-- Outbox backlog for the jobs metric proreta_jobs_outbox_pending: a number only, across tenants.
-- Rows waiting for a retry count as pending. notification_deliveries_ready_idx keeps it cheap.
-- +goose StatementBegin
CREATE FUNCTION outbox_pending()
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT count(*) FROM notification_deliveries WHERE status = 'pending'
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION outbox_pending() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION outbox_pending() TO proreta_worker;

-- Deleting a channel (api, inside the tenant transaction that soft-deletes it) fails its pending
-- deliveries with last_error 'channel_deleted'. Only a channel of the caller's tenant that is
-- already deleted qualifies. Returns the number of deliveries failed.
-- +goose StatementBegin
CREATE FUNCTION fail_deleted_channel_deliveries(p_channel_id uuid)
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- Queues a 'reminder' delivery for every open incident and every channel that receives it
-- (active, not deleted, reminder_minutes > 0, event 'down', reached by the monitor under the
-- routing rule of monitor_channels; the monitor has notifications enabled, is not paused or
-- deleted; the tenant is active), once reminder_minutes have passed since the newest 'opened' or
-- 'reminder' delivery of that incident to that channel (or since the incident started when there
-- is none). At most p_limit rows per call (capped at 1000), oldest incidents first. jobs calls it
-- every minute; the function takes its own transaction advisory lock first, so concurrent calls
-- are serialised and the second one sees the first one's rows (each statement of a volatile SQL
-- function takes a new snapshot). Returns the number of rows queued.
-- +goose StatementBegin
CREATE FUNCTION enqueue_incident_reminders(p_limit integer)
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
          AND coalesce(
              (SELECT max(d.created_at) FROM notification_deliveries AS d
               WHERE d.incident_id = i.id AND d.channel_id = c.id AND d.kind IN ('opened', 'reminder')),
              i.started_at
          ) <= now() - make_interval(mins => c.reminder_minutes)
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

-- =============================================================================================
-- Heartbeats (internal/monitors)
-- =============================================================================================

-- One row per heartbeat monitor. Only the SHA-256 of the token is stored; the token is shown
-- once (create, regenerate) and cannot be shown again.
--   waiting_since   start of the current wait: creation or the last resume
--   last_ping_at    newest accepted ping (written by heartbeat_ping only)
--   applied_ping_at newest ping the incident machine has seen (jobs)
--   missed_deadline newest deadline already applied as missed (jobs), so one deadline opens at
--                   most one failure
-- The deadline is greatest(waiting_since, last_ping_at) + interval_seconds + graceSeconds.
CREATE TABLE heartbeat_tokens (
    tenant_id        uuid        NOT NULL,
    monitor_id       uuid        PRIMARY KEY,
    token_hash       bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_at       timestamptz NOT NULL DEFAULT now(),
    waiting_since    timestamptz NOT NULL DEFAULT now(),
    last_ping_at     timestamptz,
    applied_ping_at  timestamptz,
    missed_deadline  timestamptz,
    UNIQUE (tenant_id, monitor_id),
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id)
);
-- The api creates rows, replaces the token, restarts the wait on resume and locks the row; it
-- never writes the ping or the evaluation columns. jobs (proreta_worker) records what it applied.
REVOKE UPDATE, DELETE ON heartbeat_tokens FROM proreta_api;
GRANT UPDATE (token_hash, created_at, waiting_since, applied_ping_at) ON heartbeat_tokens TO proreta_api;
REVOKE ALL ON heartbeat_tokens FROM proreta_worker;
GRANT SELECT ON heartbeat_tokens TO proreta_worker;
GRANT UPDATE (applied_ping_at, missed_deadline) ON heartbeat_tokens TO proreta_worker;

ALTER TABLE heartbeat_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE heartbeat_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON heartbeat_tokens
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- heartbeat_ping and list_due_heartbeats run as proreta_owner before the tenant is known.
CREATE POLICY owner_heartbeat_read ON heartbeat_tokens FOR SELECT TO proreta_owner USING (true);
CREATE POLICY owner_heartbeat_ping ON heartbeat_tokens FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);

-- A ping: the public endpoint calls this with the SHA-256 of the token from the URL. For a live
-- heartbeat (not paused, not deleted, tenant active) it records the ping time; it returns the
-- tenant and monitor of any known token with recorded = false when nothing was recorded, and no
-- row for an unknown token. It reveals nothing else and accepts only an exact 32-byte hash, so
-- it cannot enumerate tokens. A ping first takes the row lock, then reads the clock: a ping that
-- waited for an evaluation (which holds the lock and decides with clock_timestamp() read under
-- it) records a time after that evaluation, never one from before the wait. greatest() keeps
-- last_ping_at from moving backwards.
-- +goose StatementBegin
CREATE FUNCTION heartbeat_ping(p_token_hash bytea)
RETURNS TABLE (tenant_id uuid, monitor_id uuid, recorded boolean)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- The heartbeats jobs has to look at: live ones with a ping the incident machine has not seen, or
-- whose deadline has passed and was not applied as missed yet. Read only: jobs runs it under its
-- singleton advisory lock and re-checks each row under a row lock in the tenant transaction,
-- where it also records what it applied, so a crash in between loses nothing. The grace is cast
-- through numeric as defence in depth (monitors_heartbeat_check already guarantees an integer).
-- +goose StatementBegin
CREATE FUNCTION list_due_heartbeats(n integer)
RETURNS TABLE (tenant_id uuid, monitor_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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
-- Status pages (internal/statuspages)
-- =============================================================================================

-- status_pages            the configuration of a page (one per organization by plan)
-- status_page_components  monitors and heartbeats of any project shown under a public name
-- status_page_snapshots   the prepared public view, built by the statuspages module (api on a
--                         configuration change, jobs every 30 s); the only thing the public
--                         statuspage process can read, through status_page_snapshot(slug)
-- status_page_released_slugs
--                         an address freed by a delete or a rename stays reserved for its
--                         organization for 30 days, so another organization cannot take over a
--                         link that visitors, feeds and embeds still use; the organization
--                         itself may reclaim it at any time

CREATE TABLE status_pages (
    id            uuid        PRIMARY KEY,
    tenant_id     uuid        NOT NULL REFERENCES tenants (id),
    -- Global and public: a slug is not a secret. Reserved words are refused here too, so a
    -- route of the gateway or the renderer can never be shadowed by a page.
    slug          text        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{2,62}$' AND slug NOT IN (
                      'admin', 'api', 'app', 'assets', 'auth', 'badge', 'dashboard', 'docs', 'embed',
                      'feed', 'health', 'healthz', 'help', 'login', 'logout', 'metrics', 'proreta',
                      'public', 'readyz', 'register', 'robots', 'root', 'rss', 'settings', 'signup',
                      'static', 'status', 'support', 'system', 'www')),
    title         text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 80),
    description   text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
    -- Shown with <img>, never fetched by the server.
    logo_url      text        CHECK (logo_url IS NULL OR (char_length(logo_url) <= 500 AND logo_url ~ '^https://[^\s/?#@]+')),
    accent_color  text        CHECK (accent_color IS NULL OR accent_color ~ '^#[0-9a-f]{6}$'),
    -- argon2id PHC string; NULL = no password.
    password_hash text        CHECK (password_hash IS NULL OR password_hash LIKE '$argon2id$%'),
    published     boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    UNIQUE (tenant_id, id),
    CONSTRAINT status_pages_deleted_unpublished CHECK (deleted_at IS NULL OR NOT published)
);
CREATE UNIQUE INDEX status_pages_slug_idx ON status_pages (slug) WHERE deleted_at IS NULL;
CREATE INDEX status_pages_tenant_idx ON status_pages (tenant_id) WHERE deleted_at IS NULL;
-- Soft delete only; jobs reads.
REVOKE DELETE ON status_pages FROM proreta_api;
REVOKE INSERT, UPDATE, DELETE ON status_pages FROM proreta_worker;

CREATE TABLE status_page_components (
    id           uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    page_id      uuid        NOT NULL,
    monitor_id   uuid        NOT NULL,
    public_name  text        NOT NULL CHECK (char_length(public_name) BETWEEN 1 AND 60),
    group_name   text        CHECK (group_name IS NULL OR char_length(group_name) BETWEEN 1 AND 60),
    position     integer     NOT NULL CHECK (position BETWEEN 0 AND 999),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id),
    UNIQUE (page_id, monitor_id),
    UNIQUE (page_id, position),
    FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages (tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors (tenant_id, id)
);
CREATE INDEX status_page_components_monitor_idx ON status_page_components (tenant_id, monitor_id);
-- jobs removes the components of deleted monitors and changes nothing else.
REVOKE INSERT, UPDATE ON status_page_components FROM proreta_worker;

-- The payload is the allow-listed projection built by internal/statuspages (Snapshot); the
-- golden test there fixes its keys.
CREATE TABLE status_page_snapshots (
    page_id      uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    slug         text        NOT NULL,
    payload      jsonb       NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    etag         text        NOT NULL CHECK (etag ~ '^[0-9a-f]{32}$'),
    generated_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE status_page_released_slugs (
    tenant_id    uuid        NOT NULL REFERENCES tenants (id),
    slug         text        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{2,62}$'),
    released_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, slug)
);
CREATE INDEX status_page_released_slugs_slug_idx ON status_page_released_slugs (slug, released_at);
REVOKE DELETE ON status_page_released_slugs FROM proreta_api;
REVOKE ALL ON status_page_released_slugs FROM proreta_worker;

ALTER TABLE status_pages ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_pages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON status_pages
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_statuspage_read ON status_pages FOR SELECT TO proreta_owner USING (true);

ALTER TABLE status_page_components ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_page_components FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON status_page_components
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE status_page_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_page_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON status_page_snapshots
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_statuspage_read ON status_page_snapshots FOR SELECT TO proreta_owner USING (true);

ALTER TABLE status_page_released_slugs ENABLE ROW LEVEL SECURITY;
ALTER TABLE status_page_released_slugs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON status_page_released_slugs
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_slug_hold_read ON status_page_released_slugs FOR SELECT TO proreta_owner USING (true);

-- jobs: the published pages of live, active tenants, to rebuild their snapshots in tenant
-- transactions of their own.
-- +goose StatementBegin
CREATE FUNCTION status_page_published_pages()
RETURNS TABLE (tenant_id uuid, page_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- statuspage: the prepared snapshot of a published page of a live, active tenant, with the
-- password hash when the page is protected. Nothing for an unknown, unpublished or deleted page,
-- a suspended or deleted tenant, or a snapshot left from an older slug: the renderer answers all
-- of them with the same 404.
-- +goose StatementBegin
CREATE FUNCTION status_page_snapshot(p_slug text)
RETURNS TABLE (payload jsonb, etag text, generated_at timestamptz, password_hash text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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
-- The role is created by `proretactl db bootstrap`, never by a migration. When the bootstrap has
-- not created it yet, it grants EXECUTE itself once it does.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'proreta_statuspage') THEN
        GRANT EXECUTE ON FUNCTION status_page_snapshot(text) TO proreta_statuspage;
    END IF;
END
$$;
-- +goose StatementEnd

-- Whether a slug is held for another organization than the transaction's (app.tenant_id):
-- released by it in the last 30 days. Answers only true or false, never whose it is.
-- +goose StatementBegin
CREATE FUNCTION status_page_slug_held(p_slug text)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- =============================================================================================
-- Invitations, organization delete and OAuth domains (internal/auth, internal/oauth)
-- =============================================================================================

-- An invitation into an existing tenant. The token is a bearer secret: only its SHA-256 is
-- stored, like session and heartbeat tokens. An invitation is open while accepted_at and
-- revoked_at are NULL and expires_at is in the future; rows are kept as history (no DELETE).
CREATE TABLE invitations (
    id           uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL REFERENCES tenants (id),
    role         text        NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    label        text        NOT NULL DEFAULT '' CHECK (length(label) <= 80),
    token_hash   bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_by   uuid        NOT NULL REFERENCES users (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    accepted_by  uuid        REFERENCES users (id),
    revoked_at   timestamptz,
    UNIQUE (tenant_id, id),
    CONSTRAINT invitations_accepted_pair CHECK ((accepted_at IS NULL) = (accepted_by IS NULL)),
    CONSTRAINT invitations_single_outcome CHECK (accepted_at IS NULL OR revoked_at IS NULL),
    CONSTRAINT invitations_expiry CHECK (expires_at > created_at)
);
CREATE INDEX invitations_tenant_created_idx ON invitations (tenant_id, created_at DESC);
REVOKE ALL ON invitations FROM proreta_worker;
REVOKE DELETE ON invitations FROM proreta_api;

-- A Google Workspace domain claimed by an organization: a verified Google account whose hd claim
-- equals the domain joins the organization with default_role on its first sign-in. A domain is
-- claimed by at most one organization in the whole installation (global UNIQUE).
CREATE TABLE org_auth_domains (
    id            uuid        PRIMARY KEY,
    tenant_id     uuid        NOT NULL REFERENCES tenants (id),
    domain        text        NOT NULL UNIQUE CHECK (
        length(domain) BETWEEN 3 AND 253
        AND domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'),
    default_role  text        NOT NULL DEFAULT 'viewer' CHECK (default_role IN ('admin', 'member', 'viewer')),
    enabled       boolean     NOT NULL DEFAULT true,
    created_by    uuid        NOT NULL REFERENCES users (id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id)
);
CREATE INDEX org_auth_domains_tenant_idx ON org_auth_domains (tenant_id, domain);
REVOKE ALL ON org_auth_domains FROM proreta_worker;

ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invitations
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- auth_invitation_lookup finds an invitation by token hash before the tenant is known.
CREATE POLICY owner_invitation_lookup ON invitations FOR SELECT TO proreta_owner USING (true);

ALTER TABLE org_auth_domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_auth_domains FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON org_auth_domains
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- auth_domain_lookup finds the organization of a domain before the tenant is known.
CREATE POLICY owner_domain_lookup ON org_auth_domains FOR SELECT TO proreta_owner USING (true);

-- The tenant and role of an open invitation, for an exact 32-byte token hash. Unknown, expired,
-- accepted and revoked tokens, and invitations of suspended or deleted tenants, all return no
-- row, so the caller can give one constant answer. The acceptance itself runs in a normal tenant
-- transaction once the tenant is known (row lock on the invitation).
-- +goose StatementBegin
CREATE FUNCTION auth_invitation_lookup(p_token_hash bytea)
RETURNS TABLE (
    invitation_id  uuid,
    tenant_id      uuid,
    tenant_name    text,
    role           text,
    expires_at     timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT i.id, i.tenant_id, t.name, i.role, i.expires_at
    FROM invitations AS i
    JOIN tenants AS t ON t.id = i.tenant_id
    WHERE i.token_hash = p_token_hash
      AND length(p_token_hash) = 32
      AND i.accepted_at IS NULL
      AND i.revoked_at IS NULL
      AND i.expires_at > now()
      AND t.status = 'active'
      AND t.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_invitation_lookup(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_invitation_lookup(bytea) TO proreta_api;

-- Owner-only soft delete of the caller's own organization (same effect as admin_delete_tenant:
-- deleted_at and status suspended). The api role cannot update tenants, so this is a definer
-- function. It acts only on the tenant of the caller's transaction (app.tenant_id), re-checks
-- that the actor is an active owner there, refuses the default tenant and audits
-- organization.deleted in the same transaction. Returns false when the tenant is already deleted.
-- Error codes (mapped in internal/auth): PRO01 the default tenant, PRO02 no tenant in the
-- transaction or the actor is not an active owner.
-- +goose StatementBegin
CREATE FUNCTION org_delete_tenant(p_actor uuid, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
    VALUES (p_audit_id, v_tenant, p_actor, 'organization.deleted', 'tenant', v_tenant::text, '{}'::jsonb, p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION org_delete_tenant(uuid, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION org_delete_tenant(uuid, uuid, inet) TO proreta_api;

-- The organization and default role of an enabled domain, for an exact lowercase domain. Domains
-- of suspended or deleted organizations return no row.
-- +goose StatementBegin
CREATE FUNCTION auth_domain_lookup(p_domain text)
RETURNS TABLE (
    tenant_id     uuid,
    default_role  text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- =============================================================================================
-- Plans and entitlements (internal/plans, internal/entitlements)
-- =============================================================================================

-- Plans are global (not tenant-scoped), like probe_locations: every tenant may read them, only
-- migrations and the owner change them. limits holds the quotas (max_monitors, max_heartbeats,
-- max_channels, max_members, max_projects, max_status_pages: non-negative integers, 2147483647 is
-- plans.Unlimited); tenants.limits overrides them key by key, and keys the application does not
-- know are ignored. features names the paid features a plan unlocks (internal/entitlements).
CREATE TABLE plans (
    key         text        PRIMARY KEY CHECK (key ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    limits      jsonb       NOT NULL CHECK (jsonb_typeof(limits) = 'object'),
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- Names are slugs: lowercase letters, digits, hyphen and underscore, 1-64 characters, no NULL.
    features    text[]      NOT NULL DEFAULT '{}'
        CONSTRAINT plans_features_names CHECK (
            array_position(features, NULL) IS NULL
            AND (cardinality(features) = 0 OR array_to_string(features, ',') ~ '^[a-z0-9_-]{1,64}(,[a-z0-9_-]{1,64})*$')
        )
);
REVOKE INSERT, UPDATE, DELETE ON plans FROM proreta_api, proreta_worker;

ALTER TABLE tenants ADD CONSTRAINT tenants_plan_key_fkey FOREIGN KEY (plan_key) REFERENCES plans (key);

-- =============================================================================================
-- Platform admin (internal/admin)
-- =============================================================================================

-- The admin panel works across tenants through the definer functions below. Each admin_*
-- function first checks that the actor (the user of the session, passed by the api with the same
-- trust as app.tenant_id) is an active platform admin, and audits into the target tenant.
-- Error codes (mapped to HTTP problems in internal/admin):
--   PRA01 the actor is not an active platform admin (403)
--   PRA02 tenant or user not found (404)
--   PRA03 unknown plan (422)
--   PRA04 invalid argument, for example a status other than active or suspended (422)
--   PRA05 the default tenant cannot be suspended or deleted (409)
--   PRA06 an admin cannot suspend or delete a tenant it belongs to (409)
--   PRA07 a platform admin's password is managed by its Secret, not the panel (409)
--   PRA08 the tenant is deleted (409)

-- Internal helpers of the admin definer functions. They run as the caller (proreta_owner inside a
-- definer function), are executable by nobody else (default privileges revoke PUBLIC) and are not
-- SECURITY DEFINER themselves, so the schema lint does not list them.

-- +goose StatementBegin
CREATE FUNCTION admin_require_platform_admin(p_actor uuid)
RETURNS void
LANGUAGE plpgsql
STABLE
SET search_path = public, pg_temp
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

-- Writes an audit row into the target tenant. audit_log forces RLS for its owner too, so the row
-- is inserted under that tenant's app.tenant_id, and the caller's value is restored afterwards
-- (like auth_revoke_user_sessions). Metadata never carries a password or a hash.
-- +goose StatementBegin
CREATE FUNCTION admin_audit(p_tenant_id uuid, p_actor uuid, p_audit_id uuid, p_action text,
                            p_entity_type text, p_entity_id text, p_metadata jsonb, p_ip inet)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION admin_audit(uuid, uuid, uuid, text, text, text, jsonb, inet) FROM PUBLIC;

-- Makes p_user_id the only platform admin: sets the flag for it and clears it for every other
-- user (a retired previous admin). Returns the number of users changed. BootstrapAdmin calls it
-- for the configured system admin; it refuses (PRA01) a user that is not an active owner of the
-- default tenant or that has a membership anywhere else, so even the api cannot hand the flag to
-- a registered user.
-- +goose StatementBegin
CREATE FUNCTION auth_sync_platform_admin(p_user_id uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    n integer;
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
    SET is_platform_admin = (u.id = p_user_id), updated_at = now()
    WHERE u.is_platform_admin IS DISTINCT FROM (u.id = p_user_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION auth_sync_platform_admin(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_sync_platform_admin(uuid) TO proreta_api;

-- Tenants with their plan and usage, newest first, with the number of matches in every row
-- (total_count). p_query matches the slug, the name, the id or the login of a member (substring,
-- case-insensitive). Deleted tenants only with p_include_deleted. Counts: memberships, live http
-- monitors and live heartbeats (paused ones count, like the plan quotas).
-- +goose StatementBegin
CREATE FUNCTION admin_list_tenants(p_actor uuid, p_query text, p_include_deleted boolean, p_limit integer, p_offset integer)
RETURNS TABLE (
    id              uuid,
    slug            text,
    name            text,
    plan_key        text,
    plan_name       text,
    status          text,
    created_at      timestamptz,
    deleted_at      timestamptz,
    user_count      integer,
    monitor_count   integer,
    heartbeat_count integer,
    total_count     integer
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
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type = 'http' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL),
           p.total::integer
    FROM page AS p
    JOIN plans AS pl ON pl.key = p.plan_key
    ORDER BY p.created_at DESC, p.id DESC;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_list_tenants(uuid, text, boolean, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_tenants(uuid, text, boolean, integer, integer) TO proreta_api;

-- One tenant (deleted ones included) with the same columns as the list; PRA02 when unknown.
-- +goose StatementBegin
CREATE FUNCTION admin_get_tenant(p_actor uuid, p_tenant_id uuid)
RETURNS TABLE (
    id              uuid,
    slug            text,
    name            text,
    plan_key        text,
    plan_name       text,
    status          text,
    created_at      timestamptz,
    deleted_at      timestamptz,
    user_count      integer,
    monitor_count   integer,
    heartbeat_count integer
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
    SELECT t.id, t.slug, t.name, t.plan_key, pl.name, t.status, t.created_at, t.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = t.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type = 'http' AND mo.deleted_at IS NULL),
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
REVOKE ALL ON FUNCTION admin_get_tenant(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_get_tenant(uuid, uuid) TO proreta_api;

-- Moves a live tenant to another plan. Returns false when it already is on it (nothing written).
-- Locks the tenant row like plans.CheckCreate, so a plan change and a quota check of the same
-- tenant are serialised. Existing data above the new limits is kept; only new creations fail.
-- +goose StatementBegin
CREATE FUNCTION admin_set_tenant_plan(p_actor uuid, p_tenant_id uuid, p_plan_key text, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id, 'admin.organization.plan_changed', 'tenant', p_tenant_id::text,
                        jsonb_build_object('from', v_old, 'to', p_plan_key), p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_set_tenant_plan(uuid, uuid, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_tenant_plan(uuid, uuid, text, uuid, inet) TO proreta_api;

-- Suspends ('suspended') or unsuspends ('active') a live tenant. Returns false when the status
-- does not change. The default tenant and a tenant the actor belongs to are never suspended.
-- Unsuspending expires the Slack deliveries that waited more than 15 minutes: they are marked
-- failed with last_error 'tenant_suspended' instead of alerting about the past all at once;
-- newer ones are still sent. The unsuspend audit row carries the number expired
-- (deliveries_expired).
-- +goose StatementBegin
CREATE FUNCTION admin_set_tenant_status(p_actor uuid, p_tenant_id uuid, p_status text, p_audit_id uuid, p_ip inet)
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
        v_meta := v_meta || jsonb_build_object('deliveries_expired', v_expired);
    END IF;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id,
                        CASE WHEN p_status = 'suspended' THEN 'admin.organization.suspended' ELSE 'admin.organization.unsuspended' END,
                        'tenant', p_tenant_id::text, v_meta, p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_set_tenant_status(uuid, uuid, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_set_tenant_status(uuid, uuid, text, uuid, inet) TO proreta_api;

-- Soft-deletes a tenant: deleted_at and status 'suspended'. Its data stays; its sessions are
-- refused by auth_lookup_session, its monitors, heartbeats and deliveries are no longer claimed.
-- Returns false for an already deleted tenant. Never the default tenant or one the actor belongs
-- to.
-- +goose StatementBegin
CREATE FUNCTION admin_delete_tenant(p_actor uuid, p_tenant_id uuid, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id, 'admin.organization.deleted', 'tenant', p_tenant_id::text,
                        jsonb_build_object('status', v_status, 'plan', v_plan), p_ip);
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_delete_tenant(uuid, uuid, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_delete_tenant(uuid, uuid, uuid, inet) TO proreta_api;

-- The members of a tenant (deleted tenants included), oldest membership first, with the number
-- of matches in every row. p_query matches the login (substring, case-insensitive).
-- +goose StatementBegin
CREATE FUNCTION admin_list_users(p_actor uuid, p_tenant_id uuid, p_query text, p_limit integer, p_offset integer)
RETURNS TABLE (
    id                uuid,
    login             text,
    status            text,
    created_at        timestamptz,
    is_platform_admin boolean,
    role              text,
    total_count       integer
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
REVOKE ALL ON FUNCTION admin_list_users(uuid, uuid, text, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_list_users(uuid, uuid, text, integer, integer) TO proreta_api;

-- Replaces a user's password hash (an argon2id PHC string computed by the api from a random
-- temporary password) and deletes all of the user's sessions in every tenant. Refuses platform
-- admins (PRA07: the system admin's credentials come from its Secret). The audit row goes to the
-- user's login tenant (auth_login_tenant, so both keep the same order); a user without any
-- membership is not reachable through the panel (PRA02). Returns that tenant and the number of
-- sessions deleted.
-- +goose StatementBegin
CREATE FUNCTION admin_reset_user_password(p_actor uuid, p_user_id uuid, p_password_hash text, p_audit_id uuid, p_ip inet)
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
    -- it creates its session.
    UPDATE users SET password_hash = p_password_hash, updated_at = now() WHERE id = p_user_id;
    v_n := auth_revoke_user_sessions(p_user_id);
    PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.user.password_reset', 'user', p_user_id::text,
                        jsonb_build_object('sessions_revoked', v_n), p_ip);
    RETURN QUERY SELECT v_tenant, v_n;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_reset_user_password(uuid, uuid, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_reset_user_password(uuid, uuid, text, uuid, inet) TO proreta_api;

-- Platform-wide counts as one JSON object: tenants by state (deleted ones only in "deleted"),
-- active users, live http monitors and heartbeats of tenants that are not deleted, and every plan
-- with its number of tenants that are not deleted.
-- +goose StatementBegin
CREATE FUNCTION admin_stats(p_actor uuid)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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
                     WHERE m.type = 'http' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
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

-- New tenants get free (tenants.plan_key default), the default tenant gets internal
-- (auth_create_tenant), and community is the self-hosted plan without quotas.
INSERT INTO plans (key, name, limits) VALUES
    ('free', 'Free', '{"max_monitors": 3, "max_heartbeats": 1, "max_channels": 1,
      "max_members": 3, "max_projects": 50, "max_status_pages": 1}'),
    ('internal', 'Internal', '{"max_monitors": 1000, "max_heartbeats": 100, "max_channels": 10,
      "max_members": 50, "max_projects": 50, "max_status_pages": 1}'),
    ('community', 'Community', '{"max_monitors": 2147483647, "max_heartbeats": 2147483647, "max_channels": 2147483647,
      "max_members": 2147483647, "max_projects": 2147483647, "max_status_pages": 2147483647}');

-- The first three monthly partitions of check_results. Last, so they are created with every
-- grant and policy of the parent in place.
SELECT ensure_partitions();
