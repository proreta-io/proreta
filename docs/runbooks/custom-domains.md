# Custom status page domains

A customer can serve a status page at the root of its own host, such as `https://status.example.com/`, after proving control of the name in DNS. The feature is **off by default** and its API operations are still marked experimental. Proreta verifies hosts, routes them to the right page and tells a TLS proxy which hosts may get a certificate; **terminating TLS for customer hosts is the operator's job**, and the chart ships no proxy for it.

## Before you turn it on
- `statuspage.enabled: true` and a working [status page](status-page.md) on the installation host.
- A host name of your own for customers to point at (`cnameTarget`, for example `customers.example.net`) that resolves to the proxy or Gateway terminating TLS for customer hosts.
- A way to get certificates for hosts you do not know in advance: a proxy with on-demand TLS that asks the [permission endpoint](#the-tls-permission-endpoint), or your own Gateway with one listener and certificate per customer host.
- A plan that allows domains: the limit `Status page domains` is unlimited on `internal`, the only seeded plan ([platform admin panel](admin-panel.md)). An organization on a custom plan with 0 sees the section but cannot add a host.

## Turning it on
Chart values (all under `statuspage.customDomains`; the full list is in the [chart README](../../deploy/helm/proreta/README.md)):

```yaml
statuspage:
  enabled: true
  customDomains:
    enabled: true
    cnameTarget: customers.example.net   # required
    abuseUrl: mailto:abuse@example.net    # the "Report abuse" link; a warning is printed without it
```

| Value | Variable (processes) | Meaning |
|---|---|---|
| `enabled` | `PRORETA_STATUS_PAGE_CUSTOM_DOMAINS` (api, jobs, statuspage) | Turns the feature on for the api, jobs and the renderer together. Off, the domain operations answer 404 `custom-domains-disabled`, the app shows a "not turned on" note in place of the section and the renderer ignores the `Host` header |
| `cnameTarget` | `PRORETA_STATUSPAGE_CNAME_TARGET` (api, jobs) | Required. The host customers point their CNAME at: lowercase, no trailing dot. The render fails without it |
| `installationHosts` | `PRORETA_STATUSPAGE_INSTALLATION_HOSTS` (api, statuspage) | Hosts that keep the `/status/<slug>` routes and that no customer may claim. Empty: `httpRoute.hostnames` without wildcards, plus the host of `config.publicUrl` |
| `abuseUrl` | `PRORETA_ABUSE_URL` (statuspage) | An `https://` URL or `mailto:` address for the "Report abuse" link in the footer of customer-domain pages |
| `dns.resolver`, `dns.timeout` | `PRORETA_DNS_RESOLVER`, `PRORETA_DNS_TIMEOUT` (jobs) | Resolver (`ip` or `ip:port`) and per-lookup timeout (500ms to 10s, default 3s) of the verification job. Empty resolver: the resolver of the pod |
| `tlsAsk.*`, `secrets.tlsAsk.*` | `PRORETA_STATUSPAGE_TLS_ASK_ADDR`, `PRORETA_STATUSPAGE_TLS_ASK_TOKEN` (statuspage) | The permission endpoint for an on-demand TLS proxy, see below |
| `httpRoute.*` | | A second HTTPRoute for customer hosts your own Gateway terminates, see below |

The release that brings the feature runs its migrations as usual (the migration hook Job); turning the values on needs no manual database step and no database bootstrap run. Set `config.trustedProxyHops` to the number of proxies in front of the `statuspage` Service on the customer-host path: client limits and `Strict-Transport-Security` depend on it.

## Terminating TLS for customer hosts
Whatever sits in front must forward the **original `Host` header** to the `statuspage` Service (`X-Forwarded-Host` is never read) and set `X-Forwarded-For` and `X-Forwarded-Proto`. Two ways:

**A proxy with on-demand TLS** (for example Caddy). It obtains a certificate the first time a host is requested, after asking the permission endpoint. Turn the endpoint on with `tlsAsk.enabled: true` and give it a token:

```sh
kubectl -n proreta create secret generic proreta-tls-ask --from-literal=token="$(openssl rand -hex 32)"
```
```yaml
secrets:
  tlsAsk:
    existingSecret: proreta-tls-ask
statuspage:
  customDomains:
    tlsAsk:
      enabled: true
      proxy:
        namespace: caddy        # with networkPolicy.enabled: the only source admitted to the port
```

The proxy asks `http://<fullname>-statuspage.<namespace>:9091/internal/tls/allow` (port `tls-ask` of the statuspage Service; `<fullname>` is the release name, `<release>-proreta` when the release name does not contain `proreta`, or `fullnameOverride`). `proretactl status-domains list -format caddy` prints a Caddyfile fragment to start from. New verified hosts need no reload. The chart does not generate the token; with External Secrets use `secrets.tlsAsk.remoteKey` (property `token`) instead of `existingSecret`.

**Your own Gateway.** Add a listener with its certificate for each customer host and list the hosts in `httpRoute.hostnames` (with `httpRoute.enabled: true` and `httpRoute.parentRef.name`); the chart renders the HTTPRoute `<fullname>-customer-hosts`, which sends every path of those hosts to the `statuspage` Service. Every new customer host is a values change, so this suits a handful of known hosts. The render fails when the list contains an installation host.

## One status domain on a self-hosted installation
The common self-hosted setup is one organization with several teams. Each team's page is served on the application's own host at `/status/<slug>`, and one general page is served at the root of the organization's own host, such as `https://status.example.com/`, and linked from its website. A page keeps its `/status/<slug>` address on the application host when it also has a custom host; on the custom host only that one page is served, at `/`. No on-demand TLS proxy is needed for one known host:

1. **Plan.** Every organization starts on the `internal` plan, which allows domains. Only an organization moved to a custom plan with the limit at 0 needs the plan changed (or edited) in **Admin → Organizations** before adding the host.
2. **Gateway.** Add a listener for `status.example.com` with its own certificate (with cert-manager: the `cert-manager.io/cluster-issuer` annotation on the Gateway and a `tls.certificateRefs` Secret name per listener), then let the chart route the host:

   ```yaml
   statuspage:
     enabled: true
     customDomains:
       enabled: true
       cnameTarget: proreta.example.com   # any name that resolves to this Gateway
       abuseUrl: mailto:abuse@example.com
       httpRoute:
         enabled: true
         hostnames: [status.example.com]
         parentRef: {name: public-gateway, namespace: gateway-system, sectionName: https-status}
   config:
     trustedProxyHops: 1
   ```
3. **In the app.** An owner or maintainer of the organization opens the organization-level status page, publishes it and adds `status.example.com` under **Custom domain**. Create the TXT and CNAME records the app shows in your own DNS zone and press **Check now**; the host is served as soon as the TXT record is seen.

The chart renders no Ingress objects. With an Ingress controller, create an Ingress for the host yourself (with your cert-manager certificate) that sends every path to the `statuspage` Service, and keep `customDomains.httpRoute.enabled` off; the original `Host` header must reach the Service.

## What the customer does
Owners and maintainers of the page's project, or of the organization for an organization-level page, use the **Custom domain** section of the status page settings; people who can only read the page see the list.

1. Enter the host name only, such as `status.example.com`. It must be a name below the customer's own domain: bare registrable domains, wildcards, IP addresses, public suffixes, reserved names, installation hosts (the application's own host and every name below it) and malformed names are refused with a message that says why. Unicode characters are refused; the ASCII `xn--` form is accepted. A name another organization already holds can be added, but its check answers "This host is not available" until DNS carries a TXT record with this organization's token and none with the other's; then the host moves to the new holder. While both tokens are in DNS the current holder keeps it.
2. Create two DNS records, shown in the app:

   | Record | Name | Value | Purpose |
   |---|---|---|---|
   | TXT | `_proreta-status.status.example.com` | `proreta-verify=<token>` | Proves the domain is theirs |
   | CNAME | `status.example.com` | the `cnameTarget` | Sends visitors to the page |

3. Wait for the check, or press **Check now** (limited to 6 per minute and 30 per hour per organization).

States:

| State | Meaning | Served | Certificate allowed |
|---|---|---|---|
| `pending` | The TXT record was not seen yet. Checked every 10 minutes in the first 24 hours, then hourly; removed after 7 days without the record | no | no |
| `verified` | The TXT record was seen; the CNAME does not end at the target yet. Checked every 6 hours | yes | yes |
| `active` | Verified and the CNAME ends at the target. Checked every 6 hours | yes | yes |
| `failed` | Neither record was found in at least 3 consecutive checks and for 7 days. Returns to `verified` only when the TXT record is seen again; the CNAME alone does not bring it back. Checked every 6 hours | no | no |

Only the TXT record verifies a host: a CNAME alone never does, so a stale CNAME pointing at you cannot serve someone else's page. Once verified, either record keeps the host alive, so the TXT record may be removed after the CNAME is in place; if the host then fails, the TXT record has to be put back. A lookup that times out or fails counts as unknown, never as a missing record.

Limits: 3 domains per page, 10 domains added per organization in 24 hours, and the plan limit for the organization as a whole. A page with several domains marks one as primary: it is listed first and replaced automatically when removed, and it does not change any link or redirect yet. Removing a domain stops serving it at once and frees its place in the plan.

Changes are audited in the organization: `status_page.domain_added`, `status_page.domain_verified`, `status_page.domain_activated`, `status_page.domain_failed`, `status_page.domain_primary_changed` and `status_page.domain_removed`. A domain that fails is also logged by `jobs` at warn level. There is no in-app notice or email about a failure; the state shows in the settings.

## The verification job
The `jobs` process checks due domains every minute (`check_status_page_domains`): up to 500 per run in batches of 25, the rest at the next run. Metrics and suggested alerts (`DomainChecksStale`, `DomainChecksDNSErrors`, `DomainsFailedJump`) are in [observability](observability.md): `proreta_status_page_domains{state}` and `proreta_status_page_domains_checks_total{result}` with `ok`, `txt-missing`, `cname-missing`, `txt-mismatch` and `dns-error`. The renderer adds `proreta_statuspage_requests_total{mode}` (`path`, `host`) and `proreta_statuspage_host_lookups_total{result}` (`hit`, `miss`, `unknown`, `limited`); its limits in host mode are per replica and never shared by all visitors of a host: each client has the page-view limit of path mode (2 per second, burst 60), and lookups of hosts not in its 15 second cache are limited to 2 per second (burst 20) per client and 50 per second in total. When a lookup is refused and the host was seen before, its last answer is served instead of a 429.

## Troubleshooting
- **The section is missing in the app, or the API answers 404 `custom-domains-disabled`.** `statuspage.customDomains.enabled` is off, or the api pods were not restarted with it.
- **A domain stays `pending`.** Check the TXT record from outside: `dig +short TXT _proreta-status.status.example.com` must print `"proreta-verify=<token>"` exactly as shown in the app. Some DNS providers append the zone name to what is typed: there the record name is entered without the zone (`_proreta-status.status`, not the full name). A TXT record with an old or wrong token counts as `txt-missing` (the token changes when a host is removed and added again). `txt-mismatch` in the checks metric means the host is already served for another organization, so this claim stays pending until its own token is the only one in DNS. Many `dns-error` results point at the resolver: set `dns.resolver`.
- **A domain stays `verified`.** The CNAME chain does not end at `cnameTarget`: `dig +short CNAME status.example.com`. A proxied or flattened record at the customer's DNS provider hides the CNAME; the page is served anyway. "The CNAME target is not set up on this installation yet" in the app means `cnameTarget` is empty for the api.
- **The host is verified but the browser shows a certificate error.** TLS is terminated before Proreta: look at the proxy. With on-demand TLS, `proreta_statuspage_tls_allow_total{result}` shows whether the proxy asks and what it is told (`unauthorized`: wrong token; no samples: the proxy cannot reach the port, check the network policy).
- **The host answers 404.** The same 404 covers an unknown, unverified, failed, disabled or released host, an unpublished page and a suspended organization. Check the state with `proretactl status-domains list -all`, that the page is published, and that the proxy forwards the original `Host`. A change takes about 15 seconds at the renderer plus 30 seconds of browser cache.
- **`/status/<slug>` answers 404 on the customer host.** By design: on a customer host the page is at `/`. Installation hosts keep `/status/<slug>`.

## Operator interface
- *May a certificate be issued for this host?* The TLS permission endpoint.
- *Which hosts are served now?* `proretactl status-domains list`.

A host stops being served, and stops being allowed a certificate, when its domain is disabled by a platform admin, no longer verified, released, its page is deleted or its organization is suspended or deleted. An unpublished page may still get a certificate for a verified host.

## The TLS permission endpoint
A proxy that issues certificates on demand (Caddy's on-demand TLS) asks before it requests one:

```
GET /internal/tls/allow?domain=<host>
```

| Answer | Meaning |
|---|---|
| `200` | A certificate may be issued for the host |
| `404` | It may not: unknown, not verified, failed, disabled, released, organization not active, or not a valid host name |
| `401` | The token is missing or wrong |
| `429` | Too many lookups; the proxy should retry later (the limit is per replica: 100 burst, then 50 per second, and answers are cached for 10 seconds, so repeated asks for one host are free) |
| `503` | The database could not be asked; nothing is allowed |

The body is always empty. The host is lowercased and a trailing dot removed before the lookup; IP addresses, wildcards, reserved names, non-ASCII names and public suffixes are refused without touching the database.

### Where it runs and how it is protected
It runs on its **own listener** of the `statuspage` process, never behind the public Gateway and not on the admin port (`:9090`, metrics and probes). Its own port lets a network policy admit only the proxy while Prometheus and the probes keep the admin port. It is off unless you set the address; the chart sets both variables from `statuspage.customDomains.tlsAsk` and `secrets.tlsAsk`.

| Variable | Meaning |
|---|---|
| `PRORETA_STATUSPAGE_TLS_ASK_ADDR` | Listen address, for example `:9091`. Empty (the default) turns the endpoint off. Must differ from `PRORETA_HTTP_ADDR` and `PRORETA_ADMIN_ADDR` |
| `PRORETA_STATUSPAGE_TLS_ASK_TOKEN` | The shared token, from a Secret. Required when the address is set, and the process refuses to start without it. At least 32 characters of letters, digits and `. _ ~ -` (so it can sit in a URL) |

The proxy presents the token as `Authorization: Bearer <token>`, or as `&token=<token>` in the query for proxies that cannot add a header (Caddy's `ask` URL is such a case: put the token in the URL you give Caddy and keep that Caddyfile out of version control). The token is compared in constant time and never logged; the query string is never logged either. Generate one with `openssl rand -hex 32`. To rotate it, change the Secret, restart the `statuspage` pods and update the proxy.

Allow only the proxy to reach the port (a network policy); the token is a second line of defence, not a replacement.

The metric `proreta_statuspage_tls_allow_total{result}` (`allowed`, `denied`, `unauthorized`, `rate_limited`, `error`) is on the admin port. Many `denied` answers for hosts you expect to be verified mean the proxy and the database disagree; many `unauthorized` answers mean a wrong token or something scanning the port.

## `proretactl status-domains list`
Lists the custom domains through the api's database role, so run it where `PRORETA_DATABASE_URL` is the api's connection string (for example in the api pod):

```sh
proretactl status-domains list                  # text table of the hosts served now
proretactl status-domains list -format json
proretactl status-domains list -format caddy
proretactl status-domains list -all             # every unreleased domain with its state (text and json)
```

- `text`: a table (host, state, disabled, primary, organization slug, status page slug) and a count.
- `json`: stable and extended only by new fields:

  ```json
  {"domains": [{"host": "status.example.com", "state": "active", "primary": true, "disabled": false,
                "organization": "acme", "page": "acme-status", "verifiedAt": "2026-11-20T10:30:00Z"}]}
  ```

  `domains` is always an array, sorted by host; `verifiedAt` is `null` until the TXT proof was seen. Without `-all` only served hosts are listed (state `verified` or `active`, not disabled, organization active, page not deleted).
- `caddy`: a Caddyfile fragment. The static part is one site block with exactly the served hosts, proxying to `{$PRORETA_STATUSPAGE_UPSTREAM}` (set that environment variable for Caddy); regenerate it and reload after a change. The commented part is the on-demand alternative that asks the endpoint above, so a new verified host needs no reload. It contains no secret; the ask URL comes from `{$PRORETA_TLS_ASK_URL}`. Check the syntax against the Caddy version you run: Caddy's permission options have changed between versions.

The command reads nothing but host names, states and the slugs of the page and the organization; it never prints a verification token.

## Platform admin: the abuse switch
**Admin → Domains** (`/admin/domains`) lists the custom domains of every organization and disables or enables one host with a reason, at once and independent of the customer's state. The same through the API: `GET /api/v1/admin/status-page-domains` with `state`, `query`, `limit` and `offset`, and `POST /api/v1/admin/status-page-domains/{domainId}/disable` (and `/enable`) with `{"reason": "..."}`. Audit, guard rails and timing are in the [platform admin panel](admin-panel.md) runbook.

Takedown timing: the renderer caches a host's lookup for about 15 seconds and browsers a page for 30 seconds; the permission endpoint caches answers for 10 seconds, and a certificate that was already issued keeps working at the proxy, but the renderer answers 404 for the host.

## Not available yet
A bundled TLS proxy in the chart, host names typed in Unicode (use the `xn--` form), a redirect from `/status/<slug>` to the page's own domain, and releasing domains automatically when a plan is lowered or an organization is deleted (remove them in the app first).
