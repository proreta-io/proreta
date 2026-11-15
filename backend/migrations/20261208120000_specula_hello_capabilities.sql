-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The hello of an executor that says what it can do. Every field is replaced by this hello, so
-- the latest hello decides: an executor that stops announcing schedule/1 or tcp loses it at once.
-- The three-argument function stays for the previous application version during a rollout.
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
    p_buffer_degraded    boolean
)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH u AS (
        UPDATE specula_agents SET
            version = left(p_version, 64),
            hostname = left(p_hostname, 253),
            capabilities = coalesce(p_capabilities, '{}'),
            instance_id = left(p_instance_id, 64),
            schedule_issued_at = p_schedule_issued_at,
            buffer_depth = p_buffer_depth,
            oldest_unsent_at = p_oldest_unsent_at,
            buffer_degraded = coalesce(p_buffer_degraded, false),
            last_seen_at = now()
        WHERE id = p_specula_id AND revoked_at IS NULL
        RETURNING 1
    )
    SELECT EXISTS (SELECT 1 FROM u)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION specula_hello(uuid, text, text, text[], text, timestamptz, integer, timestamptz, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION specula_hello(uuid, text, text, text[], text, timestamptz, integer, timestamptz, boolean) TO proreta_worker;
