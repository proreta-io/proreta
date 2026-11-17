-- +goose Up
SET LOCAL lock_timeout = '5s';

-- A failed domain returns to verified only when its TXT record is seen. The CNAME target is the
-- same for every organization, so a CNAME alone proves nothing about who controls the name; a
-- stale CNAME must not give a host back to an organization that lost it. Only the body of
-- status_page_domain_record_check changes (same arguments and result, so the owner and the grants
-- stay); verified and active domains keep their behaviour.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION status_page_domain_record_check(p_tenant uuid, p_id uuid, p_txt text, p_cname text, p_error text)
RETURNS TABLE (state text, events text[])
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_ctx      text := NULLIF(current_setting('app.tenant_id', true), '');
    d          status_page_domains%ROWTYPE;
    v_state    text;
    v_proof    timestamptz;
    v_failures integer;
    v_verified timestamptz;
    v_released timestamptz;
    v_events   text[] := '{}';
    v_any      boolean := p_txt = 'present' OR p_cname = 'present';
BEGIN
    IF p_txt IS NULL OR p_cname IS NULL OR p_txt NOT IN ('present', 'missing', 'unknown') OR p_cname NOT IN ('present', 'missing', 'unknown')
       OR (p_error IS NOT NULL AND p_error NOT IN ('txt-missing', 'txt-mismatch', 'cname-missing', 'cname-wrong', 'dns-error')) THEN
        RAISE EXCEPTION 'status page domain: invalid check result' USING ERRCODE = 'PRD01';
    END IF;
    IF v_ctx IS NOT NULL AND v_ctx <> p_tenant::text THEN
        RAISE EXCEPTION 'status page domain: wrong tenant' USING ERRCODE = 'PRD02';
    END IF;
    SELECT * INTO d FROM status_page_domains AS x WHERE x.tenant_id = p_tenant AND x.id = p_id AND x.released_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'status page domain: not found' USING ERRCODE = 'PRD03';
    END IF;
    v_state := d.state;
    v_proof := d.last_proof_at;
    v_failures := d.check_failures;
    v_verified := d.verified_at;

    IF d.state = 'pending' THEN
        IF p_txt = 'present' THEN
            v_state := 'verified';
            v_proof := now();
            v_failures := 0;
            v_verified := coalesce(d.verified_at, now());
            v_events := ARRAY['verified'];
            IF p_cname = 'present' THEN
                v_state := 'active';
                v_events := array_append(v_events, 'activated');
            END IF;
        ELSIF p_txt = 'missing' AND now() - d.created_at >= interval '7 days' THEN
            v_released := now();
            v_events := ARRAY['expired'];
        END IF;
    ELSIF d.state IN ('verified', 'active') THEN
        IF v_any THEN
            v_proof := now();
            v_failures := 0;
            IF d.state = 'verified' AND p_cname = 'present' THEN
                v_state := 'active';
                v_events := ARRAY['activated'];
            END IF;
        ELSIF p_txt = 'missing' AND p_cname = 'missing' THEN
            v_failures := d.check_failures + 1;
            IF v_failures >= 3 AND now() - coalesce(d.last_proof_at, d.created_at) >= interval '7 days' THEN
                v_state := 'failed';
                v_events := ARRAY['failed'];
            END IF;
        END IF;
    ELSIF d.state = 'failed' AND p_txt = 'present' THEN
        v_state := 'verified';
        v_proof := now();
        v_failures := 0;
        v_events := ARRAY['recovered'];
    END IF;

    UPDATE status_page_domains AS x
    SET state = v_state, last_proof_at = v_proof, check_failures = v_failures, verified_at = v_verified,
        last_checked_at = now(), last_error = p_error, updated_at = now(),
        released_at = v_released, is_primary = x.is_primary AND v_released IS NULL
    WHERE x.tenant_id = p_tenant AND x.id = p_id;
    RETURN QUERY SELECT v_state, v_events;
END
$$;
-- +goose StatementEnd
