-- +goose Up
SET LOCAL lock_timeout = '5s';

-- claim_deliveries as before, but one busy channel can no longer fill a whole claim: at most two
-- ready rows per channel are handed out, and a channel that already has a delivery in progress
-- is skipped (it sends one message per second, so more rows would only wait for it). The rows of
-- other tenants behind a hot channel are therefore reached in the first claim. The lease, the
-- fencing token, the per-subject ordering and the tenant check are unchanged.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION claim_deliveries(n integer)
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

-- "Is something of this channel in progress": the same index serves the per-channel lookup.
CREATE INDEX notification_deliveries_channel_open_idx ON notification_deliveries (channel_id)
    WHERE status = 'in_progress';
