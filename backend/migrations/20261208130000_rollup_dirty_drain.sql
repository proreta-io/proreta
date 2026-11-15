-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Recomputes the hours that late results marked in rollup_dirty_hours, oldest first, at most
-- p_max per call, and clears each mark only if it is unchanged since it was read. A result
-- committed during the drain upserts its mark (a new seq), so the mark survives the delete and
-- the hour is recomputed by the next run; its row cannot be lost. The mark upsert and the delete
-- lock the same row, so a delete waits for an in-flight marker and then sees the new seq.
-- Serialised with compute_rollups_catch_up by the same advisory lock. Returns the hours
-- recomputed and the marks still left.
-- +goose StatementBegin
CREATE FUNCTION rollup_drain_dirty(p_max integer)
RETURNS TABLE (processed integer, remaining bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
SET TimeZone = 'UTC'
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

-- Same body as before, and a partition with a dirty hour is kept: its rollups are about to
-- change, so the count comparison below is not yet a proof.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION detach_expired_partition(p_name text)
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
$$;
-- +goose StatementEnd
