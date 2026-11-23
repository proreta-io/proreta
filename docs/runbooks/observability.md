# Observability: metrics, logs and suggested alerts

Every process (`api`, `worker`, `jobs`) serves Prometheus metrics at `GET /metrics` on the admin port (`PRORETA_ADMIN_ADDR`, default `:9090`), next to `/healthz` and `/readyz`. The admin port is never routed through the gateway. The chart can create a `ServiceMonitor` per component (`serviceMonitor.enabled`, needs the Prometheus Operator CRDs); six of the alert rules below ship as a ready-made rules file ([self-monitoring](self-monitoring.md)); the rest are suggestions written as text.

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
| `proreta_admin_actions_total` | counter | `action` (`admin.organization.plan_changed`, `admin.organization.suspended`, `admin.organization.unsuspended`, `admin.organization.deleted`, `admin.user.password_reset`, `admin.user.disabled`, `admin.user.enabled`, `admin.user.sessions_revoked`, `admin.user.platform_admin_granted`, `admin.user.platform_admin_revoked`, `admin.member.added`, `admin.member.role_changed`, `admin.member.removed`, `admin.invitation.created`, `admin.invitation.revoked`, `admin.project.created`, `admin.project.updated`, `admin.project.deleted`, `admin.project_member.added`, `admin.project_member.updated`, `admin.project_member.removed`, `admin.email_settings.updated`, `admin.email_test.sent`, `admin.registration_settings.updated`, `admin.retention_settings.updated`, `admin.plan.created`, `admin.plan.updated`, `admin.plan.deleted`) | Changes made in the platform admin panel; each also has an audit row in the target organization. Any increase outside planned admin work deserves a look at the audit log |

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
| `proreta_jobs_deliveries_total` | counter | `channel_type` (`slack_webhook`, `webhook`, or `unknown` when the channel could not be read), `result` (`sent`, `retry`, `failed`) | Delivery outcomes recorded by `finish_delivery` |
| `proreta_specula_late_alerts_total` | counter | `outcome` (`sent`, `skipped_resolved`, `failed`) | Deliveries written for replayed (late) results, by final outcome, served by `jobs` (the process that sends them). `skipped_resolved` is a late "down" not sent because its incident was already over, as its "recovered" tells the story; `failed` is any other delivery that gave up. Retries are not counted. No per-monitor or per-token label |
| `proreta_jobs_delivery_duration_seconds` | histogram | `channel_type` | One send to a notification channel |
| `proreta_jobs_outbox_pending` | gauge | | Outbox rows waiting (including retries), all organizations; refreshed every 15 s |
| `proreta_jobs_claim_errors_total` | counter | | Failed `claim_deliveries` calls |
| `proreta_jobs_mail_total` | counter | `kind` (message kind), `result` (`sent`, `retry`, `failed`) | Outgoing email attempts recorded by `finish_mail`; only once an SMTP server is configured in the admin panel |
| `proreta_jobs_mail_duration_seconds` | histogram | | One SMTP delivery |
| `proreta_jobs_mail_pending` | gauge | | Mail outbox rows waiting (including retries); refreshed every 15 s |
| `proreta_jobs_mail_claim_errors_total` | counter | | Failed `claim_mail` calls |
| `proreta_jobs_purged_rows_total` | counter | `table` (`notification_deliveries`, `result_rollups_hourly`) | Rows deleted by the retention purge tasks (finished deliveries older than the delivery retention, hourly rollups older than the rollup retention; both set under Admin → Retention). A run deletes in batches of 1000, at most 200 batches, and continues at the next run, so a large backlog shows as a steady rate until it is gone. The audit log and incidents are never purged |
| `proreta_jobs_housekeeping_runs_total` | counter | `task`, `result` (`ok`, `skipped`, `error`) | Housekeeping runs; `task` is `ensure_partitions`, `compute_rollups`, `drop_expired_partitions` (the retention task, implemented by `list_droppable_partitions` / `detach_expired_partition` / `drop_detached_partition`), `purge_expired_sessions`, `purge_mail_outbox` and `purge_notification_deliveries` (hourly), `purge_result_rollups` (every 6 hours), `purge_email_verifications` and `purge_password_resets` (daily), `evaluate_heartbeats` (every 30 s), `enqueue_incident_reminders` (every minute), `check_status_page_domains` (every minute, only does work with `PRORETA_STATUS_PAGE_CUSTOM_DOMAINS` on) or `rebuild_status_pages` (every 30 s) |
| `proreta_jobs_housekeeping_last_success_timestamp_seconds` | gauge | `task` | Unix time of the last successful run of each task |
| `proreta_jobs_partitions_kept_total` | counter | `reason` (`rollups_incomplete`, `error`) | Expired `check_results` partitions the retention task kept back; each one is also logged at warn level with the partition name |
| `proreta_jobs_rollup_dirty_hours` | gauge | | Hours that late results (replayed by a Specula) marked for recomputation and the rollup task has not yet recomputed; normally 0 |
| `proreta_jobs_rollup_dirty_hours_processed_total` | counter | | Marked hours recomputed by the `compute_rollups` task (six per run); retention keeps a month with a marked hour until it is recomputed |
| `proreta_jobs_specula_agents` | gauge | `status` | Specula by status: `pending`, `online`, `offline`, `revoked`, all organizations |
| `proreta_jobs_specula_incidents_total` | counter | `event` | Specula incidents opened (`offline`) and resolved (`online`) by `specula_mark_unreachable` |
| `proreta_jobs_heartbeats_missed_total` | counter | | Missed heartbeat deadlines applied as failures by `evaluate_heartbeats` (one per deadline; a heartbeat that stays silent is not counted again) |
| `proreta_jobs_status_page_snapshots_total` | counter | | Status page snapshots rebuilt and stored by `rebuild_status_pages` (one per published page and run); a failing page is logged at error level with its organization and page id |
| `proreta_status_page_domains` | gauge | `state` (`pending`, `verified`, `active`, `failed`) | Status page domains by state, all organizations, released ones excluded; refreshed by every run of `check_status_page_domains` (zero while the feature is off) |
| `proreta_status_page_domains_checks_total` | counter | `result` (`ok`, `txt-missing`, `cname-missing`, `txt-mismatch`, `dns-error`) | DNS checks of status page domains. `dns-error` is a timeout, SERVFAIL or any other failed lookup: it never counts toward `failed`. `txt-mismatch` is a claim on a host another organization holds. No host names in labels |

### speculagw
The Specula gateway (`/specula/v1`, admin port of the `speculagw` process). Limits are per replica, so with N replicas a token may use up to N times its budget.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `proreta_specula_unauthorized_total` | counter | | Requests with a missing, unknown or revoked token |
| `proreta_specula_rate_limited_total` | counter | | Requests refused with 429 because the client address used up its budget of failed token checks, or the replica's concurrent token lookups were all busy |
| `proreta_specula_token_limited_total` | counter | `endpoint` (`hello`, `schedule`, `reports`) | Requests of a valid token refused with 429 and `Retry-After`: `reports` 5 per second (burst 20), `schedule` one per 10 seconds (burst 3), `hello` 2 per minute (burst 2). An executor in normal operation never reaches them; a steady rate is a misbehaving or duplicated executor |
| `proreta_specula_jobs_claimed_total` | counter | | Jobs handed to executors in pull mode |
| `proreta_specula_results_total` | counter | `outcome` (`recorded`, `stale`, `rejected`) | Pull-mode results (`/results`) by outcome |
| `proreta_specula_reports_total` | counter | `outcome` (`recorded`, `duplicate`, `superseded`, `expired`, `rejected`) | Slot-addressed results (`/reports`) by outcome. `duplicate` is a result resent after a lost answer; `superseded` is a result of a schedule epoch that has ended; `expired` is older than 24 hours; `rejected` is malformed, off the grid, in the future or not this Specula's monitor |
| `proreta_specula_report_age_seconds` | histogram | | Age of a recorded report when it arrived (receive time minus slot): close to zero in normal operation, the depth of the backlog during a replay |
| `proreta_specula_late_reports_total` | counter | | Recorded reports older than `PRORETA_SPECULA_LATE_ALERT_AFTER` (default 10 minutes) when they arrived; alerts they raise say they are late |
| `proreta_specula_schedule_requests_total` | counter | `result` (`200`, `304`, `413`, `error`) | `GET /schedule` requests that passed the token budget; `413` is a location with more than 5000 monitors |
| `proreta_specula_schedule_build_seconds` | histogram | | Time to read and decrypt a Specula's schedule: one sample per `GET /schedule`, and one per `hello` or `reports` call that finds no schedule version cached (the version is cached for 5 seconds) |
| `proreta_specula_schedule_unreadable_monitors_total` | counter | | Monitors left out of a built schedule because their sealed request cannot be opened (wrong or missing request keys); counted at every build, so any increase means a monitor is not being checked, and the log names it |
| `proreta_specula_duplicate_instance_total` | counter | | `hello` calls whose instance id differs from the stored one while the stored one said hello less than 3 minutes ago: two executors use one token (a misconfigured second replica). The Specula page shows a badge; a restart keeps its id and a replaced pod usually comes later, so neither counts. A steady increase means both executors are still running |
| `proreta_specula_slot_losses_total` | counter | `kind` (`missed`, `dropped`), `reason` | Slots the executors reported as never checked (`missed`: `busy`, `late`, `scheduleExpired`) or results they gave up on (`dropped`: `bufferSize`, `bufferAge`, `rejected`, `superseded`, `tooOld`, `unreadable`), as the growth between two hellos of the same process; only executors that announce `schedule/2` report. Series appear with the first loss. A documented alert rather than a default one: `increase(proreta_specula_slot_losses_total{reason=~"busy\|late\|scheduleExpired\|bufferSize\|rejected\|tooOld\|unreadable"}[15m]) > 0`; see [Missed and lost checks](specula.md#missed-and-lost-checks) |
| `proreta_specula_slot_counters_ignored_total` | counter | `reason` | Slot loss counters of a `hello` that were outside 0 to 1e15 and were left out (the rest of the hello still counts); any increase means an executor sends bad values |

### specula (executor)
The executor serves metrics on its own admin port (`PRORETA_ADMIN_ADDR`, default `:9090`) in the customer's network. Their names start with `specula_`, not `proreta_`. They carry no per-monitor or per-token label.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `specula_buffer_results` | gauge | | Results waiting to be reported |
| `specula_buffer_bytes` | gauge | | Bytes the buffer holds on disk (0 in memory-only mode) |
| `specula_buffer_oldest_age_seconds` | gauge | | Age of the oldest waiting result; 0 when none waits. Grows while the link is down and during a replay; the oldest result is looked up at most every 15 s |
| `specula_buffer_degraded` | gauge | | 1 when results are buffered in memory only (state directory missing or unusable) |
| `specula_results_dropped_total` | counter | `reason` (`size`, `age`, `memory`, `unreadable`, `refused`) | Results given up on: a buffer limit, an unreadable segment, or refused by the gateway as a whole. Any increase is a gap |
| `specula_buffer_corrupt_records_total` | counter | | Damaged buffer records skipped plus segments set aside |
| `specula_schedule_loaded` | gauge | | 1 when a schedule is in use, 0 while none was fetched yet (alert on 0 for "never fetched") |
| `specula_schedule_age_seconds` | gauge | | Time since the schedule in use was issued; absent while there is none |
| `specula_schedule_expired_monitors` | gauge | | Monitors not checked because the schedule expired; 0 normally |
| `specula_missed_slots_total` | counter | `reason` (`busy`, `late`, `expired`) | Slots without a check: the previous check still ran, the executor was stalled for a whole interval, or the schedule had expired |
| `specula_slot_lateness_seconds` | histogram | | How late a check started after its slot |
| `specula_replay_inflight` | gauge | | Results in the batch being reported now; 0 when idle |
| `specula_clock_offset_seconds` | gauge | | Proreta's time minus the local time at the last hello |

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

Never logged, by design and by test (`TestIntegrationRequestLogsCarryNoSecrets` in `internal/api`, the log assertions of `internal/e2e` and of the worker and jobs tests): passwords, session tokens, CSRF tokens, monitor URLs (they can carry credentials in the query), Slack and webhook URLs (rejected values included), raw paths and query strings, request and response bodies, and raw errors of the SSRF guard (they contain resolved addresses and the URL; only error kinds such as `blocked_address` or `timeout` are logged and stored).

## Suggested alert rules
Written in PromQL for any Prometheus-compatible stack with Alertmanager; thresholds are first guesses. The repository ships six of them as `deploy/helm/proreta/alerts/proreta-rules.yaml` (`ProretaTargetDown`, `ProretaChecksStalled`, `ProretaHousekeepingStale` for `compute_rollups`, `evaluate_heartbeats`, `ensure_partitions` and `drop_expired_partitions`, `ProretaOutboxBacklog`, `ProretaDeliveriesFailing` and `ProretaApiErrors`, with slightly different windows than the table; see [self-monitoring](self-monitoring.md)). The rest of the table stays a list of suggestions that nothing deploys.

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
| DomainChecksStale | `time() - proreta_jobs_housekeeping_last_success_timestamp_seconds{task="check_status_page_domains"} > 3600` (only where custom domains are on) | 0m | Status page domains are not being verified: nothing expires, fails or recovers |
| DomainChecksDNSErrors | `sum(rate(proreta_status_page_domains_checks_total{result="dns-error"}[30m])) / sum(rate(proreta_status_page_domains_checks_total[30m])) > 0.5` | 30m | Most lookups fail: the resolver (`PRORETA_DNS_RESOLVER` or the system's) is down or blocked from jobs. Failures are not counted meanwhile, so no domain fails because of it |
| DomainsFailedJump | `delta(proreta_status_page_domains{state="failed"}[1h]) > 5` | 0m | Many domains lost their DNS proof at once: look at the resolver and at the domains before blaming the customers |
| OutboxBacklog | `proreta_jobs_outbox_pending > 50` | 10m | Alerts are not leaving: Slack down, rate limits or jobs stuck |
| MailBacklog | `proreta_jobs_mail_pending > 20` | 10m | Email is not leaving (only once an SMTP server is configured in the admin panel): server down, rate limits or jobs stuck |
| MailFailing | `increase(proreta_jobs_mail_total{result="failed"}[1h]) > 0` | 0m | A message was given up: rejected recipient, or five failed attempts. See [outgoing email](email.md) |
| DeliveriesFailing | `sum(rate(proreta_jobs_deliveries_total{result="failed"}[1h])) > 0` | 0m | A delivery gave up after five attempts or its channel is unusable |
| APIErrors | `sum(rate(proreta_http_requests_total{code=~"5.."}[5m])) / sum(rate(proreta_http_requests_total[5m])) > 0.05` | 10m | api failing |
| APILatency | `histogram_quantile(0.95, sum(rate(proreta_http_request_duration_seconds_bucket{route!="unmatched"}[10m])) by (le, route)) > 1` | 15m | Slow dashboard reads |
| LoginAbuse | `sum(rate(proreta_auth_logins_total{result="rate_limited"}[15m])) > 0.2` | 15m | Password guessing or a lockout |
| HeartbeatGuessing | `sum(rate(proreta_heartbeat_pings_total{result=~"unknown\|rate_limited"}[15m])) > 0.2` | 15m | Someone is guessing heartbeat tokens, or a sender keeps an old URL after a regenerate |
| RegistrationBurst | `sum(increase(proreta_auth_registrations_total{result="succeeded"}[1h])) > 20` | 0m | Mass sign-ups; consider `config.registrationEnabled: false` |
