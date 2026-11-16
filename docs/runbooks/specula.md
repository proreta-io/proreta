# Specula: checking services inside your network

Proreta checks public HTTPS addresses from its own worker. Services that have no public address (inside a Kubernetes cluster, a VPC or an office network) need an executor on the inside. That executor is a **Specula**: a small program that you run in your network. It only makes outbound HTTPS calls to Proreta, fetches the schedule of its checks, runs them against your services on its own clock and reports the results. Nothing connects in.

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

Optional: `PRORETA_SPECULA_STATE_DIR` (a writable directory for the schedule and the buffer; unset means memory only), `PRORETA_SPECULA_CONCURRENCY` (checks in flight, default 20), `PRORETA_SPECULA_POLL_INTERVAL` (how often it asks for work, default 5s), `PRORETA_ADMIN_ADDR` (metrics, default `:9090`), `PRORETA_LOG_LEVEL`.

It has no database. It keeps a small state directory (`PRORETA_SPECULA_STATE_DIR`, see [The buffer](#the-buffer)); the chart provides one. Run one executor per Specula: a second one with the same token runs every check twice.

The executor stops with an error when Proreta rejects its token: the token is wrong or the Specula was revoked.

## Enable the gateway
Operators: the gateway is a separate Deployment of the Proreta chart, off by default. Turn it on with `speculagw.enabled: true`. It is routed at `/specula/v1` on the same host as the app (the app's own Specula page is `/specula`), uses the worker's database role and the request keys (`secrets.request`) to hand decrypted monitor requests to an executor, and makes no outbound calls. Check that executors can reach `config.publicUrl` over HTTPS from their networks. See the [chart README](../../deploy/helm/proreta/README.md).

## How the schedule works
An executor of the current version does not ask for each check. It fetches the **schedule** of its Specula once (`GET /specula/v1/schedule`) and runs every check on its own clock:

- For each monitor the schedule holds the check definition, the interval and an **anchor** time that Proreta assigns. The check runs at the anchor plus a whole number of intervals, so a monitor is checked at the same seconds of every minute (or other interval) no matter how many times the executor restarts.
- The executor refreshes the schedule every few minutes and when Proreta tells it that something changed (a version number in every answer). A pause, resume, edit, move or delete of a monitor reaches a connected executor within about a minute. An executor that cannot be reached picks the change up when it reconnects.
- The schedule is valid for a limited time (24 hours by default). When it runs out the executor **stops starting checks** and keeps trying to refresh; this bounds what a lost or decommissioned executor can do to your services.
- Each result is reported with the **slot** it belongs to (the scheduled time, not the time it arrived), and Proreta stores it under that time. Proreta accepts a result only for a slot that the schedule really had, for a monitor that was active then, at most 24 hours old and not in the future. A result sent twice is stored once; a result for a monitor that was paused, edited or moved in the meantime is ignored.
- Because the executor does not lease checks, a monitor at a Specula no longer has a "next run" in Proreta; its cadence comes from the schedule only.

## When the link to Proreta drops
The checks keep running. Results that cannot be delivered go into the buffer, in order, and are sent when the connection is back, oldest first, at a limited rate (a backlog of one hour for a thousand monitors takes a couple of minutes). A failed check is sent as soon as there is a connection; a successful one may wait a few seconds so that results travel in batches.

What you see:

- While Proreta hears nothing from the Specula, its status becomes **Unreachable** and, after 3 minutes, its monitors show **unknown** and the *Specula unreachable* incident opens, as described under [Status](#status). The checks are being made; the results are simply not here yet.
- When the link returns, the buffered results are stored with their original times. The history of the outage fills in, the *unknown* is replaced by the real states, and incidents and rollups are corrected.
- An incident that began and ended during the outage is created with its real start and end. If the service was already healthy again by the time Proreta hears about a failure, you get one late "recovered" message and no "down" message (the late notification rules are still being finished: see the notes for the release you run).
- An incident opened or resolved by a result that reached Proreta long after its check (more than 10 minutes) is marked **reported late** in the monitor's incident list; `openedLate` and `resolvedLate` on the incident in the API say which end. Its times are the check times, not the time Proreta heard.
- A **gap** means expected slots with no stored result. The buffer closes gaps as long as the outage is shorter than the schedule's validity and the buffer limits (24 hours and 256 MiB by default), the executor keeps running, and its state directory survives. Beyond that the missing part stays missing: a day or an hour without results is shown as **no data**, never as downtime, and uptime is computed over the results that exist.
- A failing check is not a gap: it is a result. If your network is gone and the checks fail, those failures are recorded and reported when possible.
- A restart of the executor loses the few seconds in which it was down. Checks are not run for the past.

This protects against a lost link between the executor and Proreta, and against an outage of Proreta's gateway or database. It does not help when Proreta's own cloud location is down (nothing runs there to be replayed), or when the executor itself is down and the state directory is lost.

## The buffer
The buffer and the cached schedule live in the state directory as plain files (a few MiB per segment file, checked for corruption, written with the owner-only permission `0600`). It is bounded: when it reaches its size or age limit (256 MiB or 24 hours by default) the oldest results are dropped and counted, never the newest. A result takes roughly 150 bytes, which gives this guide:

| Monitors at a 60 s interval | Results per day | Disk per day | Fits 256 MiB for a full day |
|---|---|---|---|
| 100 | 144 000 | about 22 MB | yes |
| 1 000 | 1 440 000 | about 220 MB | just |
| 2 000 | 2 880 000 | about 440 MB | no, about 14 hours |

Shorter intervals count proportionally. These numbers are estimates, not guarantees.

In the chart the state directory is an `emptyDir` (`state.sizeLimit`, 512Mi). It survives a restart of the container but **not the replacement of the pod** (an upgrade, an eviction or a node drain). When the pod is replaced while the link is up, the executor sends what it holds before it stops. When it is replaced while the link is down, the buffer and the cached schedule are lost, and the executor checks nothing until it reaches Proreta again. A volume that outlives the pod is not part of the chart yet.

The cached schedule contains the decrypted headers and bodies of your monitors (tokens may be among them). Treat the state directory like the executor's own memory: do not mount it from shared storage and do not back it up.

## Degraded state
If the state directory is unusable (full, read-only, removed, or not configured) the executor does not stop. It keeps the schedule and a small number of recent results in memory and reports itself as **degraded**. In that mode:

- checks keep running while the executor is up and the schedule is valid;
- a link outage is bridged only for as long as the memory holds (about two thousand results: roughly 20 minutes for a hundred monitors at 60 s, 2 minutes for a thousand), and a restart during an outage loses the schedule, so no checks run until Proreta can be reached;
- nothing about your monitors is written to disk.

You can choose this mode on purpose with `state.enabled: false` in the chart if you do not want monitor settings on disk. Otherwise treat a degraded executor as a fault to fix (check the logs for the reason and the disk space of the node).

## Metrics, logs and alerts
The executor serves Prometheus metrics on `PRORETA_ADMIN_ADDR` (default `:9090`, `/metrics`); the list is in the `specula (executor)` section of the [observability runbook](observability.md). The page of the Specula in Proreta shows the same state as last reported by the executor's hello every minute (schedule age, results waiting, oldest waiting result, degraded buffer).

At start the executor logs one line, `executor state at start`, with its instance id, whether a state directory is used, the results waiting in the buffer, whether the buffer is degraded, the clock offset, and the issue and expiry time and monitor count of the cached schedule (or `schedule=none`). It never logs the token or monitor settings.

The instance id is created once and kept in the state directory, so a restart keeps it. Without a state directory it changes at every start. Proreta counts distinct ids per token to notice two executors that share one token; do not copy a state directory to a second executor.

Suggested alerts (PromQL, thresholds are first guesses; the schedule lasts 24 hours unless your gateway is configured otherwise):

| Alert | Expression | For |
|---|---|---|
| Schedule getting old | `specula_schedule_age_seconds > 12 * 3600` | 5m |
| Results lost | `increase(specula_results_dropped_total[15m]) > 0` | 0m |
| Results stuck | `specula_buffer_oldest_age_seconds > 600` while the link works | 10m |
| Not checking | `specula_schedule_expired_monitors > 0` | 5m |
| Buffer in memory | `specula_buffer_degraded == 1` | 10m |

## Warnings on the Specula page
- **Clock skew** appears when the executor's clock differs from Proreta's by more than 30 seconds (the executor measures it at every `hello`; the API field is `clockOffsetMs`). Results more than 30 seconds in the future are rejected, and results from a clock that is behind arrive late. Fix the time on the executor's host (NTP).
- **Two executors** (`duplicateInstance`) appears when two executors with different instance ids said hello with the same token within 3 minutes of each other, and lasts 3 minutes after the last such hello. Run one executor per Specula (the chart does: one replica, strategy `Recreate`). A restart keeps its instance id, and a replaced pod that comes up more than 3 minutes after the old one stopped does not trigger it. The gateway counts the event in `proreta_specula_duplicate_instance_total`.

## Upgrade and downgrade
**Upgrade.** Upgrade the executor chart or image. No setting is needed in Proreta. When it starts, the executor tells Proreta what it supports, fetches its schedule and takes over from the next slot. From then on Proreta hands that Specula no more checks to claim, so no check runs twice. A check that was already claimed by the old executor finishes within 30 seconds. The gateway must be upgraded first or at the same time: a new executor that finds an older gateway (no schedule endpoint) stays in the previous mode, in which it claims one check at a time as before.

**Downgrade.** Install the previous chart or image. The old executor does not announce the schedule capability, so Proreta serves it checks to claim again from its first call. There is nothing to clean up in Proreta, and results the new executor had not yet delivered are lost with its state directory. An executor of the previous version also does not run TCP monitors, and Proreta does not hand it any; they show *unknown* until you upgrade again.

The chart now runs a single pod and replaces it only after the old one has stopped. If you installed an earlier version with two replicas, the upgrade reduces it to one.

## Behind a gateway or proxy
Operators, for the [Specula gateway](#enable-the-gateway) on the Proreta side:

- **Rate limits.** The gateway limits how often one token may call it: reports 5 per second (burst 20, which fits the replay rate), schedule one per 10 seconds (burst 3) and hello 2 per minute, per gateway replica. The metrics are in the `speculagw` section of the observability runbook (`proreta_specula_token_limited_total`, `proreta_specula_reports_total`, `proreta_specula_schedule_unreadable_monitors_total`). A call over the limit gets **429 Too Many Requests** with a `Retry-After` header (seconds), and nothing is stored. The executor waits at least that long, adds a random delay, and sends the same batch again; the same applies to `503` when the gateway is busy. Proxies and load balancers in front of the gateway must pass the status and the header through unchanged and must not retry on their own.
- **Client address.** Failed token checks are limited per client address. Set `config.trustedProxyHops` (`PRORETA_TRUSTED_PROXY_HOPS`) to the number of proxies between the executors and the gateway. The chart default is `1`, which is right for the Gateway of `httpRoute`. With `0` behind a proxy every executor appears to come from the proxy's address, so a few wrong tokens can lock out all of them; with a number larger than the real one a client can choose its own address by sending `X-Forwarded-For`. Use `0` only when nothing sits in front of the gateway.
- **Timeouts.** A batch of results is a normal POST of up to 100 results; proxy timeouts of a few seconds are enough. Do not limit the request rate of `/specula/v1` below what the limits above allow, or a replay after an outage will be slowed down.

## Use it in a monitor
When a project may use at least one Specula, the monitor form has a **Run from** field: *Proreta cloud* or one of them.

- A TCP monitor ([TCP monitors](../tcp-monitors.md)) may run at a Specula too: its host may be any name or address your network resolves, on any port. Upgrade the executor before you place TCP monitors on it.
- An HTTP monitor that runs at a Specula may use `http` or `https`, any port, and internal host names or addresses such as `http://orders.shop.svc:8080/health`. The executor reaches whatever your network lets it reach; Proreta's blocking of private addresses does not apply there because the targets are yours.
- Moving a monitor between *Proreta cloud* and a Specula ends an open incident and starts its status over. A target that only works inside your network is refused when you move it back to *Proreta cloud*.
- Method, headers and body work as in [Monitor requests](../monitor-requests.md). The executor receives the decrypted headers and body over TLS, so install it only where you trust the host.

## Status
| Status | Meaning |
|---|---|
| Waiting | Registered, never connected. |
| Online | Heard from in the last 90 seconds (it reports every minute). |
| Unreachable (`offline` in the API) | Connected before, silent for longer. Proreta has no news of its monitors; an executor of the current version keeps checking and buffers the results ([below](#when-the-link-to-proreta-drops)), an older one does not. After 3 minutes without a call they show **unknown**, not *down*, so a lost link does not look like an outage of your services. They return to up or down with the first check after it reconnects. At the same moment an **incident** with the reason `agent_offline` opens (it lists the monitors that run there, and ends when the Specula is heard again), and channels that receive the event *Specula unreachable* are told. A Specula that never connected, or is revoked, opens none. |
| Revoked | The token no longer works. Its monitors show *unknown* at once. |

## Change who can use it, rename, revoke
Owners and maintainers can **Edit** a Specula (name, who can use it) and **Revoke** it. Revoking works at once: the executor is refused from its next call and the location hands out no more checks. A revoked Specula cannot be restored. Monitors that ran there stop being checked and show *unknown* until you move them to another location.

Removing a project from the list stops the Specula from handing out that project's monitors; move them first.

## Who can do what
Owners and maintainers register, edit and revoke Specula and see all of them. Members and viewers see the Specula their projects may use (without the project list) and choose them in the monitor form (members). See [Roles and permissions](../roles.md).

## Audit
`specula.created`, `specula.updated` (field names only) and `specula.revoked`. Tokens are never recorded.

## Notifications
Under **Settings → Notification channels** a Slack or webhook channel can subscribe to **Specula unreachable** and **Specula back online**; new channels do not receive them until you add them. A channel of a single project only receives events of a Specula that is shared with that project; an organization-wide channel receives all of them. In a webhook template the events are `agent_offline` and `agent_online`, `status` is `offline` or `online`, `specula.id` and `specula.name` are filled, `monitor.*` is empty and `link` opens the Specula page. Metrics (jobs): `proreta_jobs_specula_agents{status}` (pending, online, offline, revoked) and `proreta_jobs_specula_incidents_total{event}` (offline, online).

## Limits
- Going offline opens an incident and notifies; revoking does not (it is deliberate): the incident of a revoked Specula ends and no *back online* message follows. The silence threshold is fixed at 3 minutes and the check runs every minute, so the message comes 3 to 4 minutes after the last call. The incident is not reminded while it stays open.
- HTTP, HTTPS and TCP checks only.
- Proreta shows no coverage percentage yet: a partly covered day is computed over the results that exist.
- The plan limits how many Specula an organization may have (`max_agents`: `free` 1, `internal` 20, `community` unlimited). Revoked ones do not count; at the limit, registering another fails with "plan limit reached" until you revoke one. The operator can override it per organization in `tenants.limits`.
- A token has no expiry; rotate by registering a new Specula and revoking the old one.
