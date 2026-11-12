# Runbook: webhook alert channels

A webhook channel sends every alert of its events to an `https` endpoint you choose, as the body of a `POST` that you write yourself with a small template. Use it for tools that accept a generic JSON or plain text call. Slack has its own channel type with a fixed message.

Owners and maintainers of a project (or of the organization, for an organization-wide channel) create channels under **Settings → Notification channels → Add channel → Webhook**. The permission is the same as for Slack channels (`channel.manage`, see [Roles and permissions](../roles.md)).

## What is sent
For each event of the channel (`down`, `recovered`, `tls_expiring`, and reminders while an incident stays open) Proreta renders the template and sends:

- `POST <your URL>`
- `Content-Type: application/json` for the content type **JSON**, `text/plain; charset=utf-8` for **Plain text**
- `User-Agent: Proreta/1 (+notifications)`
- the rendered template as the body, at most 16 KB

Any `2xx` answer is a success. The answer body is never stored or shown; only a short result such as `http_status 500` or `timeout` is recorded.

## Placeholders
Write a placeholder as `{{name}}` (spaces inside the braces are ignored). A name that does not exist is refused when the channel is saved, and the error names it. A value that does not exist for the event is an empty string.

| Name | Value |
|---|---|
| `event` | `opened`, `resolved`, `reminder`, `tls_expiring` or `test` |
| `status` | `down` (opened, reminder, test) or `up` (resolved, tls_expiring) |
| `monitor.id`, `monitor.name`, `monitor.type` | The monitor's id, name and type (`http` or `heartbeat`) |
| `monitor.url` | The monitor URL without query string or fragment (they often carry tokens) |
| `project.name`, `organization.name` | Names of the monitor's project and of the organization |
| `incident.id` | The incident id (not for `tls_expiring`) |
| `incident.startedAt`, `incident.resolvedAt` | RFC 3339, UTC; `resolvedAt` only when the incident is resolved |
| `incident.duration` | Human readable, for example `5m` or `2h 5m`; `resolved` and `reminder` only |
| `error.kind` | Why the monitor went down, for example `timeout` or `heartbeat_missed` |
| `tls.expiresAt`, `tls.daysLeft` | `tls_expiring` only |
| `link` | The monitor in the dashboard, built from the public URL of the installation |

The default JSON template is:

```json
{
  "event": "{{event}}",
  "status": "{{status}}",
  "monitor": {
    "name": "{{monitor.name}}",
    "url": "{{monitor.url}}"
  },
  "error": "{{error.kind}}",
  "link": "{{link}}"
}
```

The default text template is `[{{event}}] {{monitor.name}} is {{status}} {{monitor.url}} {{link}}`.

## Escaping
- **JSON:** every substituted value is escaped as the inside of a JSON string (`"` becomes `\"`, a backslash `\\`, a newline `\n`, control characters `\u00XX`), so a monitor called `My "API"` cannot break the document. Put placeholders inside string quotes: `"name": "{{monitor.name}}"`. A placeholder outside a string, such as `"count": {{tls.daysLeft}}`, is refused unless the result is still valid JSON, and a delivery whose rendered body is not valid JSON fails with `bad_template` instead of sending a malformed request.
- **Plain text:** values are inserted as they are.

The template is checked when the channel is saved: it must be at most 8 KB, use only known placeholders and, for JSON, render with sample data to one valid JSON document (the error names the position of the problem). The template is not a secret: it is stored as plain text and shown to everybody who can manage the channel. People who only use an organization-wide channel do not see its settings.

## Preview and test
The form shows a live preview: the template rendered with sample data (a monitor called `Example "API" monitor`). The preview makes no network call (`POST /api/v1/channels/preview-template`). **Test** on a saved channel sends the template rendered with the same sample data and `event` set to `test`; the result is reported like a Slack test (limited to one test per 5 seconds per organization).

## The URL
- It must be an `https` URL on port 443 with a public host name or address, without user name or password. Hosts such as `localhost`, `*.internal`, `*.local`, `*.svc`, private, loopback, link-local and other internal addresses are refused when the channel is saved.
- It is stored encrypted with the installation's keys, like a Slack webhook URL, and is never shown again; replace it by typing a new one. Treat the URL as the secret if your endpoint authenticates by its path or query string. It is not written to logs, audit entries or error messages.

## SSRF limits
The same guard as for monitors applies on every delivery, not only at save time. The address is checked when the connection is made, whatever the name resolved to (so DNS rebinding does not help), including the operator's `config.blockedCIDRs`. No proxy from the environment is used, and **redirects are not followed**: a `3xx` answer is a failure. Responses are not read beyond a few KB. A request takes at most 10 seconds. An endpoint that must be reached on a private network cannot be used.

## Delivery, retries and failures
Webhook deliveries use the same queue as Slack (`notification_deliveries`), written in the same transaction as the incident change.

- A network error, timeout, `5xx` or other non-2xx status is retried with backoff, up to 5 attempts, then the delivery is `failed` (the last reason is in `last_error`).
- `429` is retried after the `Retry-After` delay (at most one hour).
- `404` or `410` marks the channel `failed`: nothing more is sent until you store a new URL (which makes it `active` again).
- A delivery is not sent, and ends `failed`, when the channel was deleted (`channel_deleted`), when the monitor or the channel moved to another project so that the channel no longer serves the monitor (`route_changed`), when the incident was resolved before a reminder was due, or when the template no longer renders (`bad_template`). Alerts of a suspended organization wait as described in the [admin panel runbook](admin-panel.md).
- Delivery is at least once: after a crash an alert can arrive twice. Use `incident.id` and `event` to deduplicate.
- The metric `proreta_jobs_deliveries_total` has `channel_type="webhook"` for these channels ([Observability](observability.md)).

## Troubleshooting
| Symptom | Check |
|---|---|
| Saving says `unknown placeholder {{x}}` | The name is not in the table above; names are case-sensitive |
| Saving says the template is not valid JSON | Put placeholders inside quotes and check commas; the message gives the byte position in the sample rendering |
| Test fails with `blocked_address` or `dns` | The host resolves to a private address, or does not resolve from the jobs process |
| Test fails with `tls` | The endpoint needs a certificate chain that public CAs sign; self-signed endpoints are not supported |
| Channel shows `failed` | The endpoint answered 404 or 410; fix the URL and store it again |
| Deliveries stay `pending` | `jobs` is not running or the endpoint answers 5xx; see `proreta_jobs_outbox_pending` |
