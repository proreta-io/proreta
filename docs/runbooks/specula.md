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

**Anywhere else**, run the `ghcr.io/proreta-io/proreta-specula` image, whose entrypoint is the executor (for example with Docker, systemd or a plain process manager). It reads two environment variables:

| Variable | Meaning |
|---|---|
| `PRORETA_URL` | The address of Proreta, for example `https://proreta.example.com` (no trailing slash). |
| `PRORETA_SPECULA_TOKEN` | The token from the registration. |

Optional: `PRORETA_SPECULA_STATE_DIR` (a writable directory for the schedule and the buffer; unset means memory only), `PRORETA_SPECULA_CONCURRENCY` (checks in flight; the binary defaults to 50, the chart sets 20 through `specula.concurrency`), `PRORETA_SPECULA_POLL_INTERVAL` (how often it asks for work, default 5s), `PRORETA_ADMIN_ADDR` (metrics, default `:9090`), `PRORETA_LOG_LEVEL`.

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
- An incident that began and ended during the outage is created with its real start and end. If the service was already healthy again by the time Proreta hears about a failure, you get one late "recovered" message and no "down" message.
- **What counts as late.** A result is late when it reaches Proreta more than `PRORETA_SPECULA_LATE_ALERT_AFTER` after its slot (default 10 minutes, 1 minute to 24 hours; chart value `speculagw.lateAlertAfter`, set on the gateway). Alerts for late results say "reported late", and webhook templates can use `{{late}}` and `{{observedAt}}` ([alert webhooks](alerts-webhook.md#placeholders)). The "down" of a late result waits 30 seconds before it is sent, so that a recovery arriving in the next call of the same replay can still cancel it. A replay that runs longer than that can send a "down" and then its "recovered": both are true as far as Proreta knew at the time. The counter `proreta_specula_late_alerts_total` shows how many were sent, skipped or failed ([observability](observability.md)).
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

In the chart the state directory is an `emptyDir` (`state.sizeLimit`, 512Mi). It survives a restart of the container but **not the replacement of the pod** (an upgrade, an eviction or a node drain). When the pod is replaced the executor first sends what it holds, for up to 20 seconds. That is enough when the buffer is small. It is not enough when a replay is still running (a replay is paced, about 100 results every 200 ms): the tail that was not sent in time is lost with the pod. When the pod is replaced while Proreta cannot be reached, the buffer and the cached schedule are lost, and the executor checks nothing until it reaches Proreta again. To keep them, put the state on a volume that outlives the pod: see [Persistent state](#persistent-state).

What a failure costs:

- **A crash or kill of the executor process** (an out-of-memory kill, a liveness restart) loses nothing that was buffered, and the state directory is still there. Only the check in flight and the seconds of downtime are missing.
- **A crash of the node** loses what the operating system had not written to disk yet, up to about a second of results, and the pod is then replaced as above.
- **Do not drain or reboot the executor's node while Proreta is unreachable.** If the executor and Proreta are restarted together (the same node, the same maintenance window, or one deployment tool), restart Proreta first and the executor after it answers. See [Argo CD](#argo-cd-and-other-tools-that-restart-both).

The cached schedule contains the decrypted headers and bodies of your monitors (tokens may be among them). Treat the state directory like the executor's own memory: do not mount it from shared storage and do not back it up.

## Persistent state
Set `state.persistence.existingClaim` to the name of a PersistentVolumeClaim in the release namespace, and the chart mounts it at the state directory instead of the `emptyDir` (the default is unchanged). Create the claim yourself, at least 1 GiB; the chart does not create one, so that removing the release never deletes your results. Use a `ReadWriteOnce` claim: together with the `Recreate` strategy and one replica it keeps a single writer, and that is the supported combination (`ReadWriteMany` is not supported). The chart sets the pod's `fsGroup` to its group so that the executor can write to the volume, unless you set `fsGroup` yourself in `podSecurityContext`.

What it fixes: the buffer, the cached schedule and the instance id survive the replacement of the pod. A Proreta and a Specula restarted together by one Argo CD sync no longer lose results. The new pod starts checking from the cached schedule before the gateway answers (while it is still valid, 24 hours by default), and it sends the buffer when the link is back, so the maintenance window leaves no gap. Keeping the sync waves from [Argo CD](#argo-cd-and-other-tools-that-restart-both) is still sensible. It does not help when the volume itself is lost, when the schedule has run out, or beyond the buffer limits. The cached schedule holds your monitors' headers and bodies, so use an encrypted storage class if that matters to you.

## What is guaranteed
Producing a result is best effort, at most once per slot: a slot is checked once or not at all, and a slot that was missed (a restart, a busy or stalled executor, an expired schedule) is never checked afterwards. Delivery to Proreta is at least once and in order for as long as the buffer lives, and Proreta stores one result per monitor, location and slot, so a resend is stored once. Beyond the buffer limits, or when the pod is replaced without a chance to send, delivery is at most once: the oldest results are dropped and counted. A result that Proreta answers with `rejected`, `superseded` or `expired` is final and is not sent again, and Proreta does not distinguish a slot that was never checked from one that was lost: both are shown as **no data**.

## Argo CD and other tools that restart both
If one Argo CD manages the cluster of Proreta and the cluster of the Specula, a single sync can replace the Proreta pods and the executor pod at the same time. If the executor pod is replaced while the gateway is down, the buffer and the cached schedule are lost and the executor starts with nothing: no checks run until the gateway answers, the gap is shown as **no data**, and after about 3 minutes the Specula shows *unreachable* and its monitors *unknown*. Nothing is wrong afterwards; the results of those minutes are simply missing.

Make the executor wait for Proreta. In an app-of-apps, give the Proreta application an earlier sync wave than the Specula application, so that Argo CD replaces the executor only once Proreta is healthy. Or sync the two applications separately, Proreta first. This is a choice for the operator; the charts do not set it. A sketch of the child `Application` resources of the parent application:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: proreta
  annotations:
    argocd.argoproj.io/sync-wave: "0"   # first
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: proreta-specula
  annotations:
    argocd.argoproj.io/sync-wave: "1"   # after the wave above is healthy
```

Argo CD applies the second wave only after everything in the first is healthy, which includes the gateway answering. Persistent state (below) is the complementary measure: with it the executor loses nothing even if it is replaced while Proreta is down.


## Degraded state
If the state directory is unusable (full, read-only, removed, or not configured) the executor does not stop. It keeps the schedule and a small number of recent results in memory and reports itself as **degraded**. In that mode:

- checks keep running while the executor is up and the schedule is valid;
- a link outage is bridged only for as long as the memory holds (about two thousand results: roughly 20 minutes for a hundred monitors at 60 s, 2 minutes for a thousand), and a restart during an outage loses the schedule, so no checks run until Proreta can be reached;
- nothing about your monitors is written to disk.

You can choose this mode on purpose with `state.enabled: false` in the chart if you do not want monitor settings on disk. Otherwise treat a degraded executor as a fault to fix (check the logs for the reason and the disk space of the node).

## Metrics, logs and alerts
The executor serves Prometheus metrics on `PRORETA_ADMIN_ADDR` (default `:9090`, `/metrics`). With the chart, `serviceMonitor.enabled=true` creates a Service for that port and a Prometheus Operator `ServiceMonitor` (add the label your Prometheus selects on under `serviceMonitor.labels`); without the Operator, scrape the pod's port 9090 yourself. The list is in the `specula (executor)` section of the [observability runbook](observability.md). The page of the Specula in Proreta shows the same state as last reported by the executor's hello every minute (schedule age, results waiting, oldest waiting result, degraded buffer).

At start the executor logs one line, `executor state at start`, with its instance id, whether a state directory is used, the results waiting in the buffer, whether the buffer is degraded, the clock offset, and the issue and expiry time and monitor count of the cached schedule (or `schedule=none`). It never logs the token or monitor settings.

The instance id is created once and kept in the state directory, so a restart keeps it. Without a state directory it changes at every start. Proreta counts distinct ids per token to notice two executors that share one token; do not copy a state directory to a second executor.

`specula_schedule_age_seconds` is absent while no schedule is loaded, so alert on `specula_schedule_loaded == 0` for that case. `specula_buffer_oldest_age_seconds` is read from the buffer at most every 15 seconds, so a scrape never reads the disk under a large backlog.

Suggested alerts (PromQL, thresholds are first guesses; the schedule lasts 24 hours unless your gateway is configured otherwise):

| Alert | Expression | For |
|---|---|---|
| Never fetched a schedule | `specula_schedule_loaded == 0` | 10m |
| Schedule getting old | `specula_schedule_age_seconds > 12 * 3600` | 5m |
| Results lost | `increase(specula_results_dropped_total[15m]) > 0` | 0m |
| Results stuck | `specula_buffer_oldest_age_seconds > 600` while the link works | 10m |
| Not checking | `specula_schedule_expired_monitors > 0` | 5m |
| Buffer in memory | `specula_buffer_degraded == 1` | 10m |
| Checks skipped, executor too slow | `increase(specula_missed_slots_total{reason=~"late\|busy"}[15m]) > 0` | 0m |
| Proreta refuses results | `increase(specula_report_outcomes_total{outcome=~"rejected\|expired"}[15m]) > 0` | 0m |
| Unreadable buffer dropped | `increase(specula_results_dropped_total{reason="unreadable"}[15m]) > 0` | 0m |

The last row means an unreadable buffer segment was dropped and its results are gone. With a persistent state volume, check that the volume is still mounted and healthy; with the default `emptyDir` this should never fire.

`specula_missed_slots_total{reason="busy"}` counts slots skipped because the previous check of the same monitor was still running, `late` slots skipped because the executor was stalled for a whole interval. Both mean it cannot keep up: size `specula.concurrency` (chart default 20) against the number of monitors and their timeouts, roughly monitors times timeout divided by interval at the very most. `specula_report_outcomes_total{outcome}` counts what Proreta answered for each reported result (`recorded`, `duplicate`, `rejected`, `superseded`, `expired`); the last three are final and the results are discarded, and they are also counted in `specula_results_dropped_total{reason="refused"}`. `superseded` after you edited or moved a monitor is normal; `rejected` and `expired` are not. Proreta shows the same losses by reason, hour and Specula under [Missed and lost checks](#missed-and-lost-checks) without scraping the executor.

## Missed and lost checks
A missing row in a monitor's history shows as **no data**, whatever the reason. A Specula of the current version tells Proreta why: in every `hello` (once a minute) it sends how many slots it never checked and how many results it gave up on, and the **Missed and lost checks** block on the Specula page shows them by reason for the last 24 hours, 7 days or 30 days (API: `GET /api/v1/specula/{speculaId}/slot-losses`, marked experimental). The gateway counts the same growth in `proreta_specula_slot_losses_total{kind,reason}`, so an alert on Proreta needs no scrape of every executor. Hourly counts are kept for 90 days.

The numbers describe the Specula in that hour, not one monitor, and say nothing about the checks that did run. They are cumulative counters since the executor process started; Proreta stores the growth between two hellos, and a restart (a new instance id) counts from zero again. **A crash loses the counts since the last hello, at most about a minute.** An executor from before this feature (or one that never connected) shows *This Specula does not report missed checks. Upgrade it.*: no losses are known, which is not the same as none.

| Kind | Reason | Meaning | What to do |
|---|---|---|---|
| not checked | `busy` | The previous check of the same monitor was still running when the slot came. | Raise `specula.concurrency` (chart default 20), lengthen the monitor's interval, or shorten its timeout. |
| not checked | `late` | The process was stalled for a whole interval (a frozen host, CPU starvation). | Look at the CPU and memory limits and at the node. |
| not checked | `scheduleExpired` | The Specula could not reach Proreta for longer than the schedule is valid (24 hours unless your gateway is configured otherwise), so it stopped checking. | Fix the network or the gateway. Checks resume as soon as it fetches a schedule. |
| checked, then dropped | `bufferSize` | The buffer reached its size limit while Proreta was unreachable (also: the in-memory buffer of a degraded executor was full). | Fix the link; give the executor a persistent volume and a larger buffer if outages are long. |
| checked, then dropped | `bufferAge` | A result waited longer than the buffer keeps it (24 hours by default). | Fix the link to Proreta. |
| checked, then dropped | `rejected` | Proreta refused the result for good (malformed, or one the executor could not send). | Check the clock of the host and the executor's logs. |
| checked, then dropped | `superseded` | The monitor was edited, paused, moved or deleted while its results were waiting. | Normal after a change made during an outage. |
| checked, then dropped | `tooOld` | The result was older than the backlog Proreta accepts. | Check the clock of the host and how long the outage lasted. |
| checked, then dropped | `unreadable` | Part of the buffer on disk could not be read. | Check the state directory and the disk of the node; see [Degraded state](#degraded-state). |

Upgrade order: upgrade the gateway first. A new executor against an older gateway announces both `schedule/1` and `schedule/2`, is offered `schedule/1` only, never sends the counters and keeps checking in schedule mode. A gateway rolled back while an executor is sending the counters answers `400`; the executor then stops sending them until it restarts, resends a plain `hello` and stays in schedule mode.

## Warnings on the Specula page
- **Clock skew** appears when the executor's clock differs from Proreta's by more than 30 seconds (the executor measures it at every `hello`; the API field is `clockOffsetMs`). Results more than 30 seconds in the future are rejected, and results from a clock that is behind arrive late. Fix the time on the executor's host (NTP).
- **Two executors** (`duplicateInstance`) appears when two executors with different instance ids said hello with the same token within 3 minutes of each other, and lasts 3 minutes after the last such hello. Run one executor per Specula (the chart does: one replica, strategy `Recreate`). A restart keeps its instance id, and a replaced pod that comes up more than 3 minutes after the old one stopped does not trigger it. The gateway counts the event in `proreta_specula_duplicate_instance_total`.

## Upgrade and downgrade
**Upgrade.** Upgrade the executor chart or image. No setting is needed in Proreta. When it starts, the executor tells Proreta what it supports, fetches its schedule and takes over from the next slot. From then on Proreta hands that Specula no more checks to claim, so no check runs twice. A check that was already claimed by the old executor finishes within 30 seconds. The gateway must be upgraded first or at the same time: a new executor that finds an older gateway (no schedule endpoint) stays in the previous mode, in which it claims one check at a time as before.

**Protocol rule (for contributors and for mixed versions).** A `schedule/1` request or response never gains a field; a new field needs a new capability. `schedule/2` is `schedule/1` plus the counters of missed and dropped slots in `hello` (see [Missed and lost checks](#missed-and-lost-checks)); the executor sends the field only after the gateway offered `schedule/2`. Upgrade the gateway first. An executor that sends a field the gateway does not know gets `400` and keeps its buffer, retrying, so skew between versions stalls delivery instead of dropping results. A test in the wire package records the field names of each capability and fails when one is added without a new capability. `tcp/probe` adds the `probe` object to a tcp job and to a scheduled tcp monitor; the server includes it only for an executor that announced the capability, and a monitor that has one is not handed to any other executor.

**Downgrade.** Install the previous chart or image. The old executor does not announce the schedule capability, so Proreta serves it checks to claim again from its first call. There is nothing to clean up in Proreta, and results the new executor had not yet delivered are lost with its state directory. An executor of the previous version also does not run TCP monitors, and Proreta does not hand it any; they show *unknown* until you upgrade again. The same holds for a TCP monitor that sends bytes or expects a reply (capability `tcp/probe`): an executor older than this feature is not handed it, so the monitor shows *unknown* instead of passing on a bare connection.

The chart runs a single pod per Specula and replaces it only after the old one has stopped: two executors for the same Specula are reported as duplicates.

**The gap during an upgrade.** A rollout takes the old pod down first and only then pulls the new image and starts the new pod. In a slow network the pull alone can take minutes. Above 3 minutes without a hello the Specula shows *unreachable* and its monitors *unknown*, the *Specula unreachable* incident opens and notifies, although nothing is lost: the checks of the gap are missing (shown as **no data**) and nothing else. In slow networks pre-pull the image on the nodes or pull through a registry mirror, so that the gap is the start of the process only.

## Behind a gateway or proxy
Operators, for the [Specula gateway](#enable-the-gateway) on the Proreta side:

- **Rate limits.** The gateway limits how often one token may call it: reports 5 per second (burst 20, which fits the replay rate), claim and results of the older pull mode 5 per second each (burst 20), schedule one per 10 seconds (burst 3) and hello 2 per minute, per gateway replica. The metrics are in the `speculagw` section of the observability runbook (`proreta_specula_token_limited_total`, `proreta_specula_reports_total`, `proreta_specula_schedule_unreadable_monitors_total`). A call over the limit gets **429 Too Many Requests** with a `Retry-After` header (seconds), and nothing is stored. The executor waits at least that long, adds a random delay, and sends the same batch again; the same applies to `503` when the gateway is busy. Proxies and load balancers in front of the gateway must pass the status and the header through unchanged and must not retry on their own.
- **Client address.** Failed token checks are limited per client address. Set `config.trustedProxyHops` (`PRORETA_TRUSTED_PROXY_HOPS`) to the number of proxies between the executors and the gateway. The chart default is `1`, which is right for the Gateway of `httpRoute`. With `0` behind a proxy every executor appears to come from the proxy's address, so a few wrong tokens can lock out all of them; with a number larger than the real one a client can choose its own address by sending `X-Forwarded-For`. Use `0` only when nothing sits in front of the gateway.
- **Timeouts.** A batch of results is a normal POST of up to 100 results; proxy timeouts of a few seconds are enough. Do not limit the request rate of `/specula/v1` below what the limits above allow, or a replay after an outage will be slowed down.

## Use it in a monitor
When a project may use at least one Specula, the monitor form has a **Run from** field: *Proreta cloud* or one of them.

- A TCP monitor ([TCP monitors](../tcp-monitors.md)) may run at a Specula too: its host may be any name or address your network resolves, on any port. Upgrade the executor before you place TCP monitors on it. A TCP monitor that sends bytes and expects a reply runs only on an executor of the release that added it (capability `tcp/probe`); until you upgrade, the monitor shows *unknown*. Upgrade the server first, then the executors.
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
- The plan limits how many Specula an organization may have (`max_agents`, unlimited on the `internal` plan). Revoked ones do not count; at the limit, registering another fails with "plan limit reached" until you revoke one. The operator can override it per organization in `tenants.limits`.
- A token has no expiry; rotate by registering a new Specula and revoking the old one.
