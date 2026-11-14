# Custom status page domains

> **Behind a feature flag and incomplete.** Status pages on a customer's own domain are being built in phases and are off by default (`PRORETA_STATUS_PAGE_CUSTOM_DOMAINS`, API only). This runbook covers the operator interface only: the TLS permission endpoint, `proretactl status-domains list` and the platform admin's abuse switch. The Helm values, the renderer's host mode, the customer settings and the front proxy recipe are not part of a release yet, and this page is **incomplete until the chart package lands**: the variables below are plain environment variables of the `statuspage` process, and the chart does not set them.

## What an operator needs to know
A customer can attach a host such as `status.example.com` to a status page and prove control of the name with a DNS record. Proreta only serves a host once it is verified; the operator terminates TLS (a proxy of their choice, or Caddy) and forwards the original `Host` header to the `statuspage` Service. The operator interface answers two questions:

- *May a certificate be issued for this host?* The TLS permission endpoint (below).
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
It runs on its **own listener** of the `statuspage` process, never behind the public Gateway and not on the admin port (`:9090`, metrics and probes). Its own port lets a network policy admit only the proxy while Prometheus and the probes keep the admin port. It is off unless you set the address.

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
`POST /api/v1/admin/status-page-domains/{domainId}/disable` (and `/enable`) with `{"reason": "..."}` stops or resumes serving one host at once, independent of the customer's state. The list is `GET /api/v1/admin/status-page-domains` with `state`, `query`, `limit` and `offset`. Audit, guard rails and timing are in the [platform admin panel](admin-panel.md) runbook.

Takedown timing: the renderer caches a host's lookup for about 15 seconds and browsers a page for 30 seconds; the permission endpoint caches answers for 10 seconds, and a certificate that was already issued keeps working at the proxy, but the renderer answers 404 for the host.
