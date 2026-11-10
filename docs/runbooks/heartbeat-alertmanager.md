# Runbook: heartbeat monitors with Prometheus Alertmanager

A heartbeat (watchdog) monitor is a dead man's switch: Proreta expects a ping on a secret URL at least every *expected interval*; when none arrives for the interval plus the *grace*, the monitor goes down, an incident opens and Slack is told. The next ping resolves it.

Prometheus setups have an always-firing alert for exactly this, usually called `Watchdog` (kube-prometheus-stack ships it). Alertmanager sends it to a receiver every `repeat_interval`; when Prometheus, the rules, Alertmanager or its network path break, the pings stop.

## 1. Create the heartbeat
In Proreta: **Add monitor**, choose **Watchdog**, set a name, the expected interval and the grace, and save. Or through the API:

```
POST /api/v1/monitors   {"type": "heartbeat", "name": "Prod Alertmanager", "heartbeat": {"expectedIntervalSeconds": 300, "graceSeconds": 60}}
```

The response shows the ping URL once, in `heartbeat.pingUrl`:

```
https://proreta.example.com/api/v1/heartbeat/<token>
```

Copy it now. Only a hash of the token is stored, so Proreta cannot show it again; if it is lost, regenerate it (section 5). The free plan includes one heartbeat; it does not count against the HTTPS monitors.

## 2. Configure Alertmanager
The URL is a secret: anybody who has it can keep the heartbeat green. Keep it out of Git and out of the main configuration: put it in a file (from a Kubernetes Secret, Vault and so on) and reference it with the webhook receiver's `url_file` (available in current Alertmanager releases; on an old one, keep the whole configuration file in a Secret instead).

```yaml
route:
  receiver: default
  routes:
    - receiver: proreta-heartbeat
      matchers:
        - alertname = "Watchdog"
      # Send the first ping at once and repeat well inside the expected interval.
      group_wait: 0s
      group_interval: 1m
      repeat_interval: 1m
      continue: false

receivers:
  - name: default
    # ... your normal receivers ...
  - name: proreta-heartbeat
    webhook_configs:
      - url_file: /etc/alertmanager/secrets/proreta-heartbeat/url
        # The Watchdog never resolves; a resolved notification would only be a stray ping.
        send_resolved: false
        # max_alerts: 1 keeps the (ignored) body small.
        max_alerts: 1
```

Rules of thumb:
- `group_interval` and `repeat_interval` must be **below** the expected interval, so at least one ping arrives in every interval even when one is lost. With `repeat_interval: 1m` an expected interval of 5 minutes and a grace of 1 minute tolerate four lost pings.
- Alertmanager's minimum effective repeat is `group_interval`; it cannot ping faster than that.
- Proreta accepts at most one ping per second per URL; extra pings get `429`, which Alertmanager logs and retries harmlessly.
- The ping may be `GET` or `POST`; the body is ignored. A test by hand: `curl -fsS -X POST "$(cat /path/to/url)"` answers `204`.

Kubernetes example for the file (kube-prometheus-stack: `alertmanager.alertmanagerSpec.secrets: [proreta-heartbeat]` mounts it under `/etc/alertmanager/secrets/proreta-heartbeat/`). Always name the cluster context explicitly. The command reads the URL from standard input, so it stays out of the command line, the process list and the shell history: run it, paste the URL, then end the input with Ctrl-D (press it twice if the cursor is still on the URL's line). A trailing newline is harmless, Alertmanager trims the file's content.

```
kubectl --context <your-cluster-context> -n monitoring create secret generic proreta-heartbeat --from-file=url=/dev/stdin
```

The Proreta web app shows the same configuration and command right after creating the heartbeat (with `group_interval` and `repeat_interval` set to half the expected interval, at least 30 s).

## 3. How deadlines work
- **Deadline** = the newer of the last ping and the start of the wait, plus the expected interval, plus the grace. The start of the wait is the creation time, or the time of the last resume.
- Proreta evaluates heartbeats every 30 seconds. When the deadline has passed, the heartbeat becomes **down** with the reason `heartbeat_missed`, and one incident opens, starting at the deadline. Slack gets one message (when *Notify Slack* is on and a Slack webhook is configured); a heartbeat that stays silent is not reported again.
- The next ping resolves the incident within 30 seconds, and Slack gets the "back up" message.
- **Never pinged:** a new heartbeat is *Pending* (`unknown`) until its first ping (then up) or until its first deadline passes (then down). Configure Alertmanager before the first deadline (interval plus grace after creation) to avoid a first false alarm.
- **Paused:** no evaluation, pings answer `204` but are not recorded, and an open incident is closed without a notification. **Resume** restarts the wait from that moment; the heartbeat is pending until the next ping or deadline.
- **Deleted:** pings answer `204` and change nothing; the URL is dead for good.
- An unknown, malformed or regenerated token answers `404`. Repeated unknown tokens from one address are limited (`429`); this limit never applies to a valid URL, so a misbehaving client behind the same address cannot block your pings.

## 4. Troubleshooting
| Symptom | Check |
|---|---|
| Heartbeat stays pending | Alertmanager logs for the `proreta-heartbeat` receiver; is the `Watchdog` alert firing (`ALERTS{alertname="Watchdog"}`)? Does `curl` on the URL answer `204`? |
| `404` from the URL | The token was regenerated or mistyped; regenerate and update the Secret |
| Down although Alertmanager runs | `repeat_interval`/`group_interval` above the expected interval, or the route does not match `alertname="Watchdog"` (another route catches it first without `continue`) |
| `429` in Alertmanager logs | Pings faster than once a second (several Alertmanager replicas each send; harmless), the api momentarily overloaded (`Retry-After: 1`, `proreta_heartbeat_pings_total{result="busy"}`; Alertmanager retries), or, for a wrong URL, many unknown tokens from one address |

Operators: `proreta_heartbeat_pings_total{result}` (api) and `proreta_jobs_heartbeats_missed_total`, `proreta_jobs_housekeeping_runs_total{task="evaluate_heartbeats"}` (jobs), see the [observability runbook](observability.md).

## 5. Regenerate the URL
When the URL leaked or was lost: open the heartbeat in Proreta, **Regenerate URL**, confirm. The old URL stops working immediately (pings get `404` and the heartbeat goes down after its deadline unless the new URL is deployed in time). Update the Secret with the new URL, then reload Alertmanager (`POST /-/reload`, or let your operator do it) so it surely uses the new file. The API call is `POST /api/v1/monitors/{id}/heartbeat-token` (session and CSRF token); the regeneration is recorded in the audit log without the token.
