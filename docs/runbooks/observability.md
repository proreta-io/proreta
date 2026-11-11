# Observability: metrics, logs and suggested alerts

Every process (`api`, `worker`, `jobs`) serves Prometheus metrics at `GET /metrics` on the admin port (`PRORETA_ADMIN_ADDR`, default `:9090`), next to `/healthz` and `/readyz`. The admin port is never routed through the gateway. The chart can create a `ServiceMonitor` per component (`serviceMonitor.enabled`, needs the Prometheus Operator CRDs); the alert rules below are suggestions written as text, not shipped.

## Label policy
No metric is labelled per organization, monitor, incident, user, channel, URL, host, path, email, IP or token (`channel_type` is the bounded channel type, never a channel): label cardinality would grow with customers and the values would leak identifiers and URLs into the metrics stack. HTTP metrics use the chi route template (`/api/v1/monitors/{monitorId}`), never the raw path. `TestNoHighCardinalityOrSecretLabels` (`internal/platform/metrics`) scans the label names of every registered metric and fails on a forbidden one; `TestMetricsAreDocumented` fails when a `proreta_*` metric is missing from this page.

## Metrics

### All processes
| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `proreta_build_info` | gauge (1) | `service`, `version`, `commit` (constant) | Which binary runs |
| `go_*`, `process_*` | various | | Go runtime and process collectors |

### api
| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `proreta_http_requests_total` | counter | `route`, `method`, `code` | Requests by route template and status; `route="unmatched"` for paths no route matched; `method` is one of `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`, else `other` |
| `proreta_http_request_duration_seconds` | histogram | `route`, `method` | Request latency |
| `proreta_auth_logins_total` | counter | `result` (`succeeded`, `failed`, `suspended`, `rate_limited`) | Login attempts; `suspended` is a correct password for an organization the platform admin suspended or deleted |
| `proreta_mail_enqueued_total` | counter | `kind` (message kind) | Messages queued in the mail outbox; registered by the api, counts only once email is enabled in the admin panel (the test message included) |
| `proreta_auth_registrations_total` | counter | `result` (`succeeded`, `invalid`, `invalid_invitation`, `invalid_credentials`, `email_taken`, `rate_limited`) | Self-service registrations and registrations through an invitation link (`invalid_invitation` is an unknown, used, revoked or expired token, `invalid_credentials` a wrong password for an existing account joining through a link); a burst of `succeeded` while registration is open is the signal for the kill switch |
| `proreta_auth_password_changes_total` | counter | `result` (`succeeded`, `invalid`, `wrong_password`, `rate_limited`) | Password changes by signed-in users (`invalid` breaks the password policy, `wrong_password` is a wrong current password); a burst of `wrong_password` from one session is someone guessing the password of an open session |
| `proreta_auth_oauth_logins_total` | counter | `provider` (`google`, `github`), `result` (`succeeded`, `linked`, `error`, or the error code of the callback redirect such as `state_invalid`, `not_invited`, `account_exists`, `email_unverified`, `failed`) | OAuth sign-ins and links; many `state_invalid` or `failed` point at forged callbacks or a misconfigured client (redirect URI, secret) |
| `proreta_heartbeat_pings_total` | counter | `result` (`accepted`, `ignored`, `unknown`, `rate_limited`, `error`) | Pings of the public heartbeat endpoint; `ignored` is a known token of a paused or deleted heartbeat, `unknown` a token nobody has |
| `proreta_admin_actions_total` | counter | `action` (`admin.organization.plan_changed`, `admin.organization.suspended`, `admin.organization.unsuspended`, `admin.organization.deleted`, `admin.user.password_reset`, `admin.user.disabled`, `admin.user.enabled`, `admin.user.sessions_revoked`, `admin.member.added`, `admin.member.role_changed`, `admin.member.removed`, `admin.invitation.created`, `admin.invitation.revoked`, `admin.project.created`, `admin.project.updated`, `admin.project.deleted`, `admin.project_member.added`, `admin.project_member.updated`, `admin.project_member.removed`, `admin.email_settings.updated`, `admin.email_test.sent`, `admin.registration_settings.updated`) | Changes made in the platform admin panel; each also has an audit row in the target organization. Any increase outside planned admin work deserves a look at the audit log |

### worker
| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `proreta_worker_checks_total` | counter | `result` (`ok`, `failed`) | Checks run and recorded for writing |
| `proreta_worker_check_duration_seconds` | histogram | | One check including redirects |
| `proreta_worker_scheduler_lag_seconds` | histogram | | Claim time minus `next_run_at` |
| `proreta_worker_stale_results_total` | counter | | Results discarded because their lease expired or was taken over |
| `proreta_worker_lock_timeouts_total` | counter | | Per-organization result batches discarded after the 2 s `lock_timeout` |
| `proreta_worker_apply_errors_total` | counter | | Results kept without a state-machine update (permanent error) |
| `proreta_worker_cancelled_checks_total` | counter | | Checks abandoned at shutdown and given back |
| `proreta_worker_write_errors_total` | counter | | Results that could not be written |
| `proreta_worker_claim_errors_total` | counter | | Failed `claim_due_monitors` calls |
| `proreta_worker_deferred_jobs_total` | counter | | Claimed jobs given back because their organization or host was at its limit |
| `proreta_worker_prober_panics_total` | counter | | Recovered prober panics |
| `proreta_worker_ensure_partitions_errors_total` | counter | | Failed `ensure_partitions()` calls of the worker |

### jobs
| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `proreta_jobs_deliveries_total` | counter | `channel_type` (`slack_webhook`, or `unknown` when the channel could not be read), `result` (`sent`, `retry`, `failed`) | Delivery outcomes recorded by `finish_delivery` |
| `proreta_jobs_delivery_duration_seconds` | histogram | `channel_type` | One send to a notification channel |
| `proreta_jobs_outbox_pending` | gauge | | Outbox rows waiting (including retries), all organizations; refreshed every 15 s |
| `proreta_jobs_claim_errors_total` | counter | | Failed `claim_deliveries` calls |
| `proreta_jobs_mail_total` | counter | `kind` (message kind), `result` (`sent`, `retry`, `failed`) | Outgoing email attempts recorded by `finish_mail`; only once an SMTP server is configured in the admin panel |
| `proreta_jobs_mail_duration_seconds` | histogram | | One SMTP delivery |
| `proreta_jobs_mail_pending` | gauge | | Mail outbox rows waiting (including retries); refreshed every 15 s |
| `proreta_jobs_mail_claim_errors_total` | counter | | Failed `claim_mail` calls |
| `proreta_jobs_housekeeping_runs_total` | counter | `task`, `result` (`ok`, `skipped`, `error`) | Housekeeping runs; `task` is `ensure_partitions`, `compute_rollups`, `drop_expired_partitions` (the retention task, implemented by `list_droppable_partitions` / `detach_expired_partition` / `drop_detached_partition`), `purge_expired_sessions`, `purge_mail_outbox` (hourly), `purge_email_verifications` and `purge_password_resets` (daily), `evaluate_heartbeats` (every 30 s), `enqueue_incident_reminders` (every minute) or `rebuild_status_pages` (every 30 s) |
| `proreta_jobs_housekeeping_last_success_timestamp_seconds` | gauge | `task` | Unix time of the last successful run of each task |
| `proreta_jobs_partitions_kept_total` | counter | `reason` (`rollups_incomplete`, `error`) | Expired `check_results` partitions the retention task kept back; each one is also logged at warn level with the partition name |
| `proreta_jobs_heartbeats_missed_total` | counter | | Missed heartbeat deadlines applied as failures by `evaluate_heartbeats` (one per deadline; a heartbeat that stays silent is not counted again) |
| `proreta_jobs_status_page_snapshots_total` | counter | | Status page snapshots rebuilt and stored by `rebuild_status_pages` (one per published page and run); a failing page is logged at error level with its organization and page id |

### statuspage
The public renderer also has `proreta_http_requests_total` and `proreta_http_request_duration_seconds` with route templates such as `/status/{slug:[a-z0-9-]+}` (never the slug itself).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `proreta_statuspage_cache_lookups_total` | counter | `result` (`hit`, `miss`) | Snapshot lookups; a miss calls `status_page_snapshot` (cache 15 s per slug and replica, unknown slugs included) |
| `proreta_statuspage_rate_limited_total` | counter | `kind` (`view`, `unlock`) | Requests refused with 429: page views per client IP (60 burst, 2/s), password attempts per IP (10 per 15 min) and per page (100 per 15 min) |
| `proreta_statuspage_unlock_attempts_total` | counter | `result` (`ok`, `wrong`) | Password attempts on protected pages |
| `proreta_statuspage_snapshot_errors_total` | counter | | Lookups that failed (database or decoding); the visitor gets the 404 page |

## Logs
All processes write one JSON object per line to stdout (`log/slog`, level from `PRORETA_LOG_LEVEL`). Every line has `time`, `level`, `msg`, `service` and `version`.

Request log (api, `msg="http request"`, one per request):

| Field | Content |
|---|---|
| `request_id` | The incoming `X-Request-Id` when it matches `[A-Za-z0-9._-]{1,128}`, otherwise a new UUIDv7; returned in the `X-Request-Id` response header and in problem bodies (`requestId`) |
| `tenant_id` | Set once the session is authenticated (and by a successful login); absent for anonymous requests |
| `method` | HTTP method |
| `route` | Route template, `unmatched` when none matched |
| `status`, `bytes` | Response status and body size |
| `duration_ms` | Handler duration in milliseconds |

Other lines of a request carry the same `request_id` and `tenant_id`. The worker and jobs log ids (`tenant_id`, `monitor_id`, `location_id`, `delivery_id`, `channel_id`) and error kinds.

Never logged, by design and by test (`TestIntegrationRequestLogsCarryNoSecrets` in `internal/api`, the log assertions of `internal/e2e` and of the worker and jobs tests): passwords, session tokens, CSRF tokens, monitor URLs (they can carry credentials in the query), Slack webhook URLs (rejected values included), raw paths and query strings, request and response bodies, and raw errors of the SSRF guard (they contain resolved addresses and the URL; only error kinds such as `blocked_address` or `timeout` are logged and stored).

## Suggested alert rules (not deployed)
Written in PromQL for any Prometheus-compatible stack with Alertmanager; thresholds are first guesses.

| Alert | Expression (PromQL) | For | Why |
|---|---|---|---|
| ChecksStalled | `sum(rate(proreta_worker_checks_total[5m])) == 0` and at least one active monitor | 5m | Nothing is being checked |
| SchedulerLagHigh | `histogram_quantile(0.95, sum(rate(proreta_worker_scheduler_lag_seconds_bucket[10m])) by (le)) > 30` | 10m | Checks start late: worker saturated or claims slow |
| ResultsDiscarded | `sum(rate(proreta_worker_stale_results_total[15m])) + sum(rate(proreta_worker_lock_timeouts_total[15m])) > 0.1` | 15m | Results are thrown away (lease expiry, lock contention) |
| WorkerErrors | `increase(proreta_worker_write_errors_total[15m]) + increase(proreta_worker_claim_errors_total[15m]) + increase(proreta_worker_apply_errors_total[15m]) + increase(proreta_worker_prober_panics_total[15m]) > 0` | 0m | Any write, claim, apply or prober failure needs a look |
| PartitionsFailing | `increase(proreta_worker_ensure_partitions_errors_total[2h]) > 1` or `time() - proreta_jobs_housekeeping_last_success_timestamp_seconds{task="ensure_partitions"} > 2*86400` | 0m | Without future partitions result inserts fail at the next month |
| HousekeepingStale | `time() - proreta_jobs_housekeeping_last_success_timestamp_seconds{task="compute_rollups"} > 1800` (and for `drop_expired_partitions`, `purge_expired_sessions`, `evaluate_heartbeats` and `enqueue_incident_reminders`: `> 2*86400`, `> 3*3600`, `> 300`, `> 600`) | 0m | A singleton task stopped succeeding; for `evaluate_heartbeats` it means missed heartbeats open no incident and pings resolve none; for `enqueue_incident_reminders` no reminders are sent |
| RetentionErrors | `increase(proreta_jobs_housekeeping_runs_total{task="drop_expired_partitions",result="error"}[1d]) > 2` | 0m | The detach keeps hitting its lock timeout |
| PartitionsKept | `increase(proreta_jobs_partitions_kept_total{reason="rollups_incomplete"}[2d]) > 0` | 0m | An expired month still has hours without rollups: older than the 7-day catch-up, needs a manual `compute_rollups(from, to)` |
| OutboxBacklog | `proreta_jobs_outbox_pending > 50` | 10m | Alerts are not leaving: Slack down, rate limits or jobs stuck |
| MailBacklog | `proreta_jobs_mail_pending > 20` | 10m | Email is not leaving (only once an SMTP server is configured in the admin panel): server down, rate limits or jobs stuck |
| MailFailing | `increase(proreta_jobs_mail_total{result="failed"}[1h]) > 0` | 0m | A message was given up: rejected recipient, or five failed attempts. See [outgoing email](email.md) |
| DeliveriesFailing | `sum(rate(proreta_jobs_deliveries_total{result="failed"}[1h])) > 0` | 0m | A delivery gave up after five attempts or its channel is unusable |
| APIErrors | `sum(rate(proreta_http_requests_total{code=~"5.."}[5m])) / sum(rate(proreta_http_requests_total[5m])) > 0.05` | 10m | api failing |
| APILatency | `histogram_quantile(0.95, sum(rate(proreta_http_request_duration_seconds_bucket{route!="unmatched"}[10m])) by (le, route)) > 1` | 15m | Slow dashboard reads |
| LoginAbuse | `sum(rate(proreta_auth_logins_total{result="rate_limited"}[15m])) > 0.2` | 15m | Password guessing or a lockout |
| HeartbeatGuessing | `sum(rate(proreta_heartbeat_pings_total{result=~"unknown\|rate_limited"}[15m])) > 0.2` | 15m | Someone is guessing heartbeat tokens, or a sender keeps an old URL after a regenerate |
| RegistrationBurst | `sum(increase(proreta_auth_registrations_total{result="succeeded"}[1h])) > 20` | 0m | Mass sign-ups; consider `config.registrationEnabled: false` |
