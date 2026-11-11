-- +goose Up
SET LOCAL lock_timeout = '5s';

-- The outgoing email settings of the installation, one row, edited by the platform admin in the
-- admin panel. Global like users and mail_outbox (no tenant_id, no RLS): nobody reaches the
-- table directly, every read and write goes through the definer functions below.
--
-- The SMTP password is sealed with the installation keyring (AES-256-GCM, key id kept in
-- password_key_id) and is returned by no function that the api can call. Only jobs, which
-- delivers the mail, reads the sealed value.
--
-- connection_version identifies the connection settings (host, port, tls, from, username and
-- password). It changes whenever one of them changes. tested_version is the connection_version
-- of the last successful test message, so "tested" means tested_version = connection_version and
-- a changed setting invalidates the test without any bookkeeping. The three event switches can
-- only be on while the settings are tested (the CHECK below), and are all off by default.
CREATE TABLE email_settings (
    id                      boolean     PRIMARY KEY DEFAULT true CHECK (id),
    host                    text        NOT NULL DEFAULT '' CHECK (length(host) <= 253),
    port                    integer     NOT NULL DEFAULT 587 CHECK (port BETWEEN 1 AND 65535),
    tls                     text        NOT NULL DEFAULT 'starttls' CHECK (tls IN ('starttls', 'tls', 'none')),
    from_header             text        NOT NULL DEFAULT '' CHECK (length(from_header) <= 320),
    username                text        NOT NULL DEFAULT '' CHECK (length(username) <= 256),
    password_sealed         bytea       NOT NULL DEFAULT ''::bytea CHECK (octet_length(password_sealed) <= 1024),
    password_key_id         text        NOT NULL DEFAULT '' CHECK (length(password_key_id) <= 64),
    connection_version      uuid        NOT NULL DEFAULT gen_random_uuid(),
    tested_version          uuid,
    tested_at               timestamptz,
    send_invitations        boolean     NOT NULL DEFAULT false,
    send_email_verification boolean     NOT NULL DEFAULT false,
    allow_password_reset    boolean     NOT NULL DEFAULT false,
    test_requested_at       timestamptz,
    test_finished_at        timestamptz,
    test_ok                 boolean,
    test_error              text        CHECK (length(test_error) <= 40),
    test_error_code         integer,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    updated_by              uuid,
    CHECK ((username = '') = (octet_length(password_sealed) = 0)),
    CHECK (username = '' OR tls <> 'none'),
    CHECK ((host = '') = (from_header = '')),
    CHECK ((tested_version IS NULL) = (tested_at IS NULL)),
    CHECK (host <> '' OR (tested_version IS NULL AND NOT (send_invitations OR send_email_verification OR allow_password_reset))),
    CHECK (NOT (send_invitations OR send_email_verification OR allow_password_reset) OR tested_version = connection_version)
);
REVOKE ALL ON email_settings FROM proreta_api, proreta_worker;
INSERT INTO email_settings (id) VALUES (true);

-- +goose StatementBegin
CREATE FUNCTION email_settings_test_state(s email_settings)
RETURNS text
LANGUAGE sql
STABLE
SET search_path = public, pg_temp
AS $$
    SELECT CASE
        WHEN s.test_requested_at IS NULL THEN 'none'
        WHEN s.test_finished_at IS NOT NULL AND s.test_ok THEN 'succeeded'
        WHEN s.test_finished_at IS NOT NULL THEN 'failed'
        WHEN s.test_requested_at > now() - interval '3 minutes' THEN 'pending'
        ELSE 'failed'
    END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_test_state(email_settings) FROM PUBLIC;

-- What the api needs to decide whether a flow may send email: a configuration that passed its
-- test, and the switches. No secret, no address.
-- +goose StatementBegin
CREATE FUNCTION email_settings_features()
RETURNS TABLE (
    enabled                 boolean,
    send_invitations        boolean,
    send_email_verification boolean,
    allow_password_reset    boolean
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version,
           s.send_invitations, s.send_email_verification, s.allow_password_reset
    FROM email_settings AS s
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_features() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION email_settings_features() TO proreta_api;

-- The connection settings with the sealed password, for the jobs process that delivers the
-- mail. No row when no server is configured.
-- +goose StatementBegin
CREATE FUNCTION email_settings_for_jobs()
RETURNS TABLE (
    host               text,
    port               integer,
    tls                text,
    from_header        text,
    username           text,
    password_sealed    bytea,
    connection_version uuid
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT s.host, s.port, s.tls, s.from_header, s.username, s.password_sealed, s.connection_version
    FROM email_settings AS s
    WHERE s.host <> ''
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_for_jobs() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION email_settings_for_jobs() TO proreta_worker;

-- Jobs records the outcome of a test message it sent with the connection p_version. The result
-- is dropped when the settings changed meanwhile (returns false). A success makes the settings
-- tested; a failure keeps an earlier success, so a passing outage does not switch the flows off.
-- p_error is an error category (never server text), p_code the SMTP reply code or 0.
-- +goose StatementBegin
CREATE FUNCTION email_settings_record_test(p_version uuid, p_ok boolean, p_error text, p_code integer)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
    UPDATE email_settings AS s
    SET test_finished_at = now(),
        test_ok = p_ok,
        test_error = CASE WHEN p_ok THEN NULL ELSE left(COALESCE(p_error, 'other'), 40) END,
        test_error_code = CASE WHEN p_ok OR COALESCE(p_code, 0) = 0 THEN NULL ELSE p_code END,
        tested_version = CASE WHEN p_ok THEN p_version ELSE s.tested_version END,
        tested_at = CASE WHEN p_ok THEN now() ELSE s.tested_at END
    WHERE s.connection_version = p_version AND s.host <> '' AND s.test_requested_at IS NOT NULL;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION email_settings_record_test(uuid, boolean, text, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION email_settings_record_test(uuid, boolean, text, integer) TO proreta_worker;

-- Error codes of the admin_email_* functions (mapped to HTTP problems in internal/mail):
--   PRA01 the actor is not an active platform admin (403)
--   PRA04 invalid argument (422)
--   PRA09 the password must be entered again: host, port or username changed (422)
--   PRA10 the switches need a successful test of the current settings (409)
--   PRA11 no server is configured (409)
--   PRA12 a test message is already on its way (409)
--   PRA13 a username needs a password (422)

-- The settings for the admin panel. The sealed password is never returned, only whether one is
-- stored.
-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_get(p_actor uuid)
RETURNS TABLE (
    host                    text,
    port                    integer,
    tls                     text,
    from_header             text,
    username                text,
    password_set            boolean,
    send_invitations        boolean,
    send_email_verification boolean,
    allow_password_reset    boolean,
    tested                  boolean,
    tested_at               timestamptz,
    test_state              text,
    test_requested_at       timestamptz,
    test_finished_at        timestamptz,
    test_error              text,
    test_error_code         integer,
    updated_at              timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    SELECT s.host, s.port, s.tls, s.from_header, s.username, octet_length(s.password_sealed) > 0,
           s.send_invitations, s.send_email_verification, s.allow_password_reset,
           s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version, s.tested_at,
           CASE WHEN s.host = '' THEN 'none' ELSE email_settings_test_state(s) END,
           s.test_requested_at, s.test_finished_at,
           CASE WHEN email_settings_test_state(s) = 'failed' AND s.test_finished_at IS NULL THEN 'not_delivered' ELSE s.test_error END,
           s.test_error_code, s.updated_at
    FROM email_settings AS s;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_get(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_get(uuid) TO proreta_api;

-- Replaces the connection settings. p_password_sealed NULL keeps the stored password; an empty
-- username removes the login and the password with it. Host, port or username changing while the
-- old password would be kept is refused (PRA09): the stored password never goes to a server the
-- admin did not type it for. Nothing changes (and nothing is audited) when every value is the
-- same. A change gives the settings a new connection_version, forgets the test and turns the
-- switches off. Returns the names of the changed settings and whether a switch was turned off.
-- The audit row goes to the admin's own organization.
-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_update(p_actor uuid, p_host text, p_port integer, p_tls text, p_from text,
                                            p_username text, p_password_sealed bytea, p_key_id text,
                                            p_audit_id uuid, p_ip inet)
RETURNS TABLE (
    changed           text[],
    switches_disabled boolean
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    s        email_settings%ROWTYPE;
    v_keep   boolean := p_password_sealed IS NULL;
    v_pw     bytea;
    v_key    text;
    v_changed text[] := ARRAY[]::text[];
    v_off    boolean;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF COALESCE(p_host, '') = '' OR COALESCE(p_from, '') = '' OR p_port IS NULL OR p_tls IS NULL
       OR p_tls NOT IN ('starttls', 'tls', 'none') OR p_port NOT BETWEEN 1 AND 65535 OR p_username IS NULL
       OR (p_tls = 'none' AND p_username <> '') THEN
        RAISE EXCEPTION 'admin: invalid email settings' USING ERRCODE = 'PRA04';
    END IF;
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF p_username = '' THEN
        v_pw := ''::bytea;
        v_key := '';
    ELSIF v_keep THEN
        IF octet_length(s.password_sealed) = 0 THEN
            RAISE EXCEPTION 'admin: a password is required with a username' USING ERRCODE = 'PRA13';
        END IF;
        IF p_host IS DISTINCT FROM s.host OR p_port IS DISTINCT FROM s.port OR p_username IS DISTINCT FROM s.username THEN
            RAISE EXCEPTION 'admin: enter the password again' USING ERRCODE = 'PRA09';
        END IF;
        v_pw := s.password_sealed;
        v_key := s.password_key_id;
    ELSE
        IF octet_length(p_password_sealed) = 0 THEN
            RAISE EXCEPTION 'admin: empty password' USING ERRCODE = 'PRA04';
        END IF;
        v_pw := p_password_sealed;
        v_key := COALESCE(p_key_id, '');
    END IF;

    IF p_host IS DISTINCT FROM s.host THEN v_changed := array_append(v_changed, 'host'::text); END IF;
    IF p_port IS DISTINCT FROM s.port THEN v_changed := array_append(v_changed, 'port'::text); END IF;
    IF p_tls IS DISTINCT FROM s.tls THEN v_changed := array_append(v_changed, 'tls'::text); END IF;
    IF p_from IS DISTINCT FROM s.from_header THEN v_changed := array_append(v_changed, 'from'::text); END IF;
    IF p_username IS DISTINCT FROM s.username THEN v_changed := array_append(v_changed, 'username'::text); END IF;
    IF (p_username <> '' AND NOT v_keep) OR (p_username = '' AND octet_length(s.password_sealed) > 0) THEN v_changed := array_append(v_changed, 'password'::text); END IF;
    IF cardinality(v_changed) = 0 THEN
        RETURN QUERY SELECT v_changed, false;
        RETURN;
    END IF;

    v_off := s.send_invitations OR s.send_email_verification OR s.allow_password_reset;
    UPDATE email_settings AS e
    SET host = p_host, port = p_port, tls = p_tls, from_header = p_from, username = p_username,
        password_sealed = v_pw, password_key_id = v_key,
        connection_version = gen_random_uuid(), tested_version = NULL, tested_at = NULL,
        send_invitations = false, send_email_verification = false, allow_password_reset = false,
        test_requested_at = NULL, test_finished_at = NULL, test_ok = NULL, test_error = NULL, test_error_code = NULL,
        updated_at = now(), updated_by = p_actor
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_settings_updated', 'email_settings', 'email',
                            jsonb_build_object('changed', to_jsonb(v_changed), 'switchesDisabled', v_off), p_ip);
    END IF;
    RETURN QUERY SELECT v_changed, v_off;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_update(uuid, text, integer, text, text, text, bytea, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_update(uuid, text, integer, text, text, text, bytea, text, uuid, inet) TO proreta_api;

-- Removes the server: every setting back to its default, the password erased, all switches off.
-- Returns whether anything was configured.
-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_clear(p_actor uuid, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    s        email_settings%ROWTYPE;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF s.host = '' THEN
        RETURN false;
    END IF;
    UPDATE email_settings AS e
    SET host = '', port = 587, tls = 'starttls', from_header = '', username = '',
        password_sealed = ''::bytea, password_key_id = '',
        connection_version = gen_random_uuid(), tested_version = NULL, tested_at = NULL,
        send_invitations = false, send_email_verification = false, allow_password_reset = false,
        test_requested_at = NULL, test_finished_at = NULL, test_ok = NULL, test_error = NULL, test_error_code = NULL,
        updated_at = now(), updated_by = p_actor
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_settings_updated', 'email_settings', 'email',
                            jsonb_build_object('changed', jsonb_build_array('removed'),
                                               'switchesDisabled', s.send_invitations OR s.send_email_verification OR s.allow_password_reset), p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_clear(uuid, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_clear(uuid, uuid, inet) TO proreta_api;

-- Sets the three switches. Turning one on needs a successful test of the current settings
-- (PRA10). Returns whether anything changed; only a change is audited.
-- +goose StatementBegin
CREATE FUNCTION admin_email_settings_set_features(p_actor uuid, p_invitations boolean, p_verification boolean,
                                                  p_reset boolean, p_audit_id uuid, p_ip inet)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    s        email_settings%ROWTYPE;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_invitations IS NULL OR p_verification IS NULL OR p_reset IS NULL THEN
        RAISE EXCEPTION 'admin: invalid switches' USING ERRCODE = 'PRA04';
    END IF;
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF (p_invitations OR p_verification OR p_reset) AND NOT (s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version) THEN
        RAISE EXCEPTION 'admin: the settings have not passed a test' USING ERRCODE = 'PRA10';
    END IF;
    IF s.send_invitations = p_invitations AND s.send_email_verification = p_verification AND s.allow_password_reset = p_reset THEN
        RETURN false;
    END IF;
    UPDATE email_settings AS e
    SET send_invitations = p_invitations, send_email_verification = p_verification, allow_password_reset = p_reset,
        updated_at = now(), updated_by = p_actor
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_settings_updated', 'email_settings', 'email',
                            jsonb_build_object('changed', jsonb_build_array('switches'), 'sendInvitations', p_invitations,
                                               'sendEmailVerification', p_verification, 'allowPasswordReset', p_reset), p_ip);
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_settings_set_features(uuid, boolean, boolean, boolean, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_settings_set_features(uuid, boolean, boolean, boolean, uuid, inet) TO proreta_api;

-- Marks a test message as requested and returns the connection_version it is for. The api queues
-- the message (mail_outbox, kind test) in the same transaction; jobs sends it with the settings it
-- reads then and records the outcome. Refused when no server is configured (PRA11) or a test
-- was requested less than three minutes ago and has no result yet (PRA12). p_to_domain is the
-- domain of the recipient, the only part of the address that is audited.
-- +goose StatementBegin
CREATE FUNCTION admin_email_test_request(p_actor uuid, p_to_domain text, p_audit_id uuid, p_ip inet)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    s        email_settings%ROWTYPE;
    v_tenant uuid;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT * INTO s FROM email_settings WHERE id FOR UPDATE;
    IF s.host = '' THEN
        RAISE EXCEPTION 'admin: no email server is configured' USING ERRCODE = 'PRA11';
    END IF;
    IF email_settings_test_state(s) = 'pending' THEN
        RAISE EXCEPTION 'admin: a test message is already on its way' USING ERRCODE = 'PRA12';
    END IF;
    UPDATE email_settings AS e
    SET test_requested_at = now(), test_finished_at = NULL, test_ok = NULL, test_error = NULL, test_error_code = NULL
    WHERE e.id;
    v_tenant := auth_login_tenant(p_actor);
    IF v_tenant IS NOT NULL THEN
        PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.email_test_sent', 'email_settings', 'email',
                            jsonb_build_object('toDomain', left(COALESCE(p_to_domain, ''), 253)), p_ip);
    END IF;
    RETURN s.connection_version;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION admin_email_test_request(uuid, text, uuid, inet) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION admin_email_test_request(uuid, text, uuid, inet) TO proreta_api;
