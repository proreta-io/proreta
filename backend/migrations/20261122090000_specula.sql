-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Specula: an executor that runs inside a customer's network (private services without a public
-- address) and calls Proreta over HTTPS. It belongs to an organization and is shared with every
-- project or a list of projects. Each Specula owns one probe location of kind 'agent' (the binding to its
-- organization is the specula_agents row); monitors run there through monitor_locations like anywhere else.

CREATE TABLE specula_agents (
    tenant_id     uuid        NOT NULL REFERENCES tenants (id),
    id            uuid        NOT NULL,
    name          text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    -- SHA-256 of the token; the token is shown once and never stored.
    token_hash    bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    -- true: every project of the organization may use it; false: only specula_projects.
    all_projects  boolean     NOT NULL DEFAULT true,
    location_id   uuid        NOT NULL UNIQUE REFERENCES probe_locations (id),
    version       text        CHECK (length(version) <= 64),
    hostname      text        CHECK (length(hostname) <= 253),
    last_seen_at  timestamptz,
    revoked_at    timestamptz,
    created_by    uuid        REFERENCES users (id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (id)
);
CREATE UNIQUE INDEX specula_agents_name_idx ON specula_agents (tenant_id, lower(name)) WHERE revoked_at IS NULL;
-- The api creates through specula_create (which also writes the location), renames and changes
-- access with UPDATE, and revokes through specula_revoke; nothing is ever deleted.
REVOKE DELETE ON specula_agents FROM proreta_api;
REVOKE ALL ON specula_agents FROM proreta_worker;
REVOKE UPDATE ON specula_agents FROM proreta_api;
GRANT UPDATE (name, all_projects, updated_at) ON specula_agents TO proreta_api;

CREATE TABLE specula_projects (
    tenant_id   uuid        NOT NULL,
    specula_id  uuid        NOT NULL,
    project_id  uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (specula_id, project_id),
    FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents (tenant_id, id),
    FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id)
);
REVOKE UPDATE ON specula_projects FROM proreta_api;
REVOKE ALL ON specula_projects FROM proreta_worker;

ALTER TABLE specula_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE specula_agents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON specula_agents
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- Read and written by the definer functions below (authentication has no tenant yet).
CREATE POLICY owner_specula ON specula_agents FOR ALL TO proreta_owner USING (true) WITH CHECK (true);

ALTER TABLE specula_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE specula_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON specula_projects
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE POLICY owner_specula_read ON specula_projects FOR SELECT TO proreta_owner USING (true);

-- Creates a Specula and its location in the caller's tenant. The api cannot write probe_locations
-- itself. p_project_ids is only used when p_all_projects is false.
-- +goose StatementBegin
CREATE FUNCTION specula_create(p_id uuid, p_name text, p_token_hash bytea, p_all_projects boolean,
                               p_project_ids uuid[], p_actor uuid)
RETURNS void
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    loc           uuid := p_id;
    project       uuid;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'specula_create: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    INSERT INTO probe_locations (id, code, name, provider, region, kind)
    VALUES (loc, 'specula-' || replace(p_id::text, '-', ''), p_name, 'customer', 'private-network', 'agent');
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
REVOKE ALL ON FUNCTION specula_create(uuid, text, bytea, boolean, uuid[], uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_create(uuid, text, bytea, boolean, uuid[], uuid) TO proreta_api;

-- Revokes a Specula of the caller's tenant: the token stops working at once and its location
-- stops handing out jobs. Returns false when it does not exist or is already revoked.
-- +goose StatementBegin
CREATE FUNCTION specula_revoke(p_id uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- Keeps the location's name in step with a rename.
-- +goose StatementBegin
CREATE FUNCTION specula_agents_sync_location() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF NEW.name IS DISTINCT FROM OLD.name THEN
        UPDATE probe_locations SET name = NEW.name WHERE id = NEW.location_id;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER specula_agents_sync_location AFTER UPDATE OF name ON specula_agents
    FOR EACH ROW EXECUTE FUNCTION specula_agents_sync_location();

-- Authenticates a Specula token hash for the gateway (no tenant yet). Returns nothing for an
-- unknown or revoked token, a suspended or deleted organization, or a disabled location. Marks the
-- Specula as seen, at most every 10 seconds.
-- +goose StatementBegin
CREATE FUNCTION specula_authenticate(p_token_hash bytea)
RETURNS TABLE (specula_id uuid, tenant_id uuid, location_id uuid, location_code text)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- Records what a Specula reports about itself at start and in its heartbeat.
-- +goose StatementBegin
CREATE FUNCTION specula_hello(p_specula_id uuid, p_version text, p_hostname text)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH u AS (
        UPDATE specula_agents SET version = left(p_version, 64), hostname = left(p_hostname, 253), last_seen_at = now()
        WHERE id = p_specula_id AND revoked_at IS NULL
        RETURNING 1
    )
    SELECT EXISTS (SELECT 1 FROM u)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_hello(uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_hello(uuid, text, text) TO proreta_worker;

-- A location of kind 'agent' only hands out jobs of its own organization, while its Specula is
-- not revoked and the monitor's project may use it. Same body as before otherwise.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION claim_due_monitors(n integer, p_location text)
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
    next_run_at        timestamptz,
    method             text,
    request            text
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
          AND (l.kind = 'platform' OR EXISTS (
                SELECT 1 FROM specula_agents AS a
                WHERE a.location_id = l.id AND a.tenant_id = s.tenant_id AND a.revoked_at IS NULL
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
              m.config ->> 'url', COALESCE(m.config ->> 'expectedStatus', '2xx'),
              m.failure_threshold, m.interval_seconds, m.timeout_seconds, s.next_run_at,
              COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config ->> 'request', '')
$$;
-- +goose StatementEnd
