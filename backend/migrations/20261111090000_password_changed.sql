-- +goose Up
SET LOCAL lock_timeout = '5s';

-- password_changed_at: when the user last changed the password in the app (POST
-- /api/v1/auth/password). NULL: never.
-- configured_password_hash: the hash of PRORETA_ADMIN_PASSWORD_HASH that BootstrapAdmin last saw
-- for this user. A password chosen in the app is kept across restarts while the configured hash
-- stays the same; a new configured hash (the documented reset) replaces it.
-- Both are nullable without a default (metadata-only changes), and the previous app version
-- neither reads nor writes them.
ALTER TABLE users ADD COLUMN password_changed_at timestamptz;
ALTER TABLE users ADD COLUMN configured_password_hash text;
GRANT UPDATE (password_changed_at, configured_password_hash) ON users TO proreta_api;
