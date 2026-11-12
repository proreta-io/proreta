-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A Specula that goes silent opens an incident of its own ("agent_offline") and tells the channels
-- that subscribed to the events agent_offline / agent_online; when it is heard again the incident
-- resolves and agent_online follows. Such an incident has no monitor_id (it belongs to the Specula)
-- and lists the monitors that run there in incident_monitors.

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_events_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_events_check
    CHECK (cardinality(events) BETWEEN 1 AND 5
           AND events <@ ARRAY['down', 'recovered', 'tls_expiring', 'agent_offline', 'agent_online']::text[]);

ALTER TABLE incidents ADD COLUMN specula_id uuid;
ALTER TABLE incidents ADD CONSTRAINT incidents_specula_fkey
    FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents (tenant_id, id);
ALTER TABLE incidents ADD CONSTRAINT incidents_one_owner_check CHECK (monitor_id IS NULL OR specula_id IS NULL);
CREATE UNIQUE INDEX incidents_one_open_per_specula_idx ON incidents (specula_id)
    WHERE resolved_at IS NULL AND specula_id IS NOT NULL;
ALTER TABLE incidents DROP CONSTRAINT incidents_resolve_reason_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_resolve_reason_check
    CHECK (resolve_reason IN ('recovered', 'paused', 'deleted', 'url_changed', 'location_changed', 'revoked'));

ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_kind_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_kind_check
    CHECK (kind IN ('opened', 'resolved', 'tls_expiring', 'reminder', 'agent_offline', 'agent_online'));
ALTER TABLE notification_deliveries ALTER COLUMN monitor_id DROP NOT NULL;
ALTER TABLE notification_deliveries ADD COLUMN specula_id uuid;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_specula_fkey
    FOREIGN KEY (tenant_id, specula_id) REFERENCES specula_agents (tenant_id, id);
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_subject_check
    CHECK ((kind IN ('agent_offline', 'agent_online')) = (specula_id IS NOT NULL)
           AND (specula_id IS NOT NULL) <> (monitor_id IS NOT NULL));
CREATE INDEX notification_deliveries_specula_open_idx ON notification_deliveries (specula_id, created_at, id)
    WHERE specula_id IS NOT NULL AND status IN ('pending', 'in_progress');

-- claim_deliveries as before, with specula_id returned and the per-subject ordering (a Specula's
-- "online" never overtakes its "offline" on the same channel) taken from monitor or Specula.
DROP FUNCTION claim_deliveries(integer);
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
    claim_token  uuid,
    specula_id   uuid
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
              WHERE o.monitor_id IS NOT DISTINCT FROM d.monitor_id
                AND o.specula_id IS NOT DISTINCT FROM d.specula_id
                AND o.channel_id = d.channel_id
                AND o.status IN ('pending', 'in_progress')
                AND (o.created_at, o.id) < (d.created_at, d.id)
          )
        ORDER BY d.created_at, d.id
        LIMIT least(greatest(n, 0), 100)
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

-- Opens the incident of every Specula that has been silent for longer than p_after and resolves
-- the incident of every one that is heard again (or was revoked, which says nothing about the link),
-- queueing the agent_offline / agent_online deliveries in the same transaction. A Specula that
-- never connected, or is revoked, is not reported. A channel receives an event when it subscribes
-- to it and is organization-wide or of a project the Specula is shared with. Returns how many
-- incidents were opened and resolved. Cross-tenant, run by the jobs process every minute.
-- +goose StatementBegin
CREATE FUNCTION specula_sync_offline(p_after interval DEFAULT interval '3 minutes')
RETURNS TABLE (opened integer, resolved integer)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- The Specula count per status for the gauge proreta_jobs_specula_agents, across tenants. A
-- Specula that never connected is pending.
-- +goose StatementBegin
CREATE FUNCTION specula_status_counts(p_after interval DEFAULT interval '3 minutes')
RETURNS TABLE (status text, n bigint)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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

-- A queued Specula delivery is sent only while its channel may still serve the Specula (same rule
-- as at queueing time); also gives the template the organization name. Tenant-scoped like
-- webhook_delivery_context. No row: the Specula is gone.
-- +goose StatementBegin
CREATE FUNCTION specula_delivery_context(p_specula_id uuid, p_channel_id uuid)
RETURNS TABLE (routed boolean, organization_name text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION specula_delivery_context(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_delivery_context(uuid, uuid) TO proreta_worker;

-- specula_sync_offline runs as the owner; the reads it needs on incidents, channels, projects and
-- monitors already have owner policies.
CREATE POLICY owner_specula_offline_incident_insert ON incidents FOR INSERT TO proreta_owner WITH CHECK (true);
CREATE POLICY owner_specula_offline_incident_resolve ON incidents FOR UPDATE TO proreta_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_specula_offline_incident_monitors ON incident_monitors FOR INSERT TO proreta_owner WITH CHECK (true);
CREATE POLICY owner_specula_offline_locations ON monitor_locations FOR SELECT TO proreta_owner USING (true);
