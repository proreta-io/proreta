-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The retention completeness check compares the row count of the partition with the sum of its
-- hourly rollups instead of grouping the whole partition by monitor, location and hour.

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
