-- +goose Up
SET LOCAL lock_timeout = '5s';

-- status_page_domains_due also returns the page of each domain: the verification job writes the
-- audit rows of a state change (metadata pageId) and has no table right to look it up. Nothing
-- calls the function yet outside the tests, so dropping and recreating it (the result type
-- changes) is safe. Body, locking and grants are as in 20261202100000.
DROP FUNCTION status_page_domains_due(integer);

-- +goose StatementBegin
CREATE FUNCTION status_page_domains_due(p_limit integer)
RETURNS TABLE (tenant_id uuid, id uuid, page_id uuid, host text, token text, state text, created_at timestamptz, last_proof_at timestamptz, check_failures integer)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    RETURN QUERY
    SELECT d.tenant_id, d.id, d.page_id, d.host, d.token, d.state, d.created_at, d.last_proof_at, d.check_failures
    FROM status_page_domains AS d
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.released_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
      AND (d.last_checked_at IS NULL OR d.last_checked_at <= now() - CASE
              WHEN d.state = 'pending' AND d.created_at > now() - interval '24 hours' THEN interval '10 minutes'
              WHEN d.state = 'pending' THEN interval '1 hour'
              ELSE interval '6 hours' END)
    ORDER BY d.last_checked_at NULLS FIRST, d.id
    LIMIT greatest(1, least(coalesce(p_limit, 1), 1000))
    FOR UPDATE OF d SKIP LOCKED;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION status_page_domains_due(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION status_page_domains_due(integer) TO proreta_worker;
