-- +goose Up
SET LOCAL lock_timeout = '5s';

-- What the latest hello said about the executor's clock (server time minus the executor's
-- corrected clock, milliseconds), when the latest hello was, and when a second instance id was last
-- seen saying hello within three minutes of the first. last_seen_at cannot serve for that: every
-- authenticated call moves it, a hello is the only call that carries the instance id.
ALTER TABLE specula_agents
    ADD COLUMN clock_offset_ms       bigint,
    ADD COLUMN last_hello_at         timestamptz,
    ADD COLUMN duplicate_instance_at timestamptz;

-- Whether an incident was opened or resolved by a result that reached Proreta late (a replayed slot).
ALTER TABLE incidents
    ADD COLUMN opened_late   boolean NOT NULL DEFAULT false,
    ADD COLUMN resolved_late boolean NOT NULL DEFAULT false;

-- specula_hello with the clock offset. It answers whether the Specula exists (found) and whether
-- this hello carried a different instance id than the stored one within three minutes of the stored
-- one's last hello (duplicate): a restart keeps its id and a replaced pod comes later. The nine
-- argument function stays for the previous application version during a rollout.
-- +goose StatementBegin
CREATE FUNCTION specula_hello(
    p_specula_id         uuid,
    p_version            text,
    p_hostname           text,
    p_capabilities       text[],
    p_instance_id        text,
    p_schedule_issued_at timestamptz,
    p_buffer_depth       integer,
    p_oldest_unsent_at   timestamptz,
    p_buffer_degraded    boolean,
    p_clock_offset_ms    bigint
)
RETURNS TABLE (found boolean, duplicate boolean)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
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
REVOKE ALL ON FUNCTION specula_hello(uuid, text, text, text[], text, timestamptz, integer, timestamptz, boolean, bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_hello(uuid, text, text, text[], text, timestamptz, integer, timestamptz, boolean, bigint) TO proreta_worker;
