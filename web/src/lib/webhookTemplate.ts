export type WebhookContentType = "json" | "text";

export const TEMPLATE_MAX_BYTES = 8192;
export const WEBHOOK_URL_MAX = 2048;

/** Mirrors the server list; the server rejects every other name. */
export const placeholders: { name: string; help: string }[] = [
  { name: "event", help: "opened, resolved, reminder, tls_expiring or test" },
  { name: "status", help: "down or up" },
  { name: "monitor.id", help: "Monitor id" },
  { name: "monitor.name", help: "Monitor name" },
  { name: "monitor.url", help: "Monitor URL without query string" },
  { name: "monitor.type", help: "http or heartbeat" },
  { name: "project.name", help: "Project name" },
  { name: "organization.name", help: "Organization name" },
  { name: "incident.id", help: "Incident id" },
  { name: "incident.startedAt", help: "RFC 3339, UTC" },
  { name: "incident.resolvedAt", help: "RFC 3339, UTC; empty while down" },
  { name: "incident.duration", help: "For example 5m; resolved and reminder only" },
  { name: "error.kind", help: "Why the monitor went down" },
  { name: "tls.expiresAt", help: "TLS expiry events only" },
  { name: "tls.daysLeft", help: "TLS expiry events only" },
  { name: "link", help: "The monitor in the dashboard" },
];

export const defaultTemplates: Record<WebhookContentType, string> = {
  json: `{
  "event": "{{event}}",
  "status": "{{status}}",
  "monitor": {
    "name": "{{monitor.name}}",
    "url": "{{monitor.url}}"
  },
  "error": "{{error.kind}}",
  "link": "{{link}}"
}`,
  text: "[{{event}}] {{monitor.name}} is {{status}} {{monitor.url}} {{link}}",
};

export function isDefaultTemplate(template: string): boolean {
  return template === defaultTemplates.json || template === defaultTemplates.text;
}

/** Mirrors the server rule: an https URL, port 443, no user info. The server also refuses private hosts. */
export function validateWebhookUrl(raw: string): string | undefined {
  const value = raw.trim();
  if (!value) return "Enter the webhook URL.";
  if (value.length > WEBHOOK_URL_MAX) return `Use ${WEBHOOK_URL_MAX} characters or fewer.`;
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return "Enter a full URL such as https://example.com/hook.";
  }
  if (url.protocol !== "https:") return "Use an https URL.";
  if (url.username || url.password) return "Remove the user name and password from the URL.";
  if (url.port && url.port !== "443") return "Use port 443.";
  return undefined;
}

export function validateTemplate(template: string): string | undefined {
  if (!template.trim()) return "Enter the message template.";
  if (new TextEncoder().encode(template).length > TEMPLATE_MAX_BYTES) {
    return `Use ${TEMPLATE_MAX_BYTES} bytes or fewer.`;
  }
  return undefined;
}

/** Inserts text at the selection of a textarea value; returns the new value and caret. */
export function insertAt(
  value: string,
  start: number,
  end: number,
  text: string,
): { value: string; caret: number } {
  return { value: value.slice(0, start) + text + value.slice(end), caret: start + text.length };
}
