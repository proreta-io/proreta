-- +goose Up
SET LOCAL lock_timeout = '5s';

-- finish_delivery learns a fourth status, 'throttled': the destination answered 429, so the
-- attempt is not counted and the row goes back to pending at retry_at. Unbounded that would
-- retry forever, so a row older than one hour is failed instead (and that last try counts).
-- Everything else, the fencing token included, is unchanged.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION finish_delivery(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamptz)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending', 'throttled') THEN
        RAISE EXCEPTION 'finish_delivery: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status IN ('pending', 'throttled') AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_delivery: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE notification_deliveries AS d
    SET status = CASE
            WHEN p_status = 'throttled' THEN
                CASE WHEN d.created_at < now() - interval '1 hour' THEN 'failed' ELSE 'pending' END
            ELSE p_status
        END,
        attempts = d.attempts + CASE
            WHEN p_status = 'throttled' AND d.created_at >= now() - interval '1 hour' THEN 0
            ELSE 1
        END,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        sent_at = CASE WHEN p_status = 'sent' THEN now() END,
        next_attempt_at = CASE WHEN p_status IN ('pending', 'throttled') THEN p_retry_at ELSE d.next_attempt_at END
    WHERE d.id = p_id AND d.status = 'in_progress' AND d.claim_token = p_token;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd
