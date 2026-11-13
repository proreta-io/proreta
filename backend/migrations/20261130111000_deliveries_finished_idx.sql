-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';

-- The delivery purge picks the oldest finished rows; without this index every batch would sort
-- the whole table.
CREATE INDEX CONCURRENTLY IF NOT EXISTS notification_deliveries_finished_idx
    ON notification_deliveries (created_at) WHERE status IN ('sent', 'failed');
