# Runbook: self-monitoring with alert rules

Proreta exposes counters and gauges on its metrics port (`/metrics` on the admin port, see [Observability](observability.md)). It does not evaluate alert rules itself: your Prometheus, vmalert (VictoriaMetrics) or any other ruler that reads the Prometheus rules format does, and Alertmanager routes the alerts to people. The repository ships six base rules in that format. They are templates that anybody takes and adapts, not a finished policy.

The file is `deploy/helm/proreta/alerts/proreta-rules.yaml` (a plain `groups:` file, no templating):

| Alert | Fires when | Severity | What to check |
|---|---|---|---|
| `ProretaTargetDown` | a scrape target of Proreta is down for 5 minutes | critical | the pod, the admin port, the network policy, and that the job selector matches |
| `ProretaChecksStalled` | no worker has recorded a check for 5 minutes (also when the series is gone) | critical | worker pods and logs, the database; with no active monitor at all it fires too, so silence it until you have one |
| `ProretaHousekeepingStale` | `compute_rollups` (more than 30 minutes) or `evaluate_heartbeats` (more than 5 minutes) has not succeeded | warning | the jobs pod and logs; stale `evaluate_heartbeats` means missed heartbeats open no incident |
| `ProretaOutboxBacklog` | more than 50 notifications wait for 10 minutes | warning | the jobs pod, Slack or the webhook target |
| `ProretaDeliveriesFailing` | a notification was given up in the last hour | warning | the notification channels (revoked webhook, changed URL), jobs logs |
| `ProretaApiErrors` | more than 5% of API requests are 5xx for 10 minutes | warning | api logs and the database |

The thresholds are first guesses. Change them, add rules from the suggestions in [Observability](observability.md#suggested-alert-rules), or drop what you do not need.

## 1. Adapt the job selector
Only `ProretaTargetDown` selects targets by label, with `job=~"proreta.*"` written once in the file. The `job` label comes from your scrape configuration (a Prometheus Operator ServiceMonitor names it after the Service, for example `<release>-proreta-api-metrics`; a VictoriaMetrics `VMServiceScrape` or a static config gives other names). Change the regex so it matches every Proreta process you scrape and nothing else. Check it in your Prometheus with `up{job=~"<your regex>"}`: you expect one series per api, worker, jobs (and statuspage, speculagw when enabled) pod. The other five rules use only Proreta metric names.

## 2. Load the rules
**vmalert or Prometheus, from files.** Copy `proreta-rules.yaml` next to your other rules and pass it with the `-rule` flag of vmalert (`-rule=/etc/vmalert/rules/proreta-rules.yaml`, a glob works) or list it under `rule_files` in Prometheus. Reload the ruler. The file is the same for both.

**A separate alerts repository applied by a GitOps tool such as Argo CD.** Copy the file into that repository, adapt the selector there, and let your tool apply it the way it applies your other rules (a ConfigMap mounted by vmalert, a rule object of your operator, or a file in your ruler's image). Proreta's chart is not involved; update your copy when you upgrade Proreta and the shipped file changed.

**Optional chart template.** With the Prometheus Operator CRDs installed, `prometheusRule.enabled: true` renders a `PrometheusRule` built from the same file (the chart reads it with `.Files.Get`, so there is no second copy). Values: `prometheusRule.labels` (what your Prometheus `ruleSelector` requires), `prometheusRule.interval` (evaluation interval of the group), `prometheusRule.jobSelector` (the regex above; empty matches the metrics Services of the release, named `<release>-<component>-metrics`, which is the job a ServiceMonitor from `serviceMonitor.enabled` gives them). The VictoriaMetrics operator can convert `PrometheusRule` objects into its own rule object when its converter is enabled; the chart is only checked to render a valid `PrometheusRule` (`helm lint`, schema validation in CI), and neither the conversion nor a real ruler run is part of its tests.

Check that the ruler loaded the rules (the six `Proreta*` alerts appear in its rules page, state inactive) before relying on them. If you have `promtool`, `promtool check rules proreta-rules.yaml` validates the file.

## 3. Both directions: Proreta watches Alertmanager, the rules watch Proreta
- **The rules watch Proreta.** Your Prometheus (or vmalert) with these rules and Alertmanager tell you when Proreta stops checking, stops sending, or is down.
- **Proreta watches Alertmanager.** The existing [heartbeat recipe](heartbeat-alertmanager.md) has Alertmanager ping a Proreta heartbeat from an always-firing `Watchdog` alert; Proreta alerts when the pings stop, which covers the ruler, Alertmanager and the path between them.

Each side covers the failure of the other, so set up both. Test the first direction:
1. Scale the worker to zero (`kubectl --context <your-cluster-context> -n <namespace> scale deployment <release>-proreta-worker --replicas=0`) and wait 6 minutes: `ProretaChecksStalled` fires and Alertmanager routes it.
2. Scale the worker back to its previous replica count; the alert resolves a few minutes later.

Test the second by pausing the `Watchdog` route (or its Alertmanager) past the heartbeat's expected interval plus grace: the heartbeat monitor goes down and Proreta sends its alert.

## 4. Optional: an external dead man's switch for a shared outage
If Proreta, the ruler and Alertmanager can fail together (one cluster or one network), neither direction above speaks, because both watchers are down. Only then add a third party outside that failure domain: a rule that always fires, for example

```yaml
- alert: ExternalWatchdog
  expr: vector(1)
  labels:
    severity: none
```

routed by Alertmanager (a `matchers: [alertname = "ExternalWatchdog"]` route with a short `repeat_interval`) to the receiver of any external dead man's switch you choose, which alerts you when the pings stop. Keep its URL secret as the heartbeat recipe describes. This is one optional safeguard, not part of the shipped rules, and a Proreta instance does not need to watch another one.
