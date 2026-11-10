# Runbook: public status page

Organizations configure the page in the app (**Status page**, owners and admins); visitors open `https://<host>/status/<slug>`, served by the `statuspage` process (chart value `statuspage.enabled`).

## Enabling it together with the db bootstrap (preferred)
On an empty or an existing database, set `database.bootstrap.enabled: true` and `statuspage.enabled: true` in one change: the sync waves run the bootstrap (-3, creates `proreta_statuspage` and adds `statuspage_url`) and the migration (-1, grants `status_page_snapshot`) before the renderer starts (0). Then switch only the bootstrap off again. The two-step order below is equally valid.

## Enabling it in two steps on an existing installation
The renderer logs in as `proreta_statuspage`, which only `proretactl db bootstrap` creates, and reads its URL from the key `statuspage_url` of the Secret `proreta-db`. An installation bootstrapped before the status page existed has neither, so keep `statuspage.enabled: false` until both exist. The admin API, the settings page and the snapshots work without it; only the public page is missing.

1. Deploy the release as usual (the migrations create the status page tables and functions). Check: `kubectl --context <cluster-context> -n proreta logs job/proreta-migrate`.
2. Set `database.bootstrap.enabled: true` and sync once. The Job creates `proreta_statuspage`, adds `statuspage_password` and `statuspage_url` to `proreta-db` (existing keys are never rewritten; only key names are logged) and grants `EXECUTE` on `status_page_snapshot(text)`. While enabled the Job holds the database server admin login.
3. Set `database.bootstrap.enabled: false` again and `statuspage.enabled: true` in the same change; sync. With `secrets.statuspage.source: generate` (default) the secrets bootstrap Job creates the Secret `proreta-statuspage` (key `cookie_key`) first; with `externalSecret` create the key `secrets.statuspage.remoteKey` (property `cookie_key`, standard base64 of 32 random bytes) in your secret store before.
4. Verify: `kubectl --context <cluster-context> -n proreta get pods -l app.kubernetes.io/component=statuspage` is Ready; `curl -sI https://proreta.example.com/status/does-not-exist` answers 404 with a `Content-Security-Policy` header; publish a page and open it.

## Checking what the role may do
As a superuser on the database (read-only queries):
```sql
SELECT has_function_privilege('proreta_statuspage', 'status_page_snapshot(text)', 'EXECUTE');      -- true
SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
 WHERE has_table_privilege('proreta_statuspage', c.oid, 'SELECT');                                   -- 0
```

## A page shows old data
- The renderer caches 15 s per replica; jobs rebuilds every 30 s (`proreta_jobs_housekeeping_runs_total{task="rebuild_status_pages"}`, `proreta_jobs_status_page_snapshots_total`). A configuration change is rebuilt by the api at once.
- jobs logs "status page snapshot rebuild failed" with the organization and page id when one page fails; the others continue.
- A suspended or deleted organization's page answers 404 (by design).

## Timing of changes
The api rebuilds a page's snapshot with every change (and when a monitor on it is paused, resumed or deleted); the renderer caches each page 15 s per replica and answers with `max-age=30`. So publishing, unpublishing, deleting and setting or removing a password apply within about a minute, not at once: a visitor or a cache may still see the previous state until then. To take a page down urgently, unpublish it and expect up to a minute.

## Addresses
A deleted page's address, and the old address after a rename, stay reserved for the organization for 30 days (`status_page_released_slugs`); the organization may reuse them, others get "This address is not available", the same answer as for an address in use. To free one early for someone else (rare, on request of the organization that released it), delete its row as `proreta_owner` with `app.tenant_id` set to that organization.

## A visitor cannot open a protected page
- Wrong passwords and 429s are counted in `proreta_statuspage_unlock_attempts_total` and `proreta_statuspage_rate_limited_total{kind="unlock"}`: 10 attempts per client (IPv4 address or IPv6 /64) and 100 wrong passwords per page per 15 minutes, per replica. When the page budget is used up, each client still has 3 wrong passwords of its own, so a visitor with the right password gets in during an attack.
- Changing the password signs every visitor out; so does rotating the cookie key (delete the Secret `proreta-statuspage`, sync, restart the `statuspage` Deployment).
- Behind a gateway the client IP comes from `X-Forwarded-For` when `config.trustedProxyHops` is the number of proxies in front (1 for one gateway); with the default 0 the peer address is used, which behind a proxy makes every visitor share one limit.
- Limits trust `X-Forwarded-For` as the api does: requests without it all share one `unknown` bucket, and a pod inside the cluster can reach the `statuspage` Service directly (unless an enforced NetworkPolicy prevents it) and send a forged `X-Forwarded-For` to get a fresh bucket for every request. The limits therefore only hold for traffic through the gateway; treat them as a cost bound, not a protection against in-cluster clients.

## Abuse
- A page shows only what its admin published: public names, groups, states, daily bars and generated incident titles. Ask the organization's admin to rename or remove a component, or unpublish the page. As a last resort the platform admin suspends the organization (its page then answers 404).
- Request floods are limited per client IP and per replica; more replicas multiply the effective limits.

## Not available yet
Custom domains, manual incident updates and maintenance windows, subscriptions other than RSS, several pages per organization, translations and custom styling.
