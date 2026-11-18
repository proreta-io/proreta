-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The first organization of a self-hosted installation is on `internal` and needs one status domain
-- of its own; the operator should not have to move it to another plan first.
UPDATE plans
SET limits = limits || jsonb_build_object('max_status_page_domains', 2147483647)
WHERE key = 'internal' AND (limits ->> 'max_status_page_domains') = '0';
