-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Reminders go only to channels that were told the incident is down: a channel created, selected
-- or given the down event while an incident is open no longer gets "still down" without the
-- first alert. Counted from the newest opened or reminder delivery to the channel (an opened
-- that failed does not count). Same function otherwise; the grants are unchanged.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_incident_reminders(p_limit integer)
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
