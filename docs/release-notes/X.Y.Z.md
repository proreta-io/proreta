# Proreta X.Y.Z

The MVP release of Proreta. It is the first release with a compatibility promise: the API is frozen as 1.0 and the database starts from a single baseline.

Earlier releases (0.1.0 to 0.4.0) were test releases. **They cannot be upgraded in place: install X.Y.Z as a new installation** (see [Breaking changes](#breaking-changes-and-what-to-do)).

Install and upgrade: [docs/install.md](../install.md). Images: `ghcr.io/proreta-io/proreta-backend:X.Y.Z` and `ghcr.io/proreta-io/proreta-web:X.Y.Z` (`amd64` and `arm64`). Charts: `oci://ghcr.io/proreta-io/charts/proreta` and `oci://ghcr.io/proreta-io/charts/proreta-specula`, version X.Y.Z.

## What is new since 0.4.0

### Specula works on its own
A Specula (the executor that checks services inside your own network, [runbook](../runbooks/specula.md)) no longer asks Proreta for every check.
- It fetches its schedule and keeps running from a cached copy when the link to Proreta drops. Results are buffered on disk (`PRORETA_SPECULA_BUFFER_MAX_MIB`, `PRORETA_SPECULA_BUFFER_MAX_AGE`) and replayed when the link returns.
- Alerts raised from replayed results say they were reported **late**. The threshold is `PRORETA_SPECULA_LATE_ALERT_AFTER` (chart value `speculagw.lateAlertAfter`, default 10m); webhook templates get the placeholders `late` and `observedAt`.
- The executor reports clock skew, duplicate instances and its state; the Specula page in the web app shows them. New metrics for the executor and the gateway, per-token limits and a cached schedule on the gateway side.
- The `proreta-specula` chart runs a single replica by default (was 2), keeps its schedule cache and buffer in an `emptyDir` (`state.*`) and can create a `ServiceMonitor` (`serviceMonitor.*`).
- New alert events `agent_offline` and `agent_online` for a Specula going away and coming back (webhook and Slack channels; see [webhook channels](../runbooks/alerts-webhook.md)).
- The gateway accepts a result's check time only within one minute of now, and bounds the cost of unauthenticated traffic.
- `PRORETA_SPECULA_CONCURRENCY` now defaults to 50 (was 20).

### TCP monitors
A monitor can check that a host accepts connections on a port, from Proreta's cloud or from a Specula ([docs](../tcp-monitors.md)). A Specula runs TCP monitors only if its executor announces the capability.

### Platform admin panel
- Create an organization from the panel, with its owner's email.
- Manage every setting of an organization from its page: plan and usage, Google Workspace sign-in domains, channels and status pages (read-only), Specula executors (register, rename, share, revoke).
- Create, edit and delete plans under **Admin → Plans**; a self-hosted installation has one seeded plan, `internal`, with no limits and every feature.
- A data retention setting, a list of all status page domains with an abuse switch, and one place per person for their roles in every project.
- Details: [admin panel runbook](../runbooks/admin-panel.md).

### Status pages on customers' own domains
A customer proves control of a host name in DNS and serves a status page at the root of that host, with automatic verification, an operator command (`proretactl status-domains list`), a TLS permission endpoint for an on-demand-TLS proxy and an abuse link. Off by default (`PRORETA_STATUS_PAGE_CUSTOM_DOMAINS`, chart `statuspage.customDomains.*`). Runbook: [custom status page domains](../runbooks/custom-domains.md).

### Alerts
- Webhook channels with a message template of your own, with a live preview and JSON escaping ([runbook](../runbooks/alerts-webhook.md)); available since 0.4.0, extended with the late placeholders above.
- A `429` from a Slack or webhook endpoint no longer uses up one of the five delivery attempts: it is retried after `Retry-After` (at least 1 s, at most 10 min, with jitter), and a delivery still throttled after one hour is marked failed.
- Reminders and the resolved message follow the alert that was opened; unsuspending a monitor restarts heartbeat waits.

### Monitors, lists and the web app
- `GET /api/v1/monitors` and `GET /api/v1/members` use cursor pagination (`limit` 1 to 200, default 100, `nextCursor`). The monitor list takes `q` and `status` and is ordered by the server: down first, then active, then paused.
- Days without data show as "no data", not as downtime. Roles, the account menu and the organization switcher are reorganised; status page components are picked with a search-as-you-type field.

### Security and robustness
- `Strict-Transport-Security` on the API and the web app; `X-Forwarded-Proto` and `X-Forwarded-For` are counted from the right, so a client cannot choose its address by sending extra entries.
- Registering the configured admin login is refused; a session that must change its password cannot link an OAuth account.
- A host with several addresses is dialled with a share of the time budget per address, so a blackholed address does not hide a working one.
- Base images are pinned by digest; CI runs `govulncheck` and `shellcheck`.

### Documentation
A database backup section in the [install guide](../install.md#back-up-the-database), and new documentation for TCP monitors, custom status page domains and Specula autonomy (see [docs/README.md](../README.md)).

## Carried over from 0.1.0 to 0.4.0
HTTPS monitors with expected status codes, request method, headers and body ([docs](../monitor-requests.md)); incidents with the notification written in the same transaction; Slack and webhook channels; watchdog heartbeats for Prometheus Alertmanager; public and password-protected status pages with an RSS feed; organizations, projects and roles (`owner`, `maintainer`, `member`, `viewer`, [roles](../roles.md)); invitations by link or email; password, Google and GitHub sign-in with optional Workspace domain auto-join; optional outgoing email (verification, reset, invitations) set up in the admin panel; a forced password change after the first sign-in or a reset; Specula executors for private networks; a Helm chart with a database bootstrap Job.

## Breaking changes and what to do

**Reinstall.** Releases before X.Y.Z cannot be upgraded in place. The migration chain of the 0.x releases is replaced by one baseline, so a database created by 0.1.0 to 0.4.0 is not migrated. Back up what you need, uninstall, drop or replace the database, and install X.Y.Z as described in [docs/install.md](../install.md). Monitors, results, incidents and users of a test installation are not carried over; there is no import.

**The API is frozen as 1.0.** From X.Y.Z, changes to [api/openapi.yaml](../../api/openapi.yaml) are additive within 1.x, apart from operations marked `x-experimental: true` (the platform admin API). Against 0.4.0 the changes are additive for clients that read the responses, with these behaviour differences:
- The monitor and member lists return at most 100 items by default: follow `nextCursor`.
- The monitor list order is fixed by the server (down first); a client that relied on oldest-first sees a different order.
- A channel may list up to five events (was three), and error bodies naming a plan limit may now carry the limits `specula` and `statusPageDomains`: clients must accept unknown values of these enums.

**Chart and settings changes since 0.4.0** (no setting was removed):

| What | Before | Now |
|---|---|---|
| Chart value `config.trustedProxyHops` (`PRORETA_TRUSTED_PROXY_HOPS`) | `0` | `1`, for the Gateway of the chart's `HTTPRoute`. Set `0` if no proxy sits in front. With the wrong value every client shares one rate-limit bucket (0 behind a proxy) or can influence its address (too high) |
| `proreta-specula` chart `replicas` | `2` | `1`; two executors for the same Specula are reported as duplicates |
| `PRORETA_SPECULA_CONCURRENCY` | `20` | `50` |
| Specula gateway | any `checkedAt` | a check time more than one minute from now is refused |

New, all optional: `PRORETA_STATUS_PAGE_CUSTOM_DOMAINS`, `PRORETA_STATUSPAGE_CNAME_TARGET`, `PRORETA_STATUSPAGE_INSTALLATION_HOSTS`, `PRORETA_ABUSE_URL`, `PRORETA_STATUSPAGE_TLS_ASK_ADDR`, `PRORETA_STATUSPAGE_TLS_ASK_TOKEN`, `PRORETA_SPECULA_LATE_ALERT_AFTER`, `PRORETA_SPECULA_STATE_DIR`, `PRORETA_SPECULA_BUFFER_MAX_MIB`, `PRORETA_SPECULA_BUFFER_MAX_AGE`; the chart values for them are documented in `helm show values`.

**If you are still on 0.3.0 or earlier** (also covered by the reinstall): the role `admin` was renamed `maintainer` in 0.4.0, and Specula and webhook channels were added in 0.4.0.

## Known limitations
- **Gateway API only.** The chart creates an `HTTPRoute`; Ingress is not supported.
- **One PostgreSQL server per installation** (the role names are fixed), external to the chart; PostgreSQL 15 or newer. No bundled database.
- **HTTP monitors use HTTPS on port 443.** A URL with another port is refused; use a TCP monitor for other ports.
- **Custom status page domains** need a TLS-terminating proxy of your own with on-demand certificates; Proreta does not obtain certificates.
- **The platform admin API is experimental** and may change in any release.
- **Migrations are forward-only.** Take a database backup before every upgrade; never run an older image against a newer schema ([install guide](../install.md#upgrade)).

## Upgrading to later releases
Read the notes of each release, back up the database ([install guide](../install.md#back-up-the-database)), then `helm upgrade` with the new `--version`; the migration Job runs before the workloads roll.
