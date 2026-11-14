-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Specula schedule mode: the executor runs its checks from a schedule the server hands out and
-- reports each result by slot. This migration holds everything the schedule and the slot
-- validation read: when a schedule was valid (epochs), which hours need their rollup recomputed
-- because late rows arrived (dirty hours), and what a Specula tells about itself.

-- What an executor says in its hello. capabilities decides the mode of its location: schedule/1
-- means the executor runs its own schedule (the claim hands it nothing), tcp that it can run tcp
-- checks. The latest hello wins, so a downgraded executor is back in pull mode at once.
ALTER TABLE specula_agents
    ADD COLUMN capabilities       text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(capabilities) <= 16),
    ADD COLUMN instance_id        text        CHECK (length(instance_id) <= 64),
    ADD COLUMN schedule_issued_at timestamptz,
    ADD COLUMN buffer_depth       integer     CHECK (buffer_depth >= 0),
    ADD COLUMN oldest_unsent_at   timestamptz,
    ADD COLUMN buffer_degraded    boolean     NOT NULL DEFAULT false;

-- One row per period in which a monitor had one schedule at a Specula location. A pause, a delete,
-- a move away or an edit that changes the check closes the open epoch; a resume or an edit opens a
-- new one. The slots of an epoch are anchor + n * interval_seconds (anchor in Unix seconds, chosen
-- by the server); a report is valid only for a slot inside [active_from, active_until). Written
-- only by the definer functions below, never deleted by a runtime role.
CREATE TABLE monitor_schedule_epochs (
    tenant_id         uuid        NOT NULL,
    monitor_id        uuid        NOT NULL,
    location_id       uuid        NOT NULL,
    epoch             integer     NOT NULL CHECK (epoch >= 1),
    anchor            bigint      NOT NULL CHECK (anchor > 0),
    interval_seconds  integer     NOT NULL CHECK (interval_seconds > 0),
    active_from       timestamptz NOT NULL,
    active_until      timestamptz,
    PRIMARY KEY (monitor_id, location_id, epoch),
    FOREIGN KEY (tenant_id, monitor_id, location_id) REFERENCES monitor_locations (tenant_id, monitor_id, location_id),
    CHECK (active_until IS NULL OR active_until >= active_from)
);
-- At most one open epoch per monitor and location, and the schedule of a location reads its open ones.
CREATE UNIQUE INDEX monitor_schedule_epochs_open_idx ON monitor_schedule_epochs (monitor_id, location_id) WHERE active_until IS NULL;
CREATE INDEX monitor_schedule_epochs_location_idx ON monitor_schedule_epochs (location_id) WHERE active_until IS NULL;
REVOKE ALL ON monitor_schedule_epochs FROM PUBLIC, proreta_api, proreta_worker;
-- The worker validates a report against the epoch inside its tenant transaction.
GRANT SELECT ON monitor_schedule_epochs TO proreta_worker;

ALTER TABLE monitor_schedule_epochs ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitor_schedule_epochs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON monitor_schedule_epochs
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- Written by the epoch functions and read by specula_schedule across tenants of one Specula.
CREATE POLICY owner_schedule_epochs ON monitor_schedule_epochs FOR ALL TO proreta_owner USING (true) WITH CHECK (true);

-- Closes the open epoch of a monitor at a location. Returns the number of epochs closed (0 or 1).
-- +goose StatementBegin
CREATE FUNCTION specula_epoch_close(p_tenant uuid, p_monitor uuid, p_location uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION specula_epoch_close(uuid, uuid, uuid) FROM PUBLIC;

-- Opens an epoch for a monitor that is to run at a Specula location now: an active http or tcp
-- monitor, a location of kind 'agent'. Returns the epoch number (the open one if there is one, so
-- a repeated call changes nothing), or NULL when the monitor does not qualify. The anchor is the
-- next whole second plus a phase of 0 .. interval-1 seconds taken from a hash of the monitor and
-- the location: stable, evenly spread, and a pure function of the row.
-- +goose StatementBegin
CREATE FUNCTION specula_epoch_open(p_tenant uuid, p_monitor uuid, p_location uuid)
RETURNS integer
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION specula_epoch_open(uuid, uuid, uuid) FROM PUBLIC;

-- Epoch maintenance. A monitor row change that stops or changes its checks closes its epochs
-- (pause, delete) or replaces them (a new target, request, type or interval); a resume opens them.
-- A state row appears when a monitor is placed at a location and disappears when it is moved
-- away. Triggers keep this in one place, whichever code path changes the row. They run as the
-- owner because the epochs are written by nobody else.
-- +goose StatementBegin
CREATE FUNCTION monitors_epoch_sync()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
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
CREATE TRIGGER monitors_epoch_sync AFTER UPDATE OF paused, deleted_at, config, type, interval_seconds, timeout_seconds ON monitors
    FOR EACH ROW
    WHEN (OLD.paused IS DISTINCT FROM NEW.paused OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at
          OR OLD.config IS DISTINCT FROM NEW.config OR OLD.type IS DISTINCT FROM NEW.type
          OR OLD.interval_seconds IS DISTINCT FROM NEW.interval_seconds
          OR OLD.timeout_seconds IS DISTINCT FROM NEW.timeout_seconds)
    EXECUTE FUNCTION monitors_epoch_sync();

-- +goose StatementBegin
CREATE FUNCTION monitor_location_state_epoch_sync()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
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
CREATE TRIGGER monitor_location_state_epoch_sync AFTER INSERT OR DELETE ON monitor_location_state
    FOR EACH ROW EXECUTE FUNCTION monitor_location_state_epoch_sync();

-- Monitors that already run at a Specula get their first epoch now.
SELECT specula_epoch_open(s.tenant_id, s.monitor_id, s.location_id) FROM monitor_location_state AS s;

-- The schedule of one Specula: the open epochs of its location with the check definition, under
-- the same conditions as the claim (active organization and location, monitor not paused or
-- deleted, the project may use this Specula). The request column is the monitor's encrypted
-- request, as in the claim. At most 5001 rows (the schedule cap plus one), so the caller can
-- refuse a larger schedule without reading it all.
-- +goose StatementBegin
CREATE FUNCTION specula_schedule(p_tenant uuid, p_specula uuid)
RETURNS TABLE (
    monitor_id        uuid,
    epoch             integer,
    anchor            bigint,
    interval_seconds  integer,
    timeout_seconds   integer,
    monitor_type      text,
    url               text,
    expected_status   text,
    method            text,
    request           text,
    host              text,
    port              integer
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION specula_schedule(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_schedule(uuid, uuid) TO proreta_worker;

-- Hours that late results touched after the regular rollup stopped looking at them (the lookback
-- is 3 hours; a replayed result can be a day old). Not tenant data, like rollup_state: one row per
-- hour for the whole system, no privilege for the runtime roles, RLS on without a policy. seq
-- changes at every mark, so whoever drains an hour deletes the row only if seq is still what it
-- read, and a mark made during the drain survives (now() is the start of a transaction, not its
-- commit, so a timestamp could not tell).
CREATE SEQUENCE rollup_dirty_seq;
REVOKE ALL ON SEQUENCE rollup_dirty_seq FROM PUBLIC, proreta_api, proreta_worker;
CREATE TABLE rollup_dirty_hours (
    hour       timestamptz PRIMARY KEY CHECK (hour = date_trunc('hour', hour, 'UTC')),
    seq        bigint      NOT NULL,
    marked_at  timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON rollup_dirty_hours FROM PUBLIC, proreta_api, proreta_worker;
ALTER TABLE rollup_dirty_hours ENABLE ROW LEVEL SECURITY;

-- Marks the hour of a late result for recomputation. Only a closed hour of the last 7 days (the
-- window of the catch-up) is marked; anything else is ignored. Returns whether a mark was written.
-- +goose StatementBegin
CREATE FUNCTION rollup_mark_dirty(p_hour timestamptz)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION rollup_mark_dirty(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION rollup_mark_dirty(timestamptz) TO proreta_worker;

-- The claim leaves a location in schedule mode alone (its executor runs its own schedule, and the
-- two modes never run a monitor twice) and hands a tcp monitor only to an executor that announced
-- tcp: an older one would report it as a failed http check. Same body otherwise.
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
    request            text,
    monitor_type       text,
    host               text,
    port               integer
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
