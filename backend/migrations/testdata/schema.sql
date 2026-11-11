-- Catalog snapshot of the migrated schema, compared by TestIntegrationSchemaSnapshot.
-- Regenerate: TEST_DATABASE_ADMIN_URL=... go test ./migrations -run TestIntegrationSchemaSnapshot -update

-- tables
audit_log kind=r owner=proreta_owner rls=true force=true acl=proreta_api=a/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=a/proreta_owner
auth_identities kind=r owner=proreta_owner rls=false force=false acl=proreta_api=ard/proreta_owner,proreta_owner=arwdDxt/proreta_owner
check_results kind=p owner=proreta_owner rls=true force=true partition by RANGE (checked_at) acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=ar/proreta_owner
email_settings kind=r owner=proreta_owner rls=false force=false acl=proreta_owner=arwdDxt/proreta_owner
email_verifications kind=r owner=proreta_owner rls=false force=false acl=proreta_api=ar/proreta_owner,proreta_owner=arwdDxt/proreta_owner
goose_db_version kind=r owner=proreta_owner rls=false force=false acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
goose_db_version_id_seq kind=S owner=proreta_owner rls=false force=false
heartbeat_tokens kind=r owner=proreta_owner rls=true force=true acl=proreta_api=ar/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
incident_monitors kind=r owner=proreta_owner rls=true force=true acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=ar/proreta_owner
incidents kind=r owner=proreta_owner rls=true force=true acl=proreta_api=rw/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=arw/proreta_owner
invitations kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arw/proreta_owner,proreta_owner=arwdDxt/proreta_owner
mail_outbox kind=r owner=proreta_owner rls=false force=false acl=proreta_api=a/proreta_owner,proreta_owner=arwdDxt/proreta_owner
memberships kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arwd/proreta_owner,proreta_owner=arwdDxt/proreta_owner
monitor_channels kind=r owner=proreta_owner rls=true force=true acl=proreta_api=ard/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
monitor_location_state kind=r owner=proreta_owner rls=true force=true options=fillfactor=70 acl=proreta_api=ar/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=rw/proreta_owner
monitor_locations kind=r owner=proreta_owner rls=true force=true acl=proreta_api=ar/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
monitor_state kind=r owner=proreta_owner rls=true force=true options=fillfactor=80 acl=proreta_api=ar/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=rw/proreta_owner
monitors kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arw/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
notification_channels kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arw/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
notification_deliveries kind=r owner=proreta_owner rls=true force=true acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=ar/proreta_owner
org_auth_domains kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arwd/proreta_owner,proreta_owner=arwdDxt/proreta_owner
password_resets kind=r owner=proreta_owner rls=false force=false acl=proreta_api=ar/proreta_owner,proreta_owner=arwdDxt/proreta_owner
plans kind=r owner=proreta_owner rls=false force=false acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
probe_locations kind=r owner=proreta_owner rls=false force=false acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
project_members kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arwd/proreta_owner,proreta_owner=arwdDxt/proreta_owner
projects kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arw/proreta_owner,proreta_owner=arwdDxt/proreta_owner
result_rollups_hourly kind=r owner=proreta_owner rls=true force=true acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
rollup_state kind=r owner=proreta_owner rls=true force=false acl=proreta_owner=arwdDxt/proreta_owner
sessions kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arwd/proreta_owner,proreta_owner=arwdDxt/proreta_owner
status_page_components kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arwd/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=rd/proreta_owner
status_page_released_slugs kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arw/proreta_owner,proreta_owner=arwdDxt/proreta_owner
status_page_snapshots kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arwd/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=arwd/proreta_owner
status_pages kind=r owner=proreta_owner rls=true force=true acl=proreta_api=arw/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
tenants kind=r owner=proreta_owner rls=false force=false acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner,proreta_worker=r/proreta_owner
users kind=r owner=proreta_owner rls=false force=false acl=proreta_api=r/proreta_owner,proreta_owner=arwdDxt/proreta_owner

-- columns
audit_log.1 id uuid not null
audit_log.2 tenant_id uuid not null
audit_log.3 actor_user_id uuid
audit_log.4 action text not null
audit_log.5 entity_type text not null
audit_log.6 entity_id text
audit_log.7 metadata jsonb not null default '{}'::jsonb
audit_log.8 ip inet
audit_log.9 created_at timestamp with time zone not null default now()
auth_identities.1 user_id uuid not null
auth_identities.2 provider text not null
auth_identities.3 subject text not null
auth_identities.4 created_at timestamp with time zone not null default now()
auth_identities.5 email citext acl=proreta_api=w/proreta_owner
auth_identities.6 hosted_domain text acl=proreta_api=w/proreta_owner
auth_identities.7 last_login_at timestamp with time zone acl=proreta_api=w/proreta_owner
check_results.1 tenant_id uuid not null
check_results.2 monitor_id uuid not null
check_results.3 location_id uuid not null
check_results.4 checked_at timestamp with time zone not null
check_results.5 ok boolean not null
check_results.6 status_code smallint
check_results.7 latency_ms integer
check_results.8 error_kind text
check_results.9 tls_not_after timestamp with time zone
email_settings.1 id boolean not null default true
email_settings.2 host text not null default ''::text
email_settings.3 port integer not null default 587
email_settings.4 tls text not null default 'starttls'::text
email_settings.5 from_header text not null default ''::text
email_settings.6 username text not null default ''::text
email_settings.7 password_sealed bytea not null default '\x'::bytea
email_settings.8 password_key_id text not null default ''::text
email_settings.9 connection_version uuid not null default gen_random_uuid()
email_settings.10 tested_version uuid
email_settings.11 tested_at timestamp with time zone
email_settings.12 send_invitations boolean not null default false
email_settings.13 send_email_verification boolean not null default false
email_settings.14 allow_password_reset boolean not null default false
email_settings.15 test_requested_at timestamp with time zone
email_settings.16 test_finished_at timestamp with time zone
email_settings.17 test_ok boolean
email_settings.18 test_error text
email_settings.19 test_error_code integer
email_settings.20 updated_at timestamp with time zone not null default now()
email_settings.21 updated_by uuid
email_verifications.1 token_hash bytea not null
email_verifications.2 user_id uuid not null
email_verifications.3 email citext not null
email_verifications.4 created_at timestamp with time zone not null default now()
email_verifications.5 expires_at timestamp with time zone not null
email_verifications.6 used_at timestamp with time zone acl=proreta_api=w/proreta_owner
goose_db_version.1 id integer not null identity d
goose_db_version.2 version_id bigint not null
goose_db_version.3 is_applied boolean not null
goose_db_version.4 tstamp timestamp without time zone not null default now()
heartbeat_tokens.1 tenant_id uuid not null
heartbeat_tokens.2 monitor_id uuid not null
heartbeat_tokens.3 token_hash bytea not null acl=proreta_api=w/proreta_owner
heartbeat_tokens.4 created_at timestamp with time zone not null default now() acl=proreta_api=w/proreta_owner
heartbeat_tokens.5 waiting_since timestamp with time zone not null default now() acl=proreta_api=w/proreta_owner
heartbeat_tokens.6 last_ping_at timestamp with time zone
heartbeat_tokens.7 applied_ping_at timestamp with time zone acl=proreta_api=w/proreta_owner,proreta_worker=w/proreta_owner
heartbeat_tokens.8 missed_deadline timestamp with time zone acl=proreta_worker=w/proreta_owner
incident_monitors.1 tenant_id uuid not null
incident_monitors.2 incident_id uuid not null
incident_monitors.3 monitor_id uuid not null
incidents.1 id uuid not null
incidents.2 tenant_id uuid not null
incidents.3 started_at timestamp with time zone not null
incidents.4 resolved_at timestamp with time zone
incidents.5 resolve_reason text
incidents.6 reason text
incidents.7 created_at timestamp with time zone not null default now()
incidents.8 monitor_id uuid
invitations.1 id uuid not null
invitations.2 tenant_id uuid not null
invitations.3 role text not null
invitations.4 label text not null default ''::text
invitations.5 token_hash bytea not null
invitations.6 created_by uuid not null
invitations.7 created_at timestamp with time zone not null default now()
invitations.8 expires_at timestamp with time zone not null
invitations.9 accepted_at timestamp with time zone
invitations.10 accepted_by uuid
invitations.11 revoked_at timestamp with time zone
mail_outbox.1 id uuid not null
mail_outbox.2 kind text not null
mail_outbox.3 payload bytea not null
mail_outbox.4 status text not null default 'pending'::text
mail_outbox.5 attempts integer not null default 0
mail_outbox.6 next_attempt_at timestamp with time zone not null default now()
mail_outbox.7 lease_until timestamp with time zone
mail_outbox.8 claim_token uuid
mail_outbox.9 last_error text
mail_outbox.10 created_at timestamp with time zone not null default now()
mail_outbox.11 expires_at timestamp with time zone not null
mail_outbox.12 finished_at timestamp with time zone
memberships.1 tenant_id uuid not null
memberships.2 user_id uuid not null
memberships.3 role text not null
memberships.4 created_at timestamp with time zone not null default now()
memberships.5 last_used_at timestamp with time zone
monitor_channels.1 tenant_id uuid not null
monitor_channels.2 monitor_id uuid not null
monitor_channels.3 channel_id uuid not null
monitor_channels.4 created_at timestamp with time zone not null default now()
monitor_location_state.1 tenant_id uuid not null
monitor_location_state.2 monitor_id uuid not null
monitor_location_state.3 location_id uuid not null
monitor_location_state.4 status text not null default 'unknown'::text
monitor_location_state.5 consecutive_failures integer not null default 0
monitor_location_state.6 next_run_at timestamp with time zone not null
monitor_location_state.7 lease_token uuid
monitor_location_state.8 lease_until timestamp with time zone
monitor_location_state.9 last_checked_at timestamp with time zone
monitor_locations.1 tenant_id uuid not null
monitor_locations.2 monitor_id uuid not null
monitor_locations.3 location_id uuid not null
monitor_locations.4 created_at timestamp with time zone not null default now()
monitor_state.1 tenant_id uuid not null
monitor_state.2 monitor_id uuid not null
monitor_state.3 status text not null default 'unknown'::text acl=proreta_api=w/proreta_owner
monitor_state.4 status_changed_at timestamp with time zone not null default now() acl=proreta_api=w/proreta_owner
monitor_state.5 last_error_kind text acl=proreta_api=w/proreta_owner
monitor_state.6 last_checked_at timestamp with time zone
monitor_state.7 tls_expires_at timestamp with time zone acl=proreta_api=w/proreta_owner
monitor_state.8 tls_warned_not_after timestamp with time zone acl=proreta_api=w/proreta_owner
monitor_state.9 current_incident_id uuid acl=proreta_api=w/proreta_owner
monitors.1 id uuid not null
monitors.2 tenant_id uuid not null
monitors.3 name text not null
monitors.4 type text not null default 'http'::text
monitors.5 config jsonb not null
monitors.6 interval_seconds integer not null default 60
monitors.7 timeout_seconds integer not null default 10
monitors.8 failure_threshold integer not null default 2
monitors.9 notifications_enabled boolean not null default true
monitors.10 paused boolean not null default false
monitors.11 created_at timestamp with time zone not null default now()
monitors.12 updated_at timestamp with time zone not null default now()
monitors.13 deleted_at timestamp with time zone
monitors.14 project_id uuid not null
notification_channels.1 id uuid not null
notification_channels.2 tenant_id uuid not null
notification_channels.3 type text not null
notification_channels.4 config_encrypted bytea
notification_channels.5 status text not null default 'active'::text acl=proreta_worker=w/proreta_owner
notification_channels.6 created_at timestamp with time zone not null default now()
notification_channels.7 updated_at timestamp with time zone not null default now() acl=proreta_worker=w/proreta_owner
notification_channels.8 name text not null default 'Slack'::text
notification_channels.9 events text[] not null default ARRAY['down'::text, 'recovered'::text, 'tls_expiring'::text]
notification_channels.10 mention text[] not null default '{}'::text[]
notification_channels.11 reminder_minutes integer not null default 0
notification_channels.12 deleted_at timestamp with time zone
notification_channels.13 project_id uuid
notification_deliveries.1 id uuid not null
notification_deliveries.2 tenant_id uuid not null
notification_deliveries.3 kind text not null
notification_deliveries.4 monitor_id uuid not null
notification_deliveries.5 incident_id uuid
notification_deliveries.6 channel_id uuid not null
notification_deliveries.7 payload jsonb not null
notification_deliveries.8 status text not null default 'pending'::text
notification_deliveries.9 attempts integer not null default 0
notification_deliveries.10 next_attempt_at timestamp with time zone not null default now()
notification_deliveries.11 lease_until timestamp with time zone
notification_deliveries.12 last_error text
notification_deliveries.13 created_at timestamp with time zone not null default now()
notification_deliveries.14 sent_at timestamp with time zone
notification_deliveries.15 claim_token uuid
org_auth_domains.1 id uuid not null
org_auth_domains.2 tenant_id uuid not null
org_auth_domains.3 domain text not null
org_auth_domains.4 default_role text not null default 'viewer'::text
org_auth_domains.5 enabled boolean not null default true
org_auth_domains.6 created_by uuid not null
org_auth_domains.7 created_at timestamp with time zone not null default now()
org_auth_domains.8 updated_at timestamp with time zone not null default now()
password_resets.1 token_hash bytea not null
password_resets.2 user_id uuid not null
password_resets.3 email citext not null
password_resets.4 created_at timestamp with time zone not null default now()
password_resets.5 expires_at timestamp with time zone not null
password_resets.6 used_at timestamp with time zone acl=proreta_api=w/proreta_owner
plans.1 key text not null
plans.2 name text not null
plans.3 limits jsonb not null
plans.4 created_at timestamp with time zone not null default now()
plans.5 features text[] not null default '{}'::text[]
probe_locations.1 id uuid not null
probe_locations.2 code text not null
probe_locations.3 name text not null
probe_locations.4 provider text not null
probe_locations.5 region text not null
probe_locations.6 kind text not null
probe_locations.7 status text not null default 'active'::text
probe_locations.8 created_at timestamp with time zone not null default now()
project_members.1 tenant_id uuid not null
project_members.2 project_id uuid not null
project_members.3 user_id uuid not null
project_members.4 role text
project_members.5 created_at timestamp with time zone not null default now()
project_members.6 updated_at timestamp with time zone not null default now()
projects.1 id uuid not null
projects.2 tenant_id uuid not null
projects.3 name text not null
projects.4 slug text not null
projects.5 is_default boolean not null default false
projects.6 all_members boolean not null default false
projects.7 created_at timestamp with time zone not null default now()
projects.8 updated_at timestamp with time zone not null default now()
projects.9 deleted_at timestamp with time zone
result_rollups_hourly.1 tenant_id uuid not null
result_rollups_hourly.2 monitor_id uuid not null
result_rollups_hourly.3 location_id uuid not null
result_rollups_hourly.4 hour timestamp with time zone not null
result_rollups_hourly.5 total integer not null
result_rollups_hourly.6 ok_count integer not null
result_rollups_hourly.7 latency_sum_ms bigint not null
result_rollups_hourly.8 latency_histogram integer[] not null
result_rollups_hourly.9 computed_at timestamp with time zone not null default now()
rollup_state.1 singleton boolean not null default true
rollup_state.2 watermark timestamp with time zone not null
rollup_state.3 updated_at timestamp with time zone not null default now()
sessions.1 id uuid not null
sessions.2 user_id uuid not null
sessions.3 tenant_id uuid not null
sessions.4 token_hash bytea not null
sessions.5 csrf_token text not null
sessions.6 created_at timestamp with time zone not null default now()
sessions.7 expires_at timestamp with time zone not null
sessions.8 last_seen_at timestamp with time zone not null default now()
sessions.9 ip inet
sessions.10 user_agent text
status_page_components.1 id uuid not null
status_page_components.2 tenant_id uuid not null
status_page_components.3 page_id uuid not null
status_page_components.4 monitor_id uuid not null
status_page_components.5 public_name text not null
status_page_components.6 group_name text
status_page_components.7 position integer not null
status_page_components.8 created_at timestamp with time zone not null default now()
status_page_components.9 updated_at timestamp with time zone not null default now()
status_page_released_slugs.1 tenant_id uuid not null
status_page_released_slugs.2 slug text not null
status_page_released_slugs.3 released_at timestamp with time zone not null default now()
status_page_snapshots.1 page_id uuid not null
status_page_snapshots.2 tenant_id uuid not null
status_page_snapshots.3 slug text not null
status_page_snapshots.4 payload jsonb not null
status_page_snapshots.5 etag text not null
status_page_snapshots.6 generated_at timestamp with time zone not null
status_pages.1 id uuid not null
status_pages.2 tenant_id uuid not null
status_pages.3 slug text not null
status_pages.4 title text not null
status_pages.5 description text not null default ''::text
status_pages.6 logo_url text
status_pages.7 accent_color text
status_pages.8 password_hash text
status_pages.9 published boolean not null default false
status_pages.10 created_at timestamp with time zone not null default now()
status_pages.11 updated_at timestamp with time zone not null default now()
status_pages.12 deleted_at timestamp with time zone
tenants.1 id uuid not null
tenants.2 slug text not null
tenants.3 name text not null
tenants.4 status text not null default 'active'::text
tenants.5 limits jsonb not null default '{}'::jsonb
tenants.6 created_at timestamp with time zone not null default now()
tenants.7 updated_at timestamp with time zone not null default now() acl=proreta_api=w/proreta_owner
tenants.8 deleted_at timestamp with time zone
tenants.9 plan_key text not null default 'free'::text
users.1 id uuid not null acl=proreta_api=a/proreta_owner
users.2 email citext not null acl=proreta_api=aw/proreta_owner
users.3 password_hash text acl=proreta_api=aw/proreta_owner
users.4 display_name text not null default ''::text acl=proreta_api=aw/proreta_owner
users.5 status text not null default 'active'::text acl=proreta_api=w/proreta_owner
users.6 created_at timestamp with time zone not null default now()
users.7 updated_at timestamp with time zone not null default now() acl=proreta_api=w/proreta_owner
users.8 is_platform_admin boolean not null default false
users.9 email_verified_at timestamp with time zone acl=proreta_api=aw/proreta_owner
users.10 password_changed_at timestamp with time zone acl=proreta_api=w/proreta_owner
users.11 configured_password_hash text acl=proreta_api=w/proreta_owner
users.12 password_must_change boolean not null default false acl=proreta_api=aw/proreta_owner

-- constraints
audit_log audit_log_action_check CHECK (((length(action) >= 1) AND (length(action) <= 100)))
audit_log audit_log_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES users(id)
audit_log audit_log_entity_type_check CHECK (((length(entity_type) >= 1) AND (length(entity_type) <= 100)))
audit_log audit_log_pkey PRIMARY KEY (id)
audit_log audit_log_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
auth_identities auth_identities_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320)))
auth_identities auth_identities_hosted_domain_check CHECK (((length(hosted_domain) >= 1) AND (length(hosted_domain) <= 253)))
auth_identities auth_identities_known_provider CHECK ((provider = ANY (ARRAY['google'::text, 'github'::text])))
auth_identities auth_identities_pkey PRIMARY KEY (provider, subject)
auth_identities auth_identities_provider_check CHECK (((length(provider) >= 1) AND (length(provider) <= 64)))
auth_identities auth_identities_subject_check CHECK (((length(subject) >= 1) AND (length(subject) <= 255)))
auth_identities auth_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
check_results check_results_check CHECK ((ok = (error_kind IS NULL)))
check_results check_results_error_kind_check CHECK (((length(error_kind) >= 1) AND (length(error_kind) <= 64)))
check_results check_results_latency_ms_check CHECK ((latency_ms >= 0))
check_results check_results_pkey PRIMARY KEY (monitor_id, checked_at, location_id)
check_results check_results_status_code_check CHECK (((status_code >= 100) AND (status_code <= 599)))
check_results check_results_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
email_settings email_settings_check CHECK (((username = ''::text) = (octet_length(password_sealed) = 0)))
email_settings email_settings_check1 CHECK (((username = ''::text) OR (tls <> 'none'::text)))
email_settings email_settings_check2 CHECK (((host = ''::text) = (from_header = ''::text)))
email_settings email_settings_check3 CHECK (((tested_version IS NULL) = (tested_at IS NULL)))
email_settings email_settings_check4 CHECK (((host <> ''::text) OR ((tested_version IS NULL) AND (NOT (send_invitations OR send_email_verification OR allow_password_reset)))))
email_settings email_settings_check5 CHECK (((NOT (send_invitations OR send_email_verification OR allow_password_reset)) OR (tested_version = connection_version)))
email_settings email_settings_from_header_check CHECK ((length(from_header) <= 320))
email_settings email_settings_host_check CHECK ((length(host) <= 253))
email_settings email_settings_id_check CHECK (id)
email_settings email_settings_password_key_id_check CHECK ((length(password_key_id) <= 64))
email_settings email_settings_password_sealed_check CHECK ((octet_length(password_sealed) <= 1024))
email_settings email_settings_pkey PRIMARY KEY (id)
email_settings email_settings_port_check CHECK (((port >= 1) AND (port <= 65535)))
email_settings email_settings_test_error_check CHECK ((length(test_error) <= 40))
email_settings email_settings_tls_check CHECK ((tls = ANY (ARRAY['starttls'::text, 'tls'::text, 'none'::text])))
email_settings email_settings_username_check CHECK ((length(username) <= 256))
email_verifications email_verifications_check CHECK ((expires_at > created_at))
email_verifications email_verifications_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320)))
email_verifications email_verifications_pkey PRIMARY KEY (token_hash)
email_verifications email_verifications_token_hash_check CHECK ((length(token_hash) = 32))
email_verifications email_verifications_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
goose_db_version goose_db_version_pkey PRIMARY KEY (id)
heartbeat_tokens heartbeat_tokens_pkey PRIMARY KEY (monitor_id)
heartbeat_tokens heartbeat_tokens_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
heartbeat_tokens heartbeat_tokens_tenant_id_monitor_id_key UNIQUE (tenant_id, monitor_id)
heartbeat_tokens heartbeat_tokens_token_hash_check CHECK ((length(token_hash) = 32))
heartbeat_tokens heartbeat_tokens_token_hash_key UNIQUE (token_hash)
incident_monitors incident_monitors_pkey PRIMARY KEY (incident_id, monitor_id)
incident_monitors incident_monitors_tenant_id_incident_id_fkey FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents(tenant_id, id)
incident_monitors incident_monitors_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
incidents incidents_check CHECK (((resolved_at IS NULL) = (resolve_reason IS NULL)))
incidents incidents_monitor_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
incidents incidents_pkey PRIMARY KEY (id)
incidents incidents_reason_check CHECK (((length(reason) >= 1) AND (length(reason) <= 64)))
incidents incidents_resolve_reason_check CHECK ((resolve_reason = ANY (ARRAY['recovered'::text, 'paused'::text, 'deleted'::text, 'url_changed'::text])))
incidents incidents_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
incidents incidents_tenant_id_id_key UNIQUE (tenant_id, id)
invitations invitations_accepted_by_fkey FOREIGN KEY (accepted_by) REFERENCES users(id)
invitations invitations_accepted_pair CHECK (((accepted_at IS NULL) = (accepted_by IS NULL)))
invitations invitations_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id)
invitations invitations_expiry CHECK ((expires_at > created_at))
invitations invitations_label_check CHECK ((length(label) <= 80))
invitations invitations_pkey PRIMARY KEY (id)
invitations invitations_role_check CHECK ((role = ANY (ARRAY['admin'::text, 'member'::text, 'viewer'::text])))
invitations invitations_single_outcome CHECK (((accepted_at IS NULL) OR (revoked_at IS NULL)))
invitations invitations_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
invitations invitations_tenant_id_id_key UNIQUE (tenant_id, id)
invitations invitations_token_hash_check CHECK ((length(token_hash) = 32))
invitations invitations_token_hash_key UNIQUE (token_hash)
mail_outbox mail_outbox_attempts_check CHECK ((attempts >= 0))
mail_outbox mail_outbox_check CHECK ((expires_at > created_at))
mail_outbox mail_outbox_check1 CHECK (((status = 'in_progress'::text) = (lease_until IS NOT NULL)))
mail_outbox mail_outbox_check2 CHECK (((status = 'in_progress'::text) = (claim_token IS NOT NULL)))
mail_outbox mail_outbox_check3 CHECK (((status = ANY (ARRAY['sent'::text, 'failed'::text])) = (finished_at IS NOT NULL)))
mail_outbox mail_outbox_check4 CHECK (((status = ANY (ARRAY['pending'::text, 'in_progress'::text])) = (octet_length(payload) > 0)))
mail_outbox mail_outbox_kind_check CHECK ((kind ~ '^[a-z][a-z_]{0,39}$'::text))
mail_outbox mail_outbox_last_error_check CHECK ((length(last_error) <= 500))
mail_outbox mail_outbox_payload_check CHECK ((octet_length(payload) <= 8192))
mail_outbox mail_outbox_pkey PRIMARY KEY (id)
mail_outbox mail_outbox_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'in_progress'::text, 'sent'::text, 'failed'::text])))
memberships memberships_pkey PRIMARY KEY (tenant_id, user_id)
memberships memberships_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'admin'::text, 'member'::text, 'viewer'::text])))
memberships memberships_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
memberships memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
monitor_channels monitor_channels_pkey PRIMARY KEY (monitor_id, channel_id)
monitor_channels monitor_channels_tenant_id_channel_id_fkey FOREIGN KEY (tenant_id, channel_id) REFERENCES notification_channels(tenant_id, id)
monitor_channels monitor_channels_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
monitor_location_state monitor_location_state_check CHECK (((lease_token IS NULL) = (lease_until IS NULL)))
monitor_location_state monitor_location_state_consecutive_failures_check CHECK ((consecutive_failures >= 0))
monitor_location_state monitor_location_state_pkey PRIMARY KEY (monitor_id, location_id)
monitor_location_state monitor_location_state_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'up'::text, 'down'::text])))
monitor_location_state monitor_location_state_tenant_id_monitor_id_location_id_fkey FOREIGN KEY (tenant_id, monitor_id, location_id) REFERENCES monitor_locations(tenant_id, monitor_id, location_id)
monitor_locations monitor_locations_location_id_fkey FOREIGN KEY (location_id) REFERENCES probe_locations(id)
monitor_locations monitor_locations_pkey PRIMARY KEY (monitor_id, location_id)
monitor_locations monitor_locations_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
monitor_locations monitor_locations_tenant_id_monitor_id_location_id_key UNIQUE (tenant_id, monitor_id, location_id)
monitor_state monitor_state_current_incident_fkey FOREIGN KEY (tenant_id, current_incident_id) REFERENCES incidents(tenant_id, id)
monitor_state monitor_state_last_error_kind_check CHECK (((length(last_error_kind) >= 1) AND (length(last_error_kind) <= 64)))
monitor_state monitor_state_pkey PRIMARY KEY (monitor_id)
monitor_state monitor_state_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'up'::text, 'down'::text])))
monitor_state monitor_state_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
monitors monitors_check CHECK (((timeout_seconds > 0) AND (timeout_seconds < interval_seconds)))
monitors monitors_config_check CHECK ((jsonb_typeof(config) = 'object'::text))
monitors monitors_failure_threshold_check CHECK (((failure_threshold >= 1) AND (failure_threshold <= 3)))
monitors monitors_heartbeat_check CHECK (((type <> 'heartbeat'::text) OR (((interval_seconds >= 60) AND (interval_seconds <= 86400)) AND (failure_threshold = 1) AND
CASE
    WHEN ((jsonb_typeof((config -> 'graceSeconds'::text)) = 'number'::text) AND ((config ->> 'graceSeconds'::text) ~ '^[0-9]+$'::text)) THEN ((((config ->> 'graceSeconds'::text))::numeric >= (0)::numeric) AND (((config ->> 'graceSeconds'::text))::numeric <= (3600)::numeric))
    ELSE false
END)))
monitors monitors_interval_seconds_check CHECK ((interval_seconds > 0))
monitors monitors_mvp_interval CHECK (((type <> 'http'::text) OR (interval_seconds = 60)))
monitors monitors_mvp_timeout CHECK (((type <> 'http'::text) OR (timeout_seconds = 10)))
monitors monitors_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 80)))
monitors monitors_pkey PRIMARY KEY (id)
monitors monitors_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
monitors monitors_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
monitors monitors_tenant_id_id_key UNIQUE (tenant_id, id)
monitors monitors_type_check CHECK ((type = ANY (ARRAY['http'::text, 'heartbeat'::text])))
notification_channels notification_channels_config_encrypted_check CHECK (((length(config_encrypted) >= 1) AND (length(config_encrypted) <= 4096)))
notification_channels notification_channels_config_present CHECK (((deleted_at IS NOT NULL) OR (config_encrypted IS NOT NULL)))
notification_channels notification_channels_events_check CHECK ((((cardinality(events) >= 1) AND (cardinality(events) <= 3)) AND (events <@ ARRAY['down'::text, 'recovered'::text, 'tls_expiring'::text])))
notification_channels notification_channels_mention_check CHECK (((cardinality(mention) <= 3) AND (array_to_string(mention, ' '::text) ~ '^((@here|@channel|<!subteam\^[A-Z0-9]{2,20}>|<@[UW][A-Z0-9]{2,20}>)( |$))*$'::text)))
notification_channels notification_channels_name_check CHECK (((length(name) >= 1) AND (length(name) <= 60)))
notification_channels notification_channels_pkey PRIMARY KEY (id)
notification_channels notification_channels_project_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
notification_channels notification_channels_reminder_minutes_check CHECK (((reminder_minutes = 0) OR ((reminder_minutes >= 5) AND (reminder_minutes <= 1440))))
notification_channels notification_channels_status_check CHECK ((status = ANY (ARRAY['active'::text, 'failed'::text])))
notification_channels notification_channels_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
notification_channels notification_channels_tenant_id_id_key UNIQUE (tenant_id, id)
notification_channels notification_channels_type_check CHECK ((type = 'slack_webhook'::text))
notification_deliveries notification_deliveries_attempts_check CHECK ((attempts >= 0))
notification_deliveries notification_deliveries_check CHECK (((status = 'in_progress'::text) = (lease_until IS NOT NULL)))
notification_deliveries notification_deliveries_check1 CHECK (((status = 'sent'::text) = (sent_at IS NOT NULL)))
notification_deliveries notification_deliveries_check2 CHECK (((kind = 'tls_expiring'::text) OR (incident_id IS NOT NULL)))
notification_deliveries notification_deliveries_claim_token_check CHECK (((status = 'in_progress'::text) = (claim_token IS NOT NULL)))
notification_deliveries notification_deliveries_kind_check CHECK ((kind = ANY (ARRAY['opened'::text, 'resolved'::text, 'tls_expiring'::text, 'reminder'::text])))
notification_deliveries notification_deliveries_last_error_check CHECK ((length(last_error) <= 500))
notification_deliveries notification_deliveries_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text))
notification_deliveries notification_deliveries_pkey PRIMARY KEY (id)
notification_deliveries notification_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'in_progress'::text, 'sent'::text, 'failed'::text])))
notification_deliveries notification_deliveries_tenant_id_channel_id_fkey FOREIGN KEY (tenant_id, channel_id) REFERENCES notification_channels(tenant_id, id)
notification_deliveries notification_deliveries_tenant_id_incident_id_fkey FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents(tenant_id, id)
notification_deliveries notification_deliveries_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
org_auth_domains org_auth_domains_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id)
org_auth_domains org_auth_domains_default_role_check CHECK ((default_role = ANY (ARRAY['admin'::text, 'member'::text, 'viewer'::text])))
org_auth_domains org_auth_domains_domain_check CHECK ((((length(domain) >= 3) AND (length(domain) <= 253)) AND (domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$'::text)))
org_auth_domains org_auth_domains_domain_key UNIQUE (domain)
org_auth_domains org_auth_domains_pkey PRIMARY KEY (id)
org_auth_domains org_auth_domains_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
org_auth_domains org_auth_domains_tenant_id_id_key UNIQUE (tenant_id, id)
password_resets password_resets_check CHECK ((expires_at > created_at))
password_resets password_resets_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320)))
password_resets password_resets_pkey PRIMARY KEY (token_hash)
password_resets password_resets_token_hash_check CHECK ((length(token_hash) = 32))
password_resets password_resets_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
plans plans_features_names CHECK (((array_position(features, NULL::text) IS NULL) AND ((cardinality(features) = 0) OR (array_to_string(features, ','::text) ~ '^[a-z0-9_-]{1,64}(,[a-z0-9_-]{1,64})*$'::text))))
plans plans_key_check CHECK ((key ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text))
plans plans_limits_check CHECK ((jsonb_typeof(limits) = 'object'::text))
plans plans_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200)))
plans plans_pkey PRIMARY KEY (key)
probe_locations probe_locations_code_check CHECK ((code ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text))
probe_locations probe_locations_code_key UNIQUE (code)
probe_locations probe_locations_kind_check CHECK ((kind = ANY (ARRAY['platform'::text, 'agent'::text])))
probe_locations probe_locations_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200)))
probe_locations probe_locations_pkey PRIMARY KEY (id)
probe_locations probe_locations_provider_check CHECK (((length(provider) >= 1) AND (length(provider) <= 100)))
probe_locations probe_locations_region_check CHECK (((length(region) >= 1) AND (length(region) <= 100)))
probe_locations probe_locations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
project_members project_members_pkey PRIMARY KEY (tenant_id, project_id, user_id)
project_members project_members_role_check CHECK ((role = ANY (ARRAY['member'::text, 'viewer'::text])))
project_members project_members_tenant_id_project_id_fkey FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
project_members project_members_tenant_id_user_id_fkey FOREIGN KEY (tenant_id, user_id) REFERENCES memberships(tenant_id, user_id) ON DELETE CASCADE
projects projects_default_live CHECK (((NOT is_default) OR (deleted_at IS NULL)))
projects projects_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 60)))
projects projects_pkey PRIMARY KEY (id)
projects projects_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text))
projects projects_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
projects projects_tenant_id_id_key UNIQUE (tenant_id, id)
result_rollups_hourly result_rollups_hourly_check CHECK (((ok_count >= 0) AND (ok_count <= total)))
result_rollups_hourly result_rollups_hourly_hour_check CHECK ((hour = date_trunc('hour'::text, hour, 'UTC'::text)))
result_rollups_hourly result_rollups_hourly_latency_histogram_check CHECK ((cardinality(latency_histogram) = 11))
result_rollups_hourly result_rollups_hourly_latency_sum_ms_check CHECK ((latency_sum_ms >= 0))
result_rollups_hourly result_rollups_hourly_pkey PRIMARY KEY (monitor_id, location_id, hour)
result_rollups_hourly result_rollups_hourly_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
result_rollups_hourly result_rollups_hourly_total_check CHECK ((total > 0))
rollup_state rollup_state_pkey PRIMARY KEY (singleton)
rollup_state rollup_state_singleton_check CHECK (singleton)
rollup_state rollup_state_watermark_check CHECK ((watermark = date_trunc('hour'::text, watermark, 'UTC'::text)))
sessions sessions_pkey PRIMARY KEY (id)
sessions sessions_tenant_id_user_id_fkey FOREIGN KEY (tenant_id, user_id) REFERENCES memberships(tenant_id, user_id) ON DELETE CASCADE
sessions sessions_token_hash_check CHECK ((length(token_hash) = 32))
sessions sessions_token_hash_key UNIQUE (token_hash)
sessions sessions_user_agent_check CHECK ((length(user_agent) <= 512))
status_page_components status_page_components_group_name_check CHECK (((group_name IS NULL) OR ((char_length(group_name) >= 1) AND (char_length(group_name) <= 60))))
status_page_components status_page_components_page_id_monitor_id_key UNIQUE (page_id, monitor_id)
status_page_components status_page_components_page_id_position_key UNIQUE (page_id, "position")
status_page_components status_page_components_pkey PRIMARY KEY (id)
status_page_components status_page_components_position_check CHECK ((("position" >= 0) AND ("position" <= 999)))
status_page_components status_page_components_public_name_check CHECK (((char_length(public_name) >= 1) AND (char_length(public_name) <= 60)))
status_page_components status_page_components_tenant_id_id_key UNIQUE (tenant_id, id)
status_page_components status_page_components_tenant_id_monitor_id_fkey FOREIGN KEY (tenant_id, monitor_id) REFERENCES monitors(tenant_id, id)
status_page_components status_page_components_tenant_id_page_id_fkey FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages(tenant_id, id) ON DELETE CASCADE
status_page_released_slugs status_page_released_slugs_pkey PRIMARY KEY (tenant_id, slug)
status_page_released_slugs status_page_released_slugs_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{2,62}$'::text))
status_page_released_slugs status_page_released_slugs_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
status_page_snapshots status_page_snapshots_etag_check CHECK ((etag ~ '^[0-9a-f]{32}$'::text))
status_page_snapshots status_page_snapshots_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text))
status_page_snapshots status_page_snapshots_pkey PRIMARY KEY (page_id)
status_page_snapshots status_page_snapshots_tenant_id_page_id_fkey FOREIGN KEY (tenant_id, page_id) REFERENCES status_pages(tenant_id, id) ON DELETE CASCADE
status_pages status_pages_accent_color_check CHECK (((accent_color IS NULL) OR (accent_color ~ '^#[0-9a-f]{6}$'::text)))
status_pages status_pages_deleted_unpublished CHECK (((deleted_at IS NULL) OR (NOT published)))
status_pages status_pages_description_check CHECK ((char_length(description) <= 500))
status_pages status_pages_logo_url_check CHECK (((logo_url IS NULL) OR ((char_length(logo_url) <= 500) AND (logo_url ~ '^https://[^\s/?#@]+'::text))))
status_pages status_pages_password_hash_check CHECK (((password_hash IS NULL) OR (password_hash ~~ '$argon2id$%'::text)))
status_pages status_pages_pkey PRIMARY KEY (id)
status_pages status_pages_slug_check CHECK (((slug ~ '^[a-z0-9][a-z0-9-]{2,62}$'::text) AND (slug <> ALL (ARRAY['admin'::text, 'api'::text, 'app'::text, 'assets'::text, 'auth'::text, 'badge'::text, 'dashboard'::text, 'docs'::text, 'embed'::text, 'feed'::text, 'health'::text, 'healthz'::text, 'help'::text, 'login'::text, 'logout'::text, 'metrics'::text, 'proreta'::text, 'public'::text, 'readyz'::text, 'register'::text, 'robots'::text, 'root'::text, 'rss'::text, 'settings'::text, 'signup'::text, 'static'::text, 'status'::text, 'support'::text, 'system'::text, 'www'::text]))))
status_pages status_pages_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id)
status_pages status_pages_tenant_id_id_key UNIQUE (tenant_id, id)
status_pages status_pages_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 80)))
tenants tenants_limits_is_object CHECK ((jsonb_typeof(limits) = 'object'::text))
tenants tenants_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200)))
tenants tenants_pkey PRIMARY KEY (id)
tenants tenants_plan_key_fkey FOREIGN KEY (plan_key) REFERENCES plans(key)
tenants tenants_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text))
tenants tenants_slug_key UNIQUE (slug)
tenants tenants_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text])))
users users_display_name_check CHECK ((length(display_name) <= 200))
users users_email_check CHECK (((length((email)::text) >= 3) AND (length((email)::text) <= 320)))
users users_email_key UNIQUE (email)
users users_pkey PRIMARY KEY (id)
users users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))

-- indexes
CREATE UNIQUE INDEX audit_log_pkey ON public.audit_log USING btree (id)
CREATE INDEX audit_log_tenant_created_idx ON public.audit_log USING btree (tenant_id, created_at DESC)
CREATE UNIQUE INDEX auth_identities_pkey ON public.auth_identities USING btree (provider, subject)
CREATE INDEX auth_identities_user_id_idx ON public.auth_identities USING btree (user_id)
CREATE UNIQUE INDEX auth_identities_user_provider_key ON public.auth_identities USING btree (user_id, provider)
CREATE INDEX check_results_checked_at_brin ON ONLY public.check_results USING brin (checked_at)
CREATE UNIQUE INDEX check_results_pkey ON ONLY public.check_results USING btree (monitor_id, checked_at, location_id)
CREATE UNIQUE INDEX email_settings_pkey ON public.email_settings USING btree (id)
CREATE INDEX email_verifications_expires_idx ON public.email_verifications USING btree (expires_at)
CREATE UNIQUE INDEX email_verifications_pkey ON public.email_verifications USING btree (token_hash)
CREATE INDEX email_verifications_user_idx ON public.email_verifications USING btree (user_id, created_at DESC)
CREATE UNIQUE INDEX goose_db_version_pkey ON public.goose_db_version USING btree (id)
CREATE UNIQUE INDEX heartbeat_tokens_pkey ON public.heartbeat_tokens USING btree (monitor_id)
CREATE UNIQUE INDEX heartbeat_tokens_tenant_id_monitor_id_key ON public.heartbeat_tokens USING btree (tenant_id, monitor_id)
CREATE UNIQUE INDEX heartbeat_tokens_token_hash_key ON public.heartbeat_tokens USING btree (token_hash)
CREATE INDEX incident_monitors_monitor_idx ON public.incident_monitors USING btree (tenant_id, monitor_id)
CREATE UNIQUE INDEX incident_monitors_pkey ON public.incident_monitors USING btree (incident_id, monitor_id)
CREATE UNIQUE INDEX incidents_one_open_per_monitor_idx ON public.incidents USING btree (monitor_id) WHERE ((resolved_at IS NULL) AND (monitor_id IS NOT NULL))
CREATE UNIQUE INDEX incidents_pkey ON public.incidents USING btree (id)
CREATE UNIQUE INDEX incidents_tenant_id_id_key ON public.incidents USING btree (tenant_id, id)
CREATE INDEX incidents_tenant_started_idx ON public.incidents USING btree (tenant_id, started_at DESC)
CREATE UNIQUE INDEX invitations_pkey ON public.invitations USING btree (id)
CREATE INDEX invitations_tenant_created_idx ON public.invitations USING btree (tenant_id, created_at DESC)
CREATE UNIQUE INDEX invitations_tenant_id_id_key ON public.invitations USING btree (tenant_id, id)
CREATE UNIQUE INDEX invitations_token_hash_key ON public.invitations USING btree (token_hash)
CREATE INDEX mail_outbox_finished_idx ON public.mail_outbox USING btree (finished_at) WHERE (finished_at IS NOT NULL)
CREATE INDEX mail_outbox_lease_idx ON public.mail_outbox USING btree (lease_until) WHERE (status = 'in_progress'::text)
CREATE UNIQUE INDEX mail_outbox_pkey ON public.mail_outbox USING btree (id)
CREATE INDEX mail_outbox_ready_idx ON public.mail_outbox USING btree (next_attempt_at) WHERE (status = 'pending'::text)
CREATE UNIQUE INDEX memberships_pkey ON public.memberships USING btree (tenant_id, user_id)
CREATE INDEX memberships_user_id_idx ON public.memberships USING btree (user_id)
CREATE INDEX monitor_channels_channel_idx ON public.monitor_channels USING btree (tenant_id, channel_id)
CREATE UNIQUE INDEX monitor_channels_pkey ON public.monitor_channels USING btree (monitor_id, channel_id)
CREATE INDEX monitor_location_state_due_idx ON public.monitor_location_state USING btree (next_run_at) WHERE (lease_token IS NULL)
CREATE INDEX monitor_location_state_lease_idx ON public.monitor_location_state USING btree (lease_until) WHERE (lease_token IS NOT NULL)
CREATE UNIQUE INDEX monitor_location_state_pkey ON public.monitor_location_state USING btree (monitor_id, location_id)
CREATE INDEX monitor_locations_location_idx ON public.monitor_locations USING btree (location_id)
CREATE UNIQUE INDEX monitor_locations_pkey ON public.monitor_locations USING btree (monitor_id, location_id)
CREATE UNIQUE INDEX monitor_locations_tenant_id_monitor_id_location_id_key ON public.monitor_locations USING btree (tenant_id, monitor_id, location_id)
CREATE UNIQUE INDEX monitor_state_pkey ON public.monitor_state USING btree (monitor_id)
CREATE UNIQUE INDEX monitors_pkey ON public.monitors USING btree (id)
CREATE INDEX monitors_project_idx ON public.monitors USING btree (tenant_id, project_id) WHERE (deleted_at IS NULL)
CREATE UNIQUE INDEX monitors_tenant_id_id_key ON public.monitors USING btree (tenant_id, id)
CREATE INDEX monitors_tenant_live_idx ON public.monitors USING btree (tenant_id, created_at) WHERE (deleted_at IS NULL)
CREATE UNIQUE INDEX notification_channels_pkey ON public.notification_channels USING btree (id)
CREATE INDEX notification_channels_project_idx ON public.notification_channels USING btree (tenant_id, project_id) WHERE (deleted_at IS NULL)
CREATE UNIQUE INDEX notification_channels_tenant_id_id_key ON public.notification_channels USING btree (tenant_id, id)
CREATE INDEX notification_deliveries_incident_channel_idx ON public.notification_deliveries USING btree (incident_id, channel_id, created_at) WHERE (incident_id IS NOT NULL)
CREATE INDEX notification_deliveries_lease_idx ON public.notification_deliveries USING btree (lease_until) WHERE (status = 'in_progress'::text)
CREATE INDEX notification_deliveries_monitor_open_idx ON public.notification_deliveries USING btree (monitor_id, created_at, id) WHERE (status = ANY (ARRAY['pending'::text, 'in_progress'::text]))
CREATE UNIQUE INDEX notification_deliveries_pkey ON public.notification_deliveries USING btree (id)
CREATE INDEX notification_deliveries_ready_idx ON public.notification_deliveries USING btree (next_attempt_at) WHERE (status = 'pending'::text)
CREATE INDEX notification_deliveries_tenant_created_idx ON public.notification_deliveries USING btree (tenant_id, created_at DESC)
CREATE UNIQUE INDEX org_auth_domains_domain_key ON public.org_auth_domains USING btree (domain)
CREATE UNIQUE INDEX org_auth_domains_pkey ON public.org_auth_domains USING btree (id)
CREATE UNIQUE INDEX org_auth_domains_tenant_id_id_key ON public.org_auth_domains USING btree (tenant_id, id)
CREATE INDEX org_auth_domains_tenant_idx ON public.org_auth_domains USING btree (tenant_id, domain)
CREATE INDEX password_resets_expires_idx ON public.password_resets USING btree (expires_at)
CREATE UNIQUE INDEX password_resets_pkey ON public.password_resets USING btree (token_hash)
CREATE INDEX password_resets_user_idx ON public.password_resets USING btree (user_id, created_at DESC)
CREATE UNIQUE INDEX plans_pkey ON public.plans USING btree (key)
CREATE UNIQUE INDEX probe_locations_code_key ON public.probe_locations USING btree (code)
CREATE UNIQUE INDEX probe_locations_pkey ON public.probe_locations USING btree (id)
CREATE UNIQUE INDEX project_members_pkey ON public.project_members USING btree (tenant_id, project_id, user_id)
CREATE INDEX project_members_user_idx ON public.project_members USING btree (tenant_id, user_id)
CREATE UNIQUE INDEX projects_one_default_idx ON public.projects USING btree (tenant_id) WHERE is_default
CREATE UNIQUE INDEX projects_pkey ON public.projects USING btree (id)
CREATE UNIQUE INDEX projects_slug_idx ON public.projects USING btree (tenant_id, slug) WHERE (deleted_at IS NULL)
CREATE UNIQUE INDEX projects_tenant_id_id_key ON public.projects USING btree (tenant_id, id)
CREATE INDEX result_rollups_hourly_hour_idx ON public.result_rollups_hourly USING btree (hour)
CREATE UNIQUE INDEX result_rollups_hourly_pkey ON public.result_rollups_hourly USING btree (monitor_id, location_id, hour)
CREATE UNIQUE INDEX rollup_state_pkey ON public.rollup_state USING btree (singleton)
CREATE INDEX sessions_expires_at_idx ON public.sessions USING btree (expires_at)
CREATE UNIQUE INDEX sessions_pkey ON public.sessions USING btree (id)
CREATE INDEX sessions_tenant_user_idx ON public.sessions USING btree (tenant_id, user_id)
CREATE UNIQUE INDEX sessions_token_hash_key ON public.sessions USING btree (token_hash)
CREATE INDEX status_page_components_monitor_idx ON public.status_page_components USING btree (tenant_id, monitor_id)
CREATE UNIQUE INDEX status_page_components_page_id_monitor_id_key ON public.status_page_components USING btree (page_id, monitor_id)
CREATE UNIQUE INDEX status_page_components_page_id_position_key ON public.status_page_components USING btree (page_id, "position")
CREATE UNIQUE INDEX status_page_components_pkey ON public.status_page_components USING btree (id)
CREATE UNIQUE INDEX status_page_components_tenant_id_id_key ON public.status_page_components USING btree (tenant_id, id)
CREATE UNIQUE INDEX status_page_released_slugs_pkey ON public.status_page_released_slugs USING btree (tenant_id, slug)
CREATE INDEX status_page_released_slugs_slug_idx ON public.status_page_released_slugs USING btree (slug, released_at)
CREATE UNIQUE INDEX status_page_snapshots_pkey ON public.status_page_snapshots USING btree (page_id)
CREATE UNIQUE INDEX status_pages_pkey ON public.status_pages USING btree (id)
CREATE UNIQUE INDEX status_pages_slug_idx ON public.status_pages USING btree (slug) WHERE (deleted_at IS NULL)
CREATE UNIQUE INDEX status_pages_tenant_id_id_key ON public.status_pages USING btree (tenant_id, id)
CREATE INDEX status_pages_tenant_idx ON public.status_pages USING btree (tenant_id) WHERE (deleted_at IS NULL)
CREATE UNIQUE INDEX tenants_pkey ON public.tenants USING btree (id)
CREATE UNIQUE INDEX tenants_slug_key ON public.tenants USING btree (slug)
CREATE UNIQUE INDEX users_email_key ON public.users USING btree (email)
CREATE UNIQUE INDEX users_pkey ON public.users USING btree (id)

-- policies
audit_log tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
check_results owner_rollup_read cmd=r permissive=true to=proreta_owner using true
check_results tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
heartbeat_tokens owner_heartbeat_ping cmd=w permissive=true to=proreta_owner using true check true
heartbeat_tokens owner_heartbeat_read cmd=r permissive=true to=proreta_owner using true
heartbeat_tokens tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
incident_monitors tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
incidents owner_reminder_read cmd=r permissive=true to=proreta_owner using true
incidents tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
invitations owner_invitation_lookup cmd=r permissive=true to=proreta_owner using true
invitations tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
memberships owner_membership_lookup cmd=r permissive=true to=proreta_owner using true
memberships tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
monitor_channels owner_reminder_read cmd=r permissive=true to=proreta_owner using true
monitor_channels tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
monitor_location_state owner_claim_lease cmd=w permissive=true to=proreta_owner using true check true
monitor_location_state owner_claim_read cmd=r permissive=true to=proreta_owner using true
monitor_location_state tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
monitor_locations tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
monitor_state tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
monitors owner_claim_read cmd=r permissive=true to=proreta_owner using true
monitors tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
notification_channels owner_reminder_read cmd=r permissive=true to=proreta_owner using true
notification_channels tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
notification_deliveries owner_delivery_read cmd=r permissive=true to=proreta_owner using true
notification_deliveries owner_delivery_update cmd=w permissive=true to=proreta_owner using true check true
notification_deliveries owner_reminder_insert cmd=a permissive=true to=proreta_owner check true
notification_deliveries tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
org_auth_domains owner_domain_lookup cmd=r permissive=true to=proreta_owner using true
org_auth_domains tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
project_members tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
projects tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
result_rollups_hourly owner_rollup_write cmd=* permissive=true to=proreta_owner using true check true
result_rollups_hourly tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
sessions owner_session_lookup cmd=r permissive=true to=proreta_owner using true
sessions owner_session_purge cmd=d permissive=true to=proreta_owner using (expires_at < now())
sessions tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
status_page_components tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
status_page_released_slugs owner_slug_hold_read cmd=r permissive=true to=proreta_owner using true
status_page_released_slugs tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
status_page_snapshots owner_statuspage_read cmd=r permissive=true to=proreta_owner using true
status_page_snapshots tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)
status_pages owner_statuspage_read cmd=r permissive=true to=proreta_owner using true
status_pages tenant_isolation cmd=* permissive=true to=public using (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid) check (tenant_id = (NULLIF(current_setting('app.tenant_id'::text, true), ''::text))::uuid)

-- triggers
CREATE TRIGGER tenants_create_default_project AFTER INSERT ON public.tenants FOR EACH ROW EXECUTE FUNCTION tenants_create_default_project()

-- function privileges
admin_audit(uuid,uuid,uuid,text,text,text,jsonb,inet) owner=proreta_owner definer=false acl=proreta_owner=X/proreta_owner
admin_delete_tenant(uuid,uuid,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_email_settings_clear(uuid,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_email_settings_get(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_email_settings_set_features(uuid,boolean,boolean,boolean,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_email_settings_update(uuid,text,integer,text,text,text,bytea,text,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_email_test_request(uuid,text,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_get_tenant(uuid,uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_list_tenants(uuid,text,boolean,integer,integer) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_list_users(uuid,uuid,text,integer,integer) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_require_platform_admin(uuid) owner=proreta_owner definer=false acl=proreta_owner=X/proreta_owner
admin_reset_user_password(uuid,uuid,text,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_set_tenant_plan(uuid,uuid,text,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_set_tenant_status(uuid,uuid,text,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
admin_stats(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_count_other_memberships(uuid,uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_create_tenant(uuid,text,text) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_domain_lookup(text) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_invitation_lookup(bytea) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_login_tenant(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_lookup_session(bytea) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_revoke_user_sessions(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_sync_platform_admin(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
auth_user_organizations(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
claim_deliveries(integer) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
claim_due_monitors(integer,text) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
claim_mail(integer) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
compute_rollups(timestamp with time zone,timestamp with time zone) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
compute_rollups_catch_up(integer,integer) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
detach_expired_partition(text) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
drop_detached_partition(text) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
email_settings_features() owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
email_settings_for_jobs() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
email_settings_record_test(uuid,boolean,text,integer) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
email_settings_test_state(email_settings) owner=proreta_owner definer=false acl=proreta_owner=X/proreta_owner
enqueue_incident_reminders(integer) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
ensure_partitions() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
fail_deleted_channel_deliveries(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
finish_delivery(uuid,uuid,text,text,timestamp with time zone) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
finish_mail(uuid,uuid,text,text,timestamp with time zone) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
heartbeat_ping(bytea) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
list_droppable_partitions() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
list_due_heartbeats(integer) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
mail_pending() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
org_delete_tenant(uuid,uuid,inet) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
outbox_pending() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
purge_email_verifications() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
purge_expired_sessions() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
purge_mail_outbox(interval) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
purge_password_resets() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
reset_monitor_runtime(uuid) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
status_page_published_pages() owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_worker=X/proreta_owner
status_page_slug_held(text) owner=proreta_owner definer=true acl=proreta_api=X/proreta_owner,proreta_owner=X/proreta_owner
status_page_snapshot(text) owner=proreta_owner definer=true acl=proreta_owner=X/proreta_owner,proreta_statuspage=X/proreta_owner
tenants_create_default_project() owner=proreta_owner definer=false acl=proreta_owner=X/proreta_owner
uuidv7_now() owner=proreta_owner definer=false acl=proreta_owner=X/proreta_owner

-- default privileges
proreta_owner * f proreta_owner=X/proreta_owner
proreta_owner public S proreta_api=rU/proreta_owner,proreta_worker=rU/proreta_owner
proreta_owner public r proreta_api=arwd/proreta_owner,proreta_worker=arwd/proreta_owner

-- functions
CREATE OR REPLACE FUNCTION public.admin_audit(p_tenant_id uuid, p_actor uuid, p_audit_id uuid, p_action text, p_entity_type text, p_entity_id text, p_metadata jsonb, p_ip inet)
 RETURNS void
 LANGUAGE plpgsql
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    caller text := current_setting('app.tenant_id', true);
BEGIN
    PERFORM set_config('app.tenant_id', p_tenant_id::text, true);
    INSERT INTO audit_log (id, tenant_id, actor_user_id, action, entity_type, entity_id, metadata, ip)
    VALUES (p_audit_id, p_tenant_id, p_actor, p_action, p_entity_type, p_entity_id, p_metadata, p_ip);
    PERFORM set_config('app.tenant_id', COALESCE(caller, ''), true);
END
$function$
CREATE OR REPLACE FUNCTION public.admin_delete_tenant(p_actor uuid, p_tenant_id uuid, p_audit_id uuid, p_ip inet)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    v_status  text;
    v_slug    text;
    v_plan    text;
    v_deleted timestamptz;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT t.status, t.slug, t.plan_key, t.deleted_at INTO v_status, v_slug, v_plan, v_deleted
    FROM tenants AS t WHERE t.id = p_tenant_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RETURN false;
    END IF;
    IF v_slug = 'default' THEN
        RAISE EXCEPTION 'admin: the default tenant cannot be deleted' USING ERRCODE = 'PRA05';
    END IF;
    IF EXISTS (SELECT 1 FROM memberships AS m WHERE m.tenant_id = p_tenant_id AND m.user_id = p_actor) THEN
        RAISE EXCEPTION 'admin: cannot delete a tenant the admin belongs to' USING ERRCODE = 'PRA06';
    END IF;
    UPDATE tenants SET deleted_at = now(), status = 'suspended', updated_at = now() WHERE id = p_tenant_id;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id, 'admin.organization.deleted', 'organization', p_tenant_id::text,
                        jsonb_build_object('status', v_status, 'plan', v_plan), p_ip);
    RETURN true;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_email_settings_clear(p_actor uuid, p_audit_id uuid, p_ip inet)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
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
$function$
CREATE OR REPLACE FUNCTION public.admin_email_settings_get(p_actor uuid)
 RETURNS TABLE(host text, port integer, tls text, from_header text, username text, password_set boolean, send_invitations boolean, send_email_verification boolean, allow_password_reset boolean, tested boolean, tested_at timestamp with time zone, test_state text, test_requested_at timestamp with time zone, test_finished_at timestamp with time zone, test_error text, test_error_code integer, updated_at timestamp with time zone)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
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
$function$
CREATE OR REPLACE FUNCTION public.admin_email_settings_set_features(p_actor uuid, p_invitations boolean, p_verification boolean, p_reset boolean, p_audit_id uuid, p_ip inet)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
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
$function$
CREATE OR REPLACE FUNCTION public.admin_email_settings_update(p_actor uuid, p_host text, p_port integer, p_tls text, p_from text, p_username text, p_password_sealed bytea, p_key_id text, p_audit_id uuid, p_ip inet)
 RETURNS TABLE(changed text[], switches_disabled boolean)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
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
$function$
CREATE OR REPLACE FUNCTION public.admin_email_test_request(p_actor uuid, p_to_domain text, p_audit_id uuid, p_ip inet)
 RETURNS uuid
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
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
$function$
CREATE OR REPLACE FUNCTION public.admin_get_tenant(p_actor uuid, p_tenant_id uuid)
 RETURNS TABLE(id uuid, slug text, name text, plan_key text, plan_name text, status text, created_at timestamp with time zone, deleted_at timestamp with time zone, user_count integer, monitor_count integer, heartbeat_count integer)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
#variable_conflict use_column
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    SELECT t.id, t.slug, t.name, t.plan_key, pl.name, t.status, t.created_at, t.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = t.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type = 'http' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = t.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL)
    FROM tenants AS t
    JOIN plans AS pl ON pl.key = t.plan_key
    WHERE t.id = p_tenant_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_list_tenants(p_actor uuid, p_query text, p_include_deleted boolean, p_limit integer, p_offset integer)
 RETURNS TABLE(id uuid, slug text, name text, plan_key text, plan_name text, status text, created_at timestamp with time zone, deleted_at timestamp with time zone, user_count integer, monitor_count integer, heartbeat_count integer, total_count integer)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
#variable_conflict use_column
DECLARE
    q text := lower(btrim(COALESCE(p_query, '')));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN QUERY
    WITH matched AS (
        SELECT t.id, t.slug, t.name, t.plan_key, t.status, t.created_at, t.deleted_at
        FROM tenants AS t
        WHERE (p_include_deleted OR t.deleted_at IS NULL)
          AND (q = ''
               OR strpos(lower(t.slug), q) > 0
               OR strpos(lower(t.name), q) > 0
               OR t.id::text = q
               OR EXISTS (SELECT 1 FROM memberships AS m JOIN users AS u ON u.id = m.user_id
                          WHERE m.tenant_id = t.id AND strpos(lower(u.email::text), q) > 0))
    ), page AS (
        SELECT mt.*, count(*) OVER () AS total
        FROM matched AS mt
        ORDER BY mt.created_at DESC, mt.id DESC
        LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
        OFFSET greatest(COALESCE(p_offset, 0), 0)
    )
    SELECT p.id, p.slug, p.name, p.plan_key, pl.name, p.status, p.created_at, p.deleted_at,
           (SELECT count(*)::integer FROM memberships AS m WHERE m.tenant_id = p.id),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type = 'http' AND mo.deleted_at IS NULL),
           (SELECT count(*)::integer FROM monitors AS mo WHERE mo.tenant_id = p.id AND mo.type = 'heartbeat' AND mo.deleted_at IS NULL),
           p.total::integer
    FROM page AS p
    JOIN plans AS pl ON pl.key = p.plan_key
    ORDER BY p.created_at DESC, p.id DESC;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_list_users(p_actor uuid, p_tenant_id uuid, p_query text, p_limit integer, p_offset integer)
 RETURNS TABLE(id uuid, login text, status text, created_at timestamp with time zone, is_platform_admin boolean, role text, total_count integer)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
#variable_conflict use_column
DECLARE
    q text := lower(btrim(COALESCE(p_query, '')));
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF NOT EXISTS (SELECT 1 FROM tenants AS t WHERE t.id = p_tenant_id) THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    RETURN QUERY
    SELECT u.id, u.email::text, u.status, u.created_at, u.is_platform_admin, m.role,
           (count(*) OVER ())::integer
    FROM memberships AS m
    JOIN users AS u ON u.id = m.user_id
    WHERE m.tenant_id = p_tenant_id
      AND (q = '' OR strpos(lower(u.email::text), q) > 0)
    ORDER BY m.created_at, u.id
    LIMIT least(greatest(COALESCE(p_limit, 25), 1), 100)
    OFFSET greatest(COALESCE(p_offset, 0), 0);
END
$function$
CREATE OR REPLACE FUNCTION public.admin_require_platform_admin(p_actor uuid)
 RETURNS void
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'public', 'pg_temp'
AS $function$
BEGIN
    IF p_actor IS NULL OR NOT EXISTS (
        SELECT 1 FROM users AS u WHERE u.id = p_actor AND u.status = 'active' AND u.is_platform_admin
    ) THEN
        RAISE EXCEPTION 'admin: actor is not a platform admin' USING ERRCODE = 'PRA01';
    END IF;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_reset_user_password(p_actor uuid, p_user_id uuid, p_password_hash text, p_audit_id uuid, p_ip inet)
 RETURNS TABLE(tenant_id uuid, sessions_revoked integer)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
#variable_conflict use_column
DECLARE
    v_admin  boolean;
    v_tenant uuid;
    v_n      integer;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_password_hash IS NULL OR p_password_hash NOT LIKE '$argon2id$%' THEN
        RAISE EXCEPTION 'admin: invalid password hash' USING ERRCODE = 'PRA04';
    END IF;
    SELECT u.is_platform_admin INTO v_admin FROM users AS u WHERE u.id = p_user_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: user not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_admin THEN
        RAISE EXCEPTION 'admin: a platform admin password is managed by its Secret' USING ERRCODE = 'PRA07';
    END IF;
    v_tenant := auth_login_tenant(p_user_id);
    IF v_tenant IS NULL THEN
        RAISE EXCEPTION 'admin: user has no membership' USING ERRCODE = 'PRA02';
    END IF;
    -- updated_at changes with the hash: a login verified against the old hash is refused when
    -- it creates its session. The user must replace the temporary password at the next sign-in.
    UPDATE users SET password_hash = p_password_hash, password_must_change = true, updated_at = now() WHERE id = p_user_id;
    v_n := auth_revoke_user_sessions(p_user_id);
    PERFORM admin_audit(v_tenant, p_actor, p_audit_id, 'admin.user.password_reset', 'user', p_user_id::text,
                        jsonb_build_object('sessionsRevoked', v_n), p_ip);
    RETURN QUERY SELECT v_tenant, v_n;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_set_tenant_plan(p_actor uuid, p_tenant_id uuid, p_plan_key text, p_audit_id uuid, p_ip inet)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    v_old     text;
    v_deleted timestamptz;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    SELECT t.plan_key, t.deleted_at INTO v_old, v_deleted FROM tenants AS t WHERE t.id = p_tenant_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RAISE EXCEPTION 'admin: tenant is deleted' USING ERRCODE = 'PRA08';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM plans AS p WHERE p.key = p_plan_key) THEN
        RAISE EXCEPTION 'admin: unknown plan' USING ERRCODE = 'PRA03';
    END IF;
    IF v_old = p_plan_key THEN
        RETURN false;
    END IF;
    UPDATE tenants SET plan_key = p_plan_key, updated_at = now() WHERE id = p_tenant_id;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id, 'admin.organization.plan_changed', 'organization', p_tenant_id::text,
                        jsonb_build_object('from', v_old, 'to', p_plan_key), p_ip);
    RETURN true;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_set_tenant_status(p_actor uuid, p_tenant_id uuid, p_status text, p_audit_id uuid, p_ip inet)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    v_old     text;
    v_slug    text;
    v_deleted timestamptz;
    v_expired integer;
    v_meta    jsonb;
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    IF p_status IS NULL OR p_status NOT IN ('active', 'suspended') THEN
        RAISE EXCEPTION 'admin: invalid status' USING ERRCODE = 'PRA04';
    END IF;
    SELECT t.status, t.slug, t.deleted_at INTO v_old, v_slug, v_deleted FROM tenants AS t WHERE t.id = p_tenant_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'admin: tenant not found' USING ERRCODE = 'PRA02';
    END IF;
    IF v_deleted IS NOT NULL THEN
        RAISE EXCEPTION 'admin: tenant is deleted' USING ERRCODE = 'PRA08';
    END IF;
    IF p_status = 'suspended' THEN
        IF v_slug = 'default' THEN
            RAISE EXCEPTION 'admin: the default tenant cannot be suspended' USING ERRCODE = 'PRA05';
        END IF;
        IF EXISTS (SELECT 1 FROM memberships AS m WHERE m.tenant_id = p_tenant_id AND m.user_id = p_actor) THEN
            RAISE EXCEPTION 'admin: cannot suspend a tenant the admin belongs to' USING ERRCODE = 'PRA06';
        END IF;
    END IF;
    IF v_old = p_status THEN
        RETURN false;
    END IF;
    UPDATE tenants SET status = p_status, updated_at = now() WHERE id = p_tenant_id;
    v_meta := jsonb_build_object('from', v_old, 'to', p_status);
    IF p_status = 'active' THEN
        -- owner_delivery_update lets the owner update rows of any tenant; claim_deliveries skips
        -- the tenant until this transaction commits, so no row is in progress.
        UPDATE notification_deliveries AS d
        SET status = 'failed', last_error = 'tenant_suspended'
        WHERE d.tenant_id = p_tenant_id AND d.status = 'pending' AND d.created_at < now() - interval '15 minutes';
        GET DIAGNOSTICS v_expired = ROW_COUNT;
        v_meta := v_meta || jsonb_build_object('deliveriesExpired', v_expired);
    END IF;
    PERFORM admin_audit(p_tenant_id, p_actor, p_audit_id,
                        CASE WHEN p_status = 'suspended' THEN 'admin.organization.suspended' ELSE 'admin.organization.unsuspended' END,
                        'organization', p_tenant_id::text, v_meta, p_ip);
    RETURN true;
END
$function$
CREATE OR REPLACE FUNCTION public.admin_stats(p_actor uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
BEGIN
    PERFORM admin_require_platform_admin(p_actor);
    RETURN jsonb_build_object(
        'tenants', (SELECT jsonb_build_object(
                        'total', count(*) FILTER (WHERE t.deleted_at IS NULL),
                        'active', count(*) FILTER (WHERE t.deleted_at IS NULL AND t.status = 'active'),
                        'suspended', count(*) FILTER (WHERE t.deleted_at IS NULL AND t.status = 'suspended'),
                        'deleted', count(*) FILTER (WHERE t.deleted_at IS NOT NULL))
                    FROM tenants AS t),
        'users', (SELECT count(*) FROM users AS u WHERE u.status = 'active'),
        'monitors', (SELECT count(*) FROM monitors AS m JOIN tenants AS t ON t.id = m.tenant_id
                     WHERE m.type = 'http' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
        'heartbeats', (SELECT count(*) FROM monitors AS m JOIN tenants AS t ON t.id = m.tenant_id
                       WHERE m.type = 'heartbeat' AND m.deleted_at IS NULL AND t.deleted_at IS NULL),
        'plans', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                         'key', p.key, 'name', p.name,
                         'tenants', (SELECT count(*) FROM tenants AS t WHERE t.plan_key = p.key AND t.deleted_at IS NULL))
                      ORDER BY p.key), '[]'::jsonb)
                  FROM plans AS p)
    );
END
$function$
CREATE OR REPLACE FUNCTION public.auth_count_other_memberships(p_user_id uuid, p_tenant_id uuid)
 RETURNS integer
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT count(*)::integer
    FROM memberships AS m
    WHERE m.user_id = p_user_id AND m.tenant_id <> p_tenant_id
$function$
CREATE OR REPLACE FUNCTION public.auth_create_tenant(p_id uuid, p_slug text, p_name text)
 RETURNS uuid
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    INSERT INTO tenants (id, slug, name, plan_key)
    VALUES (p_id, p_slug, p_name, CASE WHEN p_slug = 'default' THEN 'internal' ELSE 'free' END)
    ON CONFLICT (slug) DO NOTHING
    RETURNING id
$function$
CREATE OR REPLACE FUNCTION public.auth_domain_lookup(p_domain text)
 RETURNS TABLE(tenant_id uuid, default_role text)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT d.tenant_id, d.default_role
    FROM org_auth_domains AS d
    JOIN tenants AS t ON t.id = d.tenant_id
    WHERE d.domain = p_domain
      AND d.enabled
      AND t.status = 'active'
      AND t.deleted_at IS NULL
$function$
CREATE OR REPLACE FUNCTION public.auth_invitation_lookup(p_token_hash bytea)
 RETURNS TABLE(invitation_id uuid, tenant_id uuid, tenant_name text, role text, expires_at timestamp with time zone)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT i.id, i.tenant_id, t.name, i.role, i.expires_at
    FROM invitations AS i
    JOIN tenants AS t ON t.id = i.tenant_id
    WHERE i.token_hash = p_token_hash
      AND length(p_token_hash) = 32
      AND i.accepted_at IS NULL
      AND i.revoked_at IS NULL
      AND i.expires_at > now()
      AND t.status = 'active'
      AND t.deleted_at IS NULL
$function$
CREATE OR REPLACE FUNCTION public.auth_login_tenant(p_user_id uuid)
 RETURNS uuid
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT m.tenant_id
    FROM memberships AS m
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.user_id = p_user_id
    ORDER BY (t.deleted_at IS NULL) DESC, (t.status = 'active') DESC, m.last_used_at DESC NULLS LAST,
             (t.slug = 'default') DESC, m.created_at, m.tenant_id
    LIMIT 1
$function$
CREATE OR REPLACE FUNCTION public.auth_lookup_session(p_token_hash bytea)
 RETURNS TABLE(id uuid, user_id uuid, tenant_id uuid, csrf_token text, created_at timestamp with time zone, expires_at timestamp with time zone, last_seen_at timestamp with time zone)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT s.id, s.user_id, s.tenant_id, s.csrf_token, s.created_at, s.expires_at, s.last_seen_at
    FROM sessions AS s
    JOIN users AS u ON u.id = s.user_id
    JOIN tenants AS t ON t.id = s.tenant_id
    WHERE s.token_hash = p_token_hash
      AND s.expires_at > now()
      AND u.status = 'active'
      AND t.status = 'active'
      AND t.deleted_at IS NULL
$function$
CREATE OR REPLACE FUNCTION public.auth_revoke_user_sessions(p_user_id uuid)
 RETURNS integer
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    caller text := current_setting('app.tenant_id', true);
    t      uuid;
    n      integer;
    total  integer := 0;
BEGIN
    FOR t IN SELECT DISTINCT s.tenant_id FROM sessions AS s WHERE s.user_id = p_user_id LOOP
        PERFORM set_config('app.tenant_id', t::text, true);
        DELETE FROM sessions AS s WHERE s.tenant_id = t AND s.user_id = p_user_id;
        GET DIAGNOSTICS n = ROW_COUNT;
        total := total + n;
    END LOOP;
    PERFORM set_config('app.tenant_id', COALESCE(caller, ''), true);
    RETURN total;
END
$function$
CREATE OR REPLACE FUNCTION public.auth_sync_platform_admin(p_user_id uuid)
 RETURNS integer
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    n integer;
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM users AS u
        JOIN memberships AS m ON m.user_id = u.id
        JOIN tenants AS t ON t.id = m.tenant_id
        WHERE u.id = p_user_id AND u.status = 'active' AND m.role = 'owner'
          AND t.slug = 'default' AND t.deleted_at IS NULL
    ) OR EXISTS (
        SELECT 1
        FROM memberships AS m
        JOIN tenants AS t ON t.id = m.tenant_id
        WHERE m.user_id = p_user_id AND t.slug <> 'default'
    ) THEN
        RAISE EXCEPTION 'auth_sync_platform_admin: user is not the owner of the default tenant only'
            USING ERRCODE = 'PRA01';
    END IF;
    UPDATE users AS u
    SET is_platform_admin = (u.id = p_user_id), updated_at = now()
    WHERE u.is_platform_admin IS DISTINCT FROM (u.id = p_user_id);
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$function$
CREATE OR REPLACE FUNCTION public.auth_user_organizations(p_user_id uuid)
 RETURNS TABLE(tenant_id uuid, slug text, name text, status text, role text, joined_at timestamp with time zone)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT t.id, t.slug, t.name, t.status, m.role, m.created_at
    FROM memberships AS m
    JOIN tenants AS t ON t.id = m.tenant_id
    WHERE m.user_id = p_user_id
      AND t.deleted_at IS NULL
    ORDER BY lower(t.name), t.id
$function$
CREATE OR REPLACE FUNCTION public.claim_deliveries(n integer)
 RETURNS TABLE(id uuid, tenant_id uuid, kind text, monitor_id uuid, incident_id uuid, channel_id uuid, payload jsonb, attempts integer, created_at timestamp with time zone, claim_token uuid)
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    UPDATE notification_deliveries AS d
    SET status = CASE WHEN d.attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END,
        attempts = d.attempts + 1,
        last_error = 'lease_expired',
        lease_until = NULL,
        claim_token = NULL
    WHERE d.id IN (
        SELECT e.id FROM notification_deliveries AS e
        WHERE e.status = 'in_progress' AND e.lease_until <= now()
        FOR UPDATE SKIP LOCKED
    );

    UPDATE notification_deliveries AS d
    SET status = 'failed', last_error = 'tenant_deleted'
    WHERE d.id IN (
        SELECT e.id FROM notification_deliveries AS e
        JOIN tenants AS t ON t.id = e.tenant_id
        WHERE e.status = 'pending' AND t.deleted_at IS NOT NULL
        FOR UPDATE OF e SKIP LOCKED
    );

    WITH ready AS (
        SELECT d.id
        FROM notification_deliveries AS d
        WHERE d.status = 'pending'
          AND d.next_attempt_at <= now()
          AND EXISTS (
              SELECT 1 FROM tenants AS t
              WHERE t.id = d.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
          )
          AND NOT EXISTS (
              SELECT 1
              FROM notification_deliveries AS o
              WHERE o.monitor_id = d.monitor_id
                AND o.channel_id = d.channel_id
                AND o.status IN ('pending', 'in_progress')
                AND (o.created_at, o.id) < (d.created_at, d.id)
          )
        ORDER BY d.created_at, d.id
        LIMIT least(greatest(n, 0), 100)
        FOR UPDATE OF d SKIP LOCKED
    )
    UPDATE notification_deliveries AS d
    SET status = 'in_progress', lease_until = now() + interval '2 minutes', claim_token = gen_random_uuid()
    FROM ready
    WHERE d.id = ready.id
    RETURNING d.id, d.tenant_id, d.kind, d.monitor_id, d.incident_id, d.channel_id, d.payload, d.attempts, d.created_at,
              d.claim_token
$function$
CREATE OR REPLACE FUNCTION public.claim_due_monitors(n integer, p_location text)
 RETURNS TABLE(tenant_id uuid, monitor_id uuid, location_id uuid, lease_token uuid, lease_until timestamp with time zone, url text, expected_status text, failure_threshold integer, interval_seconds integer, timeout_seconds integer, next_run_at timestamp with time zone)
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    WITH due AS (
        SELECT s.monitor_id, s.location_id
        FROM monitor_location_state AS s
        JOIN monitors AS m ON m.tenant_id = s.tenant_id AND m.id = s.monitor_id
        JOIN tenants AS t ON t.id = s.tenant_id
        JOIN probe_locations AS l ON l.id = s.location_id
        WHERE l.code = p_location
          AND l.status = 'active'
          AND t.status = 'active'
          AND t.deleted_at IS NULL
          AND s.next_run_at <= now()
          AND (s.lease_token IS NULL OR s.lease_until <= now())
          AND m.type = 'http'
          AND NOT m.paused
          AND m.deleted_at IS NULL
        ORDER BY s.next_run_at
        LIMIT least(greatest(n, 0), 1000)
        FOR UPDATE OF s SKIP LOCKED
    )
    UPDATE monitor_location_state AS s
    SET lease_token = gen_random_uuid(),
        lease_until = now() + interval '30 seconds'
    FROM due, monitors AS m
    WHERE s.monitor_id = due.monitor_id AND s.location_id = due.location_id
      AND m.tenant_id = s.tenant_id AND m.id = s.monitor_id
    RETURNING s.tenant_id, s.monitor_id, s.location_id, s.lease_token, s.lease_until,
              m.config ->> 'url', COALESCE(m.config ->> 'expectedStatus', '2xx'),
              m.failure_threshold, m.interval_seconds, m.timeout_seconds, s.next_run_at
$function$
CREATE OR REPLACE FUNCTION public.claim_mail(n integer)
 RETURNS TABLE(id uuid, kind text, payload bytea, attempts integer, claim_token uuid)
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    UPDATE mail_outbox AS m
    SET status = CASE WHEN m.attempts + 1 >= 5 THEN 'failed' ELSE 'pending' END,
        attempts = m.attempts + 1,
        last_error = 'lease_expired',
        lease_until = NULL,
        claim_token = NULL,
        finished_at = CASE WHEN m.attempts + 1 >= 5 THEN now() END,
        payload = CASE WHEN m.attempts + 1 >= 5 THEN ''::bytea ELSE m.payload END
    WHERE m.id IN (
        SELECT e.id FROM mail_outbox AS e
        WHERE e.status = 'in_progress' AND e.lease_until <= now()
        FOR UPDATE SKIP LOCKED
    );

    UPDATE mail_outbox AS m
    SET status = 'failed', last_error = 'expired', finished_at = now(), payload = ''::bytea
    WHERE m.id IN (
        SELECT e.id FROM mail_outbox AS e
        WHERE e.status = 'pending' AND e.expires_at <= now()
        FOR UPDATE SKIP LOCKED
    );

    WITH ready AS (
        SELECT e.id
        FROM mail_outbox AS e
        WHERE e.status = 'pending' AND e.next_attempt_at <= now()
        ORDER BY e.created_at, e.id
        LIMIT least(greatest(n, 0), 100)
        FOR UPDATE SKIP LOCKED
    )
    UPDATE mail_outbox AS m
    SET status = 'in_progress', lease_until = now() + interval '2 minutes', claim_token = gen_random_uuid()
    FROM ready
    WHERE m.id = ready.id
    RETURNING m.id, m.kind, m.payload, m.attempts, m.claim_token
$function$
CREATE OR REPLACE FUNCTION public.compute_rollups(from_hour timestamp with time zone, to_hour timestamp with time zone)
 RETURNS integer
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
 SET "TimeZone" TO 'UTC'
AS $function$
DECLARE
    lo      timestamptz := date_trunc('hour', from_hour);
    hi      timestamptz := least(date_trunc('hour', to_hour), date_trunc('hour', now()));
    written integer;
BEGIN
    IF lo IS NULL OR hi IS NULL OR lo >= hi THEN
        RETURN 0;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.compute_rollups'));
    INSERT INTO result_rollups_hourly AS r
        (tenant_id, monitor_id, location_id, hour, total, ok_count, latency_sum_ms, latency_histogram, computed_at)
    SELECT c.tenant_id, c.monitor_id, c.location_id, date_trunc('hour', c.checked_at),
           count(*), count(*) FILTER (WHERE c.ok), COALESCE(sum(c.latency_ms), 0),
           ARRAY[
               count(*) FILTER (WHERE c.latency_ms <= 10),
               count(*) FILTER (WHERE c.latency_ms > 10 AND c.latency_ms <= 25),
               count(*) FILTER (WHERE c.latency_ms > 25 AND c.latency_ms <= 50),
               count(*) FILTER (WHERE c.latency_ms > 50 AND c.latency_ms <= 100),
               count(*) FILTER (WHERE c.latency_ms > 100 AND c.latency_ms <= 250),
               count(*) FILTER (WHERE c.latency_ms > 250 AND c.latency_ms <= 500),
               count(*) FILTER (WHERE c.latency_ms > 500 AND c.latency_ms <= 1000),
               count(*) FILTER (WHERE c.latency_ms > 1000 AND c.latency_ms <= 2500),
               count(*) FILTER (WHERE c.latency_ms > 2500 AND c.latency_ms <= 5000),
               count(*) FILTER (WHERE c.latency_ms > 5000 AND c.latency_ms <= 10000),
               count(*) FILTER (WHERE c.latency_ms > 10000)
           ]::integer[],
           now()
    FROM check_results AS c
    WHERE c.checked_at >= lo AND c.checked_at < hi
    GROUP BY c.tenant_id, c.monitor_id, c.location_id, date_trunc('hour', c.checked_at)
    ON CONFLICT (monitor_id, location_id, hour) DO UPDATE
    SET total = EXCLUDED.total,
        ok_count = EXCLUDED.ok_count,
        latency_sum_ms = EXCLUDED.latency_sum_ms,
        latency_histogram = EXCLUDED.latency_histogram,
        computed_at = EXCLUDED.computed_at;
    GET DIAGNOSTICS written = ROW_COUNT;
    RETURN written;
END
$function$
CREATE OR REPLACE FUNCTION public.compute_rollups_catch_up(p_max_hours integer, p_lookback_hours integer)
 RETURNS integer
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
 SET "TimeZone" TO 'UTC'
AS $function$
DECLARE
    closed_end timestamptz := date_trunc('hour', now());
    floor_hour timestamptz := date_trunc('hour', now()) - interval '7 days';
    w          timestamptz;
    lo         timestamptz;
    hi         timestamptz;
    written    integer;
BEGIN
    IF p_max_hours IS NULL OR p_max_hours NOT BETWEEN 1 AND 168
       OR p_lookback_hours IS NULL OR p_lookback_hours NOT BETWEEN 0 AND 24 THEN
        RAISE EXCEPTION 'compute_rollups_catch_up: p_max_hours must be 1..168 and p_lookback_hours 0..24'
            USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.compute_rollups'));
    SELECT s.watermark INTO w FROM rollup_state AS s WHERE s.singleton;
    IF w IS NULL THEN
        SELECT date_trunc('hour', min(c.checked_at)) INTO w
        FROM check_results AS c
        WHERE c.checked_at >= floor_hour AND c.checked_at < closed_end;
        w := COALESCE(w, closed_end);
    END IF;
    w  := least(greatest(w, floor_hour), closed_end);
    hi := least(w + make_interval(hours => p_max_hours), closed_end);
    lo := least(w, closed_end - make_interval(hours => p_lookback_hours));
    written := compute_rollups(lo, hi);
    INSERT INTO rollup_state AS s (singleton, watermark, updated_at) VALUES (true, hi, now())
    ON CONFLICT (singleton) DO UPDATE SET watermark = EXCLUDED.watermark, updated_at = EXCLUDED.updated_at;
    RETURN written;
END
$function$
CREATE OR REPLACE FUNCTION public.detach_expired_partition(p_name text)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
 SET "TimeZone" TO 'UTC'
 SET lock_timeout TO '1s'
AS $function$
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
    SELECT EXISTS (
        SELECT 1
        FROM (
            SELECT c.monitor_id, c.location_id, date_trunc('hour', c.checked_at) AS hour, count(*) AS n
            FROM check_results AS c
            WHERE c.checked_at >= lo AND c.checked_at < hi
            GROUP BY 1, 2, 3
        ) AS raw
        LEFT JOIN result_rollups_hourly AS r
          ON r.monitor_id = raw.monitor_id AND r.location_id = raw.location_id AND r.hour = raw.hour
        WHERE r.total IS DISTINCT FROM raw.n
    ) INTO incomplete;
    IF incomplete THEN
        RETURN false;
    END IF;
    EXECUTE format('ALTER TABLE check_results DETACH PARTITION %I', p_name);
    RETURN true;
END
$function$
CREATE OR REPLACE FUNCTION public.drop_detached_partition(p_name text)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
 SET "TimeZone" TO 'UTC'
 SET lock_timeout TO '5s'
AS $function$
DECLARE
    lo timestamptz;
BEGIN
    IF p_name IS NULL OR p_name !~ '^check_results_p[0-9]{4}(0[1-9]|1[0-2])$' THEN
        RAISE EXCEPTION 'drop_detached_partition: invalid partition name' USING ERRCODE = '22023';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('proreta.ensure_partitions'));
    lo := make_timestamptz(substr(p_name, 16, 4)::integer, substr(p_name, 20, 2)::integer, 1, 0, 0, 0, 'UTC');
    IF lo + interval '1 month' >= now() - interval '30 days' OR lo = date_trunc('month', now()) THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class AS c
        JOIN pg_namespace AS ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
        WHERE c.relname = p_name
          AND c.relkind = 'r'
          AND NOT c.relispartition
          AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
    ) THEN
        RETURN false;
    END IF;
    EXECUTE format('DROP TABLE %I', p_name);
    RETURN true;
END
$function$
CREATE OR REPLACE FUNCTION public.email_settings_features()
 RETURNS TABLE(enabled boolean, send_invitations boolean, send_email_verification boolean, allow_password_reset boolean)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT s.host <> '' AND s.tested_version IS NOT DISTINCT FROM s.connection_version,
           s.send_invitations, s.send_email_verification, s.allow_password_reset
    FROM email_settings AS s
$function$
CREATE OR REPLACE FUNCTION public.email_settings_for_jobs()
 RETURNS TABLE(host text, port integer, tls text, from_header text, username text, password_sealed bytea, connection_version uuid)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT s.host, s.port, s.tls, s.from_header, s.username, s.password_sealed, s.connection_version
    FROM email_settings AS s
    WHERE s.host <> ''
$function$
CREATE OR REPLACE FUNCTION public.email_settings_record_test(p_version uuid, p_ok boolean, p_error text, p_code integer)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
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
$function$
CREATE OR REPLACE FUNCTION public.email_settings_test_state(s email_settings)
 RETURNS text
 LANGUAGE sql
 STABLE
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT CASE
        WHEN s.test_requested_at IS NULL THEN 'none'
        WHEN s.test_finished_at IS NOT NULL AND s.test_ok THEN 'succeeded'
        WHEN s.test_finished_at IS NOT NULL THEN 'failed'
        WHEN s.test_requested_at > now() - interval '3 minutes' THEN 'pending'
        ELSE 'failed'
    END
$function$
CREATE OR REPLACE FUNCTION public.enqueue_incident_reminders(p_limit integer)
 RETURNS integer
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT pg_advisory_xact_lock(hashtext('proreta.enqueue_incident_reminders'));

    WITH due AS (
        SELECT i.tenant_id, i.id AS incident_id, i.monitor_id, i.started_at, c.id AS channel_id, m.name AS monitor_name
        FROM incidents AS i
        JOIN tenants AS t ON t.id = i.tenant_id
        JOIN monitors AS m ON m.tenant_id = i.tenant_id AND m.id = i.monitor_id
        JOIN notification_channels AS c ON c.tenant_id = i.tenant_id
        WHERE i.resolved_at IS NULL
          AND t.status = 'active' AND t.deleted_at IS NULL
          AND m.notifications_enabled AND NOT m.paused AND m.deleted_at IS NULL
          AND c.deleted_at IS NULL AND c.status = 'active' AND c.reminder_minutes > 0
          AND 'down' = ANY (c.events)
          AND (
              (EXISTS (SELECT 1 FROM monitor_channels AS mc
                       WHERE mc.tenant_id = i.tenant_id AND mc.monitor_id = m.id AND mc.channel_id = c.id)
               AND (c.project_id IS NULL OR c.project_id = m.project_id))
              OR (NOT EXISTS (SELECT 1 FROM monitor_channels AS mc
                              JOIN notification_channels AS c2 ON c2.tenant_id = mc.tenant_id AND c2.id = mc.channel_id
                              WHERE mc.tenant_id = i.tenant_id AND mc.monitor_id = m.id
                                AND (c2.project_id IS NULL OR c2.project_id = m.project_id))
                  AND c.project_id = m.project_id)
          )
          AND coalesce(
              (SELECT max(d.created_at) FROM notification_deliveries AS d
               WHERE d.incident_id = i.id AND d.channel_id = c.id AND d.kind IN ('opened', 'reminder')),
              i.started_at
          ) <= now() - make_interval(mins => c.reminder_minutes)
        ORDER BY i.started_at, i.id, c.id
        LIMIT least(greatest(p_limit, 0), 1000)
    ),
    queued AS (
        INSERT INTO notification_deliveries (id, tenant_id, kind, monitor_id, incident_id, channel_id, payload)
        SELECT uuidv7_now(),
               due.tenant_id, 'reminder', due.monitor_id, due.incident_id, due.channel_id,
               jsonb_build_object(
                   'monitorId', due.monitor_id,
                   'monitorName', due.monitor_name,
                   'incidentId', due.incident_id,
                   'startedAt', due.started_at,
                   'durationSeconds', greatest(floor(extract(epoch FROM now() - due.started_at)), 0)::bigint
               )
        FROM due
        RETURNING 1
    )
    SELECT count(*)::integer FROM queued
$function$
CREATE OR REPLACE FUNCTION public.ensure_partitions()
 RETURNS integer
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
 SET "TimeZone" TO 'UTC'
 SET lock_timeout TO '5s'
AS $function$
DECLARE
    first_month timestamptz := date_trunc('month', now());
    lo          timestamptz;
    hi          timestamptz;
    part        text;
    created     integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('proreta.ensure_partitions'));
    FOR i IN 0..2 LOOP
        lo := first_month + make_interval(months => i);
        hi := first_month + make_interval(months => i + 1);
        part := 'check_results_p' || to_char(lo, 'YYYYMM');
        CONTINUE WHEN to_regclass('public.' || quote_ident(part)) IS NOT NULL;
        EXECUTE format('CREATE TABLE %I PARTITION OF check_results FOR VALUES FROM (%L) TO (%L)', part, lo, hi);
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', part);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', part);
        EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
            USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
            WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)$p$, part);
        EXECUTE format('REVOKE ALL ON %I FROM proreta_api, proreta_worker', part);
        EXECUTE format('GRANT SELECT ON %I TO proreta_api', part);
        EXECUTE format('GRANT SELECT, INSERT ON %I TO proreta_worker', part);
        created := created + 1;
    END LOOP;
    RETURN created;
END
$function$
CREATE OR REPLACE FUNCTION public.fail_deleted_channel_deliveries(p_channel_id uuid)
 RETURNS integer
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    WITH failed AS (
        UPDATE notification_deliveries AS d
        SET status = 'failed', last_error = 'channel_deleted'
        WHERE d.tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid
          AND d.channel_id = p_channel_id
          AND d.status = 'pending'
          AND EXISTS (
              SELECT 1 FROM notification_channels AS c
              WHERE c.tenant_id = d.tenant_id AND c.id = p_channel_id AND c.deleted_at IS NOT NULL
          )
        RETURNING 1
    )
    SELECT count(*)::integer FROM failed
$function$
CREATE OR REPLACE FUNCTION public.finish_delivery(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamp with time zone)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending') THEN
        RAISE EXCEPTION 'finish_delivery: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status = 'pending' AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_delivery: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE notification_deliveries AS d
    SET status = p_status,
        attempts = d.attempts + 1,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        sent_at = CASE WHEN p_status = 'sent' THEN now() END,
        next_attempt_at = CASE WHEN p_status = 'pending' THEN p_retry_at ELSE d.next_attempt_at END
    WHERE d.id = p_id AND d.status = 'in_progress' AND d.claim_token = p_token;
    RETURN FOUND;
END
$function$
CREATE OR REPLACE FUNCTION public.finish_mail(p_id uuid, p_token uuid, p_status text, p_error text, p_retry_at timestamp with time zone)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
BEGIN
    IF p_status IS NULL OR p_status NOT IN ('sent', 'failed', 'pending') THEN
        RAISE EXCEPTION 'finish_mail: invalid status' USING ERRCODE = '22023';
    END IF;
    IF p_status = 'pending' AND p_retry_at IS NULL THEN
        RAISE EXCEPTION 'finish_mail: retry_at is required for pending' USING ERRCODE = '22023';
    END IF;
    UPDATE mail_outbox AS m
    SET status = p_status,
        attempts = m.attempts + 1,
        lease_until = NULL,
        claim_token = NULL,
        last_error = CASE WHEN p_status = 'sent' THEN NULL ELSE left(p_error, 500) END,
        finished_at = CASE WHEN p_status = 'pending' THEN NULL ELSE now() END,
        payload = CASE WHEN p_status = 'pending' THEN m.payload ELSE ''::bytea END,
        next_attempt_at = CASE WHEN p_status = 'pending' THEN p_retry_at ELSE m.next_attempt_at END
    WHERE m.id = p_id AND m.status = 'in_progress' AND m.claim_token = p_token;
    RETURN FOUND;
END
$function$
CREATE OR REPLACE FUNCTION public.heartbeat_ping(p_token_hash bytea)
 RETURNS TABLE(tenant_id uuid, monitor_id uuid, recorded boolean)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
#variable_conflict use_column
DECLARE
    v_tenant  uuid;
    v_monitor uuid;
BEGIN
    IF p_token_hash IS NULL OR length(p_token_hash) <> 32 THEN
        RETURN;
    END IF;
    PERFORM 1 FROM heartbeat_tokens AS h WHERE h.token_hash = p_token_hash FOR UPDATE;
    UPDATE heartbeat_tokens AS h
    SET last_ping_at = greatest(h.last_ping_at, clock_timestamp())
    FROM monitors AS m, tenants AS t
    WHERE h.token_hash = p_token_hash
      AND m.tenant_id = h.tenant_id AND m.id = h.monitor_id
      AND NOT m.paused AND m.deleted_at IS NULL
      AND t.id = h.tenant_id AND t.status = 'active' AND t.deleted_at IS NULL
    RETURNING h.tenant_id, h.monitor_id INTO v_tenant, v_monitor;
    IF FOUND THEN
        RETURN QUERY SELECT v_tenant, v_monitor, true;
        RETURN;
    END IF;
    RETURN QUERY
        SELECT h.tenant_id, h.monitor_id, false
        FROM heartbeat_tokens AS h
        WHERE h.token_hash = p_token_hash;
END
$function$
CREATE OR REPLACE FUNCTION public.list_droppable_partitions()
 RETURNS TABLE(name text, detached boolean)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
 SET "TimeZone" TO 'UTC'
AS $function$
    SELECT c.relname::text, NOT c.relispartition
    FROM pg_class AS c
    JOIN pg_namespace AS ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
    WHERE c.relkind = 'r'
      AND c.relname ~ '^check_results_p[0-9]{4}(0[1-9]|1[0-2])$'
      AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
      AND (NOT c.relispartition
           OR EXISTS (SELECT 1 FROM pg_inherits AS i WHERE i.inhrelid = c.oid AND i.inhparent = 'public.check_results'::regclass))
      AND make_timestamptz(substr(c.relname, 16, 4)::integer, substr(c.relname, 20, 2)::integer, 1, 0, 0, 0, 'UTC')
          + interval '1 month' < now() - interval '30 days'
      AND make_timestamptz(substr(c.relname, 16, 4)::integer, substr(c.relname, 20, 2)::integer, 1, 0, 0, 0, 'UTC')
          <> date_trunc('month', now())
    ORDER BY 1
$function$
CREATE OR REPLACE FUNCTION public.list_due_heartbeats(n integer)
 RETURNS TABLE(tenant_id uuid, monitor_id uuid)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT h.tenant_id, h.monitor_id
    FROM heartbeat_tokens AS h
    JOIN monitors AS m ON m.tenant_id = h.tenant_id AND m.id = h.monitor_id
    JOIN tenants AS t ON t.id = h.tenant_id
    CROSS JOIN LATERAL (
        SELECT greatest(h.waiting_since, coalesce(h.last_ping_at, h.waiting_since))
               + make_interval(secs => m.interval_seconds
                   + CASE WHEN m.type = 'heartbeat' THEN (m.config ->> 'graceSeconds')::numeric::integer ELSE 0 END) AS deadline
    ) AS d
    WHERE m.type = 'heartbeat'
      AND NOT m.paused
      AND m.deleted_at IS NULL
      AND t.status = 'active'
      AND t.deleted_at IS NULL
      AND ((h.last_ping_at IS NOT NULL AND (h.applied_ping_at IS NULL OR h.last_ping_at > h.applied_ping_at))
           OR (d.deadline < now() AND (h.missed_deadline IS NULL OR h.missed_deadline < d.deadline)))
    ORDER BY d.deadline, h.monitor_id
    LIMIT least(greatest(n, 0), 1000)
$function$
CREATE OR REPLACE FUNCTION public.mail_pending()
 RETURNS bigint
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT count(*) FROM mail_outbox WHERE status = 'pending'
$function$
CREATE OR REPLACE FUNCTION public.org_delete_tenant(p_actor uuid, p_audit_id uuid, p_ip inet)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    v_tenant  uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    v_slug    text;
    v_deleted timestamptz;
BEGIN
    IF v_tenant IS NULL THEN
        RAISE EXCEPTION 'org_delete_tenant: no tenant in the transaction' USING ERRCODE = 'PRO02';
    END IF;
    SELECT t.slug, t.deleted_at INTO v_slug, v_deleted FROM tenants AS t WHERE t.id = v_tenant FOR UPDATE;
    IF NOT FOUND OR v_deleted IS NOT NULL THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM memberships AS m JOIN users AS u ON u.id = m.user_id
        WHERE m.tenant_id = v_tenant AND m.user_id = p_actor AND m.role = 'owner' AND u.status = 'active'
    ) THEN
        RAISE EXCEPTION 'org_delete_tenant: actor is not an owner' USING ERRCODE = 'PRO02';
    END IF;
    IF v_slug = 'default' THEN
        RAISE EXCEPTION 'org_delete_tenant: the default tenant cannot be deleted' USING ERRCODE = 'PRO01';
    END IF;
    UPDATE tenants SET deleted_at = now(), status = 'suspended', updated_at = now() WHERE id = v_tenant;
    INSERT INTO audit_log (id, tenant_id, actor_user_id, action, entity_type, entity_id, metadata, ip)
    VALUES (p_audit_id, v_tenant, p_actor, 'organization.deleted', 'organization', v_tenant::text, '{}'::jsonb, p_ip);
    RETURN true;
END
$function$
CREATE OR REPLACE FUNCTION public.outbox_pending()
 RETURNS bigint
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT count(*) FROM notification_deliveries WHERE status = 'pending'
$function$
CREATE OR REPLACE FUNCTION public.purge_email_verifications()
 RETURNS integer
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    WITH gone AS (
        DELETE FROM email_verifications AS v
        WHERE v.token_hash IN (
            SELECT e.token_hash FROM email_verifications AS e
            WHERE e.expires_at < now() - interval '1 day'
            ORDER BY e.expires_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$function$
CREATE OR REPLACE FUNCTION public.purge_expired_sessions()
 RETURNS integer
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    WITH d AS (DELETE FROM sessions WHERE expires_at < now() RETURNING 1)
    SELECT count(*)::integer FROM d
$function$
CREATE OR REPLACE FUNCTION public.purge_mail_outbox(p_keep interval)
 RETURNS integer
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    WITH gone AS (
        DELETE FROM mail_outbox AS m
        WHERE m.id IN (
            SELECT e.id FROM mail_outbox AS e
            WHERE e.finished_at IS NOT NULL AND e.finished_at < now() - p_keep
            ORDER BY e.finished_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$function$
CREATE OR REPLACE FUNCTION public.purge_password_resets()
 RETURNS integer
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    WITH gone AS (
        DELETE FROM password_resets AS r
        WHERE r.token_hash IN (
            SELECT e.token_hash FROM password_resets AS e
            WHERE e.expires_at < now() - interval '1 day'
            ORDER BY e.expires_at
            LIMIT 1000
            FOR UPDATE SKIP LOCKED
        )
        RETURNING 1
    )
    SELECT count(*)::integer FROM gone
$function$
CREATE OR REPLACE FUNCTION public.reset_monitor_runtime(p_monitor_id uuid)
 RETURNS integer
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    caller_tenant uuid := NULLIF(current_setting('app.tenant_id', true), '')::uuid;
    n             integer;
BEGIN
    IF caller_tenant IS NULL THEN
        RAISE EXCEPTION 'reset_monitor_runtime: app.tenant_id is not set' USING ERRCODE = 'insufficient_privilege';
    END IF;
    UPDATE monitor_location_state AS s
    SET status = 'unknown',
        consecutive_failures = 0,
        lease_token = NULL,
        lease_until = NULL,
        next_run_at = CASE WHEN m.type = 'http' THEN now() + random() * interval '30 seconds'
                           ELSE 'infinity'::timestamptz END
    FROM monitors AS m
    WHERE m.tenant_id = caller_tenant AND m.id = p_monitor_id AND m.deleted_at IS NULL
      AND s.tenant_id = m.tenant_id AND s.monitor_id = m.id;
    GET DIAGNOSTICS n = ROW_COUNT;
    RETURN n;
END
$function$
CREATE OR REPLACE FUNCTION public.status_page_published_pages()
 RETURNS TABLE(tenant_id uuid, page_id uuid)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT p.tenant_id, p.id
    FROM status_pages AS p
    JOIN tenants AS t ON t.id = p.tenant_id
    WHERE p.published AND p.deleted_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
    ORDER BY p.tenant_id, p.id
$function$
CREATE OR REPLACE FUNCTION public.status_page_slug_held(p_slug text)
 RETURNS boolean
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT EXISTS (
        SELECT 1 FROM status_page_released_slugs AS r
        WHERE r.slug = p_slug
          AND r.tenant_id IS DISTINCT FROM NULLIF(current_setting('app.tenant_id', true), '')::uuid
          AND r.released_at > now() - interval '30 days')
$function$
CREATE OR REPLACE FUNCTION public.status_page_snapshot(p_slug text)
 RETURNS TABLE(payload jsonb, etag text, generated_at timestamp with time zone, password_hash text)
 LANGUAGE sql
 STABLE SECURITY DEFINER
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT s.payload, s.etag, s.generated_at, p.password_hash
    FROM status_pages AS p
    JOIN status_page_snapshots AS s ON s.tenant_id = p.tenant_id AND s.page_id = p.id AND s.slug = p.slug
    JOIN tenants AS t ON t.id = p.tenant_id
    WHERE p.slug = p_slug
      AND p.published AND p.deleted_at IS NULL
      AND t.status = 'active' AND t.deleted_at IS NULL
$function$
CREATE OR REPLACE FUNCTION public.tenants_create_default_project()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'public', 'pg_temp'
AS $function$
DECLARE
    v_prev text := current_setting('app.tenant_id', true);
BEGIN
    PERFORM set_config('app.tenant_id', NEW.id::text, true);
    INSERT INTO projects (id, tenant_id, name, slug, is_default, all_members)
    VALUES (uuidv7_now(), NEW.id, 'Default', 'default', true, true);
    PERFORM set_config('app.tenant_id', coalesce(v_prev, ''), true);
    RETURN NULL;
END
$function$
CREATE OR REPLACE FUNCTION public.uuidv7_now()
 RETURNS uuid
 LANGUAGE sql
 SET search_path TO 'public', 'pg_temp'
AS $function$
    SELECT encode(set_bit(set_bit(overlay(uuid_send(gen_random_uuid())
               PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
               FROM 1 FOR 6), 52, 1), 53, 1), 'hex')::uuid
$function$
