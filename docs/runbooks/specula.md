# Specula: checking services inside your network

Proreta checks public HTTPS addresses from its own worker. Services that have no public address (inside a Kubernetes cluster, a VPC or an office network) need an executor on the inside. That executor is a **Specula**: a small program that you run in your network. It only makes outbound HTTPS calls to Proreta, claims the checks that are due, runs them against your services and reports the results. Nothing connects in.

Everything else stays in Proreta: the schedule, incidents, notifications and the status page work the same for a monitor that runs at a Specula.

## What you need
- A Specula token (below).
- The executor running where it can reach both your services and your Proreta address over HTTPS. How the network is arranged (peering, VPN, a public name) is up to you. Proreta itself must be reachable on its public URL from the executor.
- The Specula gateway enabled in your Proreta installation (operators: [turn it on](#enable-the-gateway)). Without it the executor cannot connect and shows as *Waiting*.

## Register a Specula
Owners and maintainers: **Specula → Register a Specula**.

1. Give it a name, for example the cluster it runs in. Names are unique among the Specula that are not revoked.
2. Choose who can use it: all projects, or chosen projects. Projects that are not chosen do not see it and cannot run monitors there.
3. Copy the token. It starts with `spc_` and is shown **once**; Proreta keeps only a hash. If you lose it, revoke the Specula and register a new one. Treat it like a password: whoever has it can claim this Specula's checks and receives the headers and bodies of those monitors.

## Run the executor
**In Kubernetes**, use the [Specula chart](../../deploy/helm/proreta-specula/README.md): put the token in a Secret, then
```sh
helm upgrade --install specula oci://ghcr.io/proreta-io/charts/proreta-specula --version <release> \
  --namespace proreta-specula --create-namespace \
  --set proreta.url=https://proreta.example.com --set token.existingSecret=proreta-specula-token
```

**Anywhere else**, run the `/app/specula` binary of the Proreta image (for example with Docker, systemd or a plain process manager). It reads two environment variables:

| Variable | Meaning |
|---|---|
| `PRORETA_URL` | The address of Proreta, for example `https://proreta.example.com` (no trailing slash). |
| `PRORETA_SPECULA_TOKEN` | The token from the registration. |

Optional: `PRORETA_SPECULA_CONCURRENCY` (checks in flight, default 20), `PRORETA_SPECULA_POLL_INTERVAL` (how often it asks for work, default 5s), `PRORETA_ADMIN_ADDR` (metrics, default `:9090`), `PRORETA_LOG_LEVEL`.

It has no database and keeps nothing on disk; run two replicas with the same token if you want it to survive a restart without a gap (a check is leased to one of them at a time).

The executor stops with an error when Proreta rejects its token: the token is wrong or the Specula was revoked.

## Enable the gateway
Operators: the gateway is a separate Deployment of the Proreta chart, off by default. Turn it on with `speculagw.enabled: true`. It is routed at `/specula/v1` on the same host as the app (the app's own Specula page is `/specula`), uses the worker's database role and the request keys (`secrets.request`) to hand decrypted monitor requests to an executor, and makes no outbound calls. Check that executors can reach `config.publicUrl` over HTTPS from their networks. See the [chart README](../../deploy/helm/proreta/README.md).

## Use it in a monitor
When a project may use at least one Specula, the monitor form has a **Run from** field: *Proreta cloud* or one of them.

- A monitor that runs at a Specula may use `http` or `https`, any port, and internal host names or addresses such as `http://orders.shop.svc:8080/health`. The executor reaches whatever your network lets it reach; Proreta's blocking of private addresses does not apply there because the targets are yours.
- Moving a monitor between *Proreta cloud* and a Specula ends an open incident and starts its status over. A target that only works inside your network is refused when you move it back to *Proreta cloud*.
- Method, headers and body work as in [Monitor requests](../monitor-requests.md). The executor receives the decrypted headers and body over TLS, so install it only where you trust the host.

## Status
| Status | Meaning |
|---|---|
| Waiting | Registered, never connected. |
| Online | Heard from in the last 90 seconds (it reports every minute). |
| Offline | Connected before, silent for longer. Its monitors are not being checked. After 3 minutes without a call they show **unknown**, not *down*, so a lost link does not look like an outage of your services. They return to up or down with the first check after it reconnects. |
| Revoked | The token no longer works. Its monitors show *unknown* at once. |

## Change who can use it, rename, revoke
Owners and maintainers can **Edit** a Specula (name, who can use it) and **Revoke** it. Revoking works at once: the executor is refused from its next call and the location hands out no more checks. A revoked Specula cannot be restored. Monitors that ran there stop being checked and show *unknown* until you move them to another location.

Removing a project from the list stops the Specula from handing out that project's monitors; move them first.

## Who can do what
Owners and maintainers register, edit and revoke Specula and see all of them. Members and viewers see the Specula their projects may use (without the project list) and choose them in the monitor form (members). See [Roles and permissions](../roles.md).

## Audit
`specula.created`, `specula.updated` (field names only) and `specula.revoked`. Tokens are never recorded.

## Limits
- Going offline or being revoked does not open an incident or send a notification yet; the monitors show *unknown*. Watch the Specula page, or alert on the executor's own metrics.
- HTTP and HTTPS checks only; TCP checks are not available yet.
- The plan limits how many Specula an organization may have (`max_agents`: `free` 1, `internal` 20, `community` unlimited). Revoked ones do not count; at the limit, registering another fails with "plan limit reached" until you revoke one. The operator can override it per organization in `tenants.limits`.
- A token has no expiry; rotate by registering a new Specula and revoking the old one.
