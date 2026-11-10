import type { HeartbeatMonitor, MonitorListItem } from "../api/endpoints";
import { formatAgo, formatDuration, formatSince, secondsBetween } from "./format";

/**
 * When the watchdog goes down without another ping: the last ping plus interval and grace. Null
 * before the first ping, since the start of the wait is not in the API.
 */
export function heartbeatDeadline(m: Pick<MonitorListItem, "heartbeat" | "paused">): number | null {
  const hb = m.heartbeat;
  if (m.paused || !hb?.lastPingAt) return null;
  return Date.parse(hb.lastPingAt) + (hb.expectedIntervalSeconds + hb.graceSeconds) * 1000;
}

/** Bounds of a heartbeat; the server checks them too. */
export const INTERVAL_MIN = 60;
export const INTERVAL_MAX = 86_400;
export const GRACE_MAX = 3_600;
export const DEFAULT_INTERVAL = 300;
export const DEFAULT_GRACE = 60;

export const INTERVAL_PRESETS = [
  { label: "1 min", seconds: 60 },
  { label: "5 min", seconds: 300 },
  { label: "15 min", seconds: 900 },
] as const;

export const isHeartbeat = (m: Pick<MonitorListItem, "type">) => m.type === "heartbeat";

/** "Ping every 5 min, grace 1 min". */
export function expectationText(
  hb: Pick<HeartbeatMonitor, "expectedIntervalSeconds" | "graceSeconds">,
): string {
  return `Ping every ${formatDuration(hb.expectedIntervalSeconds)}, grace ${formatDuration(hb.graceSeconds)}`;
}

/** The status line of a heartbeat's details page. */
export function heartbeatStatusLine(
  m: Pick<MonitorListItem, "paused" | "status" | "statusSince" | "heartbeat">,
  now: number,
): string {
  if (m.paused) return "Paused. Pings are ignored until you resume the monitor.";
  if (m.status === "unknown") return "Waiting for the first ping";
  const duration = formatDuration(secondsBetween(m.statusSince, now));
  const since = `since ${formatSince(m.statusSince, now)}`;
  if (m.status === "up") {
    return `Up for ${duration} · last ping ${formatAgo(m.heartbeat?.lastPingAt, now)}`;
  }
  return `Down for ${duration} · no ping in time · ${since}`;
}

/**
 * Alertmanager's repeat interval for a heartbeat: half the expected interval, at least 30 s, so a
 * lost ping does not make the heartbeat late.
 */
export function repeatInterval(expectedSeconds: number): string {
  return `${Math.max(30, Math.floor(expectedSeconds / 2))}s`;
}

/**
 * The Alertmanager route and receivers for the Watchdog alert, structured exactly like the
 * runbook (docs/runbooks/heartbeat-alertmanager.md); the URL itself lives in a file.
 */
export function alertmanagerSnippet(expectedSeconds: number): string {
  const repeat = repeatInterval(expectedSeconds);
  return `route:
  receiver: default
  routes:
    - receiver: proreta-heartbeat
      matchers:
        - alertname = "Watchdog"
      # Send the first ping at once and repeat well inside the expected interval.
      group_wait: 0s
      group_interval: ${repeat}
      repeat_interval: ${repeat}
      continue: false

receivers:
  - name: default
    # ... your normal receivers ...
  - name: proreta-heartbeat
    webhook_configs:
      # The URL is a secret: keep it in a file, not in this config.
      - url_file: /etc/alertmanager/secrets/proreta-heartbeat/url
        # The Watchdog never resolves; a resolved notification would only be a stray ping.
        send_resolved: false
        # max_alerts: 1 keeps the (ignored) body small.
        max_alerts: 1`;
}

/**
 * Creates the Kubernetes Secret for url_file. The URL is read from standard input (paste it, then
 * end the input with Ctrl-D), so it never appears on the command line or in the shell history;
 * the context is explicit so the command cannot hit whatever cluster is current.
 */
export function secretCommand(): string {
  return "kubectl --context <your-cluster-context> -n monitoring create secret generic proreta-heartbeat --from-file=url=/dev/stdin";
}
