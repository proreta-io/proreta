# Runbook: public status page

A page belongs to a project or to the organization; every project can have its own. Owners and maintainers of the project (or of the organization) configure a project page in the app (**Status page**); it shows only monitors of that project. An organization-level page is configured by organization owners and maintainers only and may show monitors of every project; people who hold only project roles do not see it. Pages created before projects are organization-level. Visitors open `https://<host>/status/<slug>`, served by the `statuspage` process (chart value `statuspage.enabled`).

## Enabling it together with the db bootstrap (preferred)
On an empty or an existing database, set `database.bootstrap.enabled: true` and `statuspage.enabled: true` in one change: the sync waves run the bootstrap (-3, creates `proreta_statuspage` and adds `statuspage_url`) and the migration (-1, grants `status_page_snapshot`) before the renderer starts (0). The bootstrap does not skip while `proreta-db` lacks the status page keys, so provide the admin login for this change; afterwards it may stay on and delete the admin Secret. The two-step order below is equally valid.

## Enabling it in two steps on an existing installation
The renderer logs in as `proreta_statuspage`, which only `proretactl db bootstrap` creates, and reads its URL from the key `statuspage_url` of the Secret `proreta-db`. An installation bootstrapped before the status page existed has neither, so keep `statuspage.enabled: false` until both exist. The admin API, the settings page and the snapshots work without it; only the public page is missing.

1. Deploy the release as usual (the migrations create the status page tables and functions). Check: `kubectl --context <cluster-context> -n proreta logs job/proreta-migrate`.
2. Set `database.bootstrap.enabled: true` and sync once. The Job creates `proreta_statuspage`, adds `statuspage_password` and `statuspage_url` to `proreta-db` (existing keys are never rewritten; only key names are logged) and grants `EXECUTE` on `status_page_snapshot(text)`, `status_page_domain_lookup(text)` and `status_page_domain_allowed(text)`. The admin Secret must exist for this run (the Job does not skip while the status page keys are missing).
3. Set `statuspage.enabled: true` (and, if you like, `database.bootstrap.enabled: false`) in the same change; sync. With `secrets.statuspage.source: generate` (default) the secrets bootstrap Job creates the Secret `proreta-statuspage` (key `cookie_key`) first; with `externalSecret` create the key `secrets.statuspage.remoteKey` (property `cookie_key`, standard base64 of 32 random bytes) in your secret store before.
4. Verify: `kubectl --context <cluster-context> -n proreta get pods -l app.kubernetes.io/component=statuspage` is Ready; `curl -sI https://proreta.example.com/status/does-not-exist` answers 404 with a `Content-Security-Policy` header; publish a page and open it.

## Checking what the role may do
As a superuser on the database (read-only queries):
```sql
SELECT has_function_privilege('proreta_statuspage', 'status_page_snapshot(text)', 'EXECUTE');      -- true
SELECT has_function_privilege('proreta_statuspage', 'status_page_domain_lookup(text)', 'EXECUTE');  -- true
SELECT has_function_privilege('proreta_statuspage', 'status_page_domain_allowed(text)', 'EXECUTE'); -- true
SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
 WHERE has_table_privilege('proreta_statuspage', c.oid, 'SELECT');                                   -- 0
```

## A page shows old data
- The renderer caches 15 s per replica; jobs rebuilds every 30 s (`proreta_jobs_housekeeping_runs_total{task="rebuild_status_pages"}`, `proreta_jobs_status_page_snapshots_total`). A configuration change is rebuilt by the api at once.
- jobs rebuilds the published pages one after another, one snapshot per page, so the run time grows with the number of published pages (the jobs log shows the duration of each run).
- jobs logs "status page snapshot rebuild failed" with the organization and page id when one page fails; the others continue.
- A suspended or deleted organization's page answers 404 (by design).

## Timing of changes
The api rebuilds a page's snapshot with every change (and when a monitor on it is paused, resumed or deleted); the renderer caches each page 15 s per replica and answers with `max-age=30`. So publishing, unpublishing, deleting and setting or removing a password apply within about a minute, not at once: a visitor or a cache may still see the previous state until then. To take a page down urgently, unpublish it and expect up to a minute.

## Addresses
A deleted page's address, and the old address after a rename, stay reserved for the organization for 30 days (`status_page_released_slugs`); the organization may reuse them, others get "This address is not available", the same answer as for an address in use. To free one early for someone else (rare, on request of the organization that released it), delete its row as `proreta_owner` with `app.tenant_id` set to that organization.

## A visitor cannot open a protected page
- Wrong passwords and 429s are counted in `proreta_statuspage_unlock_attempts_total` and `proreta_statuspage_rate_limited_total{kind="unlock"}`: 10 attempts per client (IPv4 address or IPv6 /64) and 100 wrong passwords per page per 15 minutes, per replica. When the page budget is used up, each client still has 3 wrong passwords of its own, so a visitor with the right password gets in during an attack.
- The unlock cookie `proreta_status_unlock` is signed with the cookie key (`HttpOnly`, `SameSite=Lax`, `Secure` unless `config.cookieSecure` is `false`, `Path=/status/<slug>`, 12 hours) and unlocks that one page. It has no `__Host-` prefix, because that prefix requires `Path=/`, and the page lives under a path. A sibling subdomain of the installation's host could therefore set a cookie with the same name; it cannot forge a valid one without the cookie key, so the worst it can do is make a visitor's own unlock fail until the cookie expires or is deleted.
- Changing the password signs every visitor out; so does rotating the cookie key (delete the Secret `proreta-statuspage`, sync, restart the `statuspage` Deployment). Losing the Secret has the same effect and nothing else; it can be kept with the encryption key ([back up the secrets](../install.md#back-up-the-secrets)).
- Behind a gateway the client IP comes from `X-Forwarded-For` when `config.trustedProxyHops` is the number of proxies in front (1 for one gateway, the chart default); with 0 the peer address is used, which behind a proxy makes every visitor share one limit.
- Limits trust `X-Forwarded-For` as the api does: requests without it all share one `unknown` bucket, and a pod inside the cluster can reach the `statuspage` Service directly (unless an enforced NetworkPolicy prevents it) and send a forged `X-Forwarded-For` to get a fresh bucket for every request. The limits therefore only hold for traffic through the gateway; treat them as a cost bound, not a protection against in-cluster clients.

## Projects
- A page is created in one project or in the organization and stays there; there is no move yet. The address stays unique across every organization. The plan limit (`max_status_pages`, 1 on Free and Internal, unlimited on Community) counts the pages of one project, and separately the organization-level pages, so every project can have its own page. A project may hold several pages once the limit is raised (plan or per-organization override): nothing else changes. `/usage` reports the fullest scope against that limit.
- Components of a project page must be monitors of that project (`422` on `items.<n>.monitorId` otherwise). When a monitor is moved to another project it is removed from the pages of its old project, and those pages are refreshed at once; organization-level pages keep it. The monitor's audit row records `statusPageComponentsRemoved`.
- A project that still has a status page is not empty: delete the page before the project.
- A page of a project the caller cannot see answers `404`, like other project data.

## Abuse
- A page shows only what its maintainers published: public names, groups, states, daily bars and generated incident titles. Ask a maintainer of the organization to rename or remove a component, or unpublish the page. As a last resort the platform admin suspends the organization (its page then answers 404).
- Request floods are limited per client IP and per replica; more replicas multiply the effective limits.

## Custom domains (behind a feature flag)
The renderer can serve a page at the root of a customer's own domain, such as `https://status.example.com/`, once that host is verified. This is off by default and only the renderer side exists so far: adding and verifying domains, the TLS hook and the chart values come in later changes, so an installation cannot use it end to end yet.

Settings of the `statuspage` process (environment variables):

| Variable | Meaning |
|---|---|
| `PRORETA_STATUS_PAGE_CUSTOM_DOMAINS` | `true` turns host mode on (default `false`). Off, the `Host` header is ignored and everything behaves as described above. |
| `PRORETA_STATUSPAGE_INSTALLATION_HOSTS` | Comma separated host names of the installation itself, in addition to the host of `PRORETA_PUBLIC_URL`. These keep the `/status/<slug>` routes. |
| `PRORETA_ABUSE_URL` | An `https://` URL or `mailto:` address behind the "Report abuse" link in the footer of customer-domain pages. Empty means no link, and the renderer logs a warning at startup. |
| `PRORETA_TRUSTED_PROXY_HOPS` | As before; also decides whether `X-Forwarded-Proto` is believed (for HSTS). |

With host mode on:
- A request is classified by its `Host` (lower case, port and one trailing dot removed). An installation host serves the `/status/<slug>` routes exactly as before. Any other host is looked up with `status_page_domain_lookup(host)`, which returns the slug of a verified or active domain of a published page of an active organization, and nothing else. `X-Forwarded-Host` is never read.
- On a customer host the page is at `/`, with `/status.json`, `/feed.rss`, `/badge`, `/embed.js`, `/robots.txt` and the password form posting to `/unlock`. Every link in the page, the feed, the badge and the embed script uses the request host. `/status/<slug>` is not served there.
- An unknown, unverified, failed, disabled, released or unpublished host, a suspended organization and a malformed `Host` all get the same 404.
- The unlock cookie of a customer host is `__Host-proreta_status_unlock` (`Path=/`, `Secure`, no `Domain`) and is bound to that host: it does not unlock the page on another host.
- The footer reads "Status page by Proreta" with the abuse link, and the password form carries a notice not to enter an account password. They cannot be changed from the page settings.
- `Strict-Transport-Security: max-age=31536000` (no `includeSubDomains`, no `preload`) is sent on successful answers when the request arrived over TLS: the proxy in front must set `X-Forwarded-Proto` and `PRORETA_TRUSTED_PROXY_HOPS` must count it.
- Limits, per replica: all visitors of one host share 20 requests per second (burst 200), and lookups of hosts not in the cache are limited to 50 per second in total (a refused one answers `429`). Answers are cached for 15 seconds per host, so a removed domain stops serving within about 15 seconds plus the 30 second browser cache.
- Metrics: `proreta_statuspage_requests_total{mode="path|host"}` and `proreta_statuspage_host_lookups_total{result="hit|miss|unknown|limited"}`.

Not done yet: `<link rel="canonical">` on `/status/<slug>` pointing at the page's own domain (the renderer role cannot read a page's domain; a customer-domain page already names itself as canonical).

## Not available yet
Manual incident updates and maintenance windows, subscriptions other than RSS, translations and custom styling.
