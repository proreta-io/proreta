import { ApiError, errorMessage } from "../api/client";
import type { Channel, MonitorCreate, MonitorDetail, MonitorUpdate } from "../api/endpoints";
import { formatDuration } from "./format";
import {
  DEFAULT_GRACE,
  DEFAULT_INTERVAL,
  GRACE_MAX,
  INTERVAL_MAX,
  INTERVAL_MIN,
} from "./heartbeat";

export type MonitorKind = "http" | "heartbeat";

export const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"] as const;
export type Method = (typeof METHODS)[number];
/** Methods that may carry a body; the others are read-only by convention. */
export const BODY_METHODS: readonly Method[] = ["POST", "PUT", "PATCH", "DELETE"];
export const isSafeMethod = (m: Method) => m === "GET" || m === "HEAD";
export const MAX_HEADERS = 10;
export const BODY_MAX_BYTES = 64 * 1024;
const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const DENIED_HEADER =
  /^(host|content-length|transfer-encoding|connection|upgrade|te|trailer|expect|proxy-.*)$/i;

/**
 * One custom header row. A stored header keeps its secret value on the server: its `value` stays
 * empty unless the user types a new one.
 */
export interface HeaderRow {
  name: string;
  value: string;
  /** The monitor already has a value stored for this header name. */
  stored: boolean;
}

export interface MonitorFormValues {
  type: MonitorKind;
  name: string;
  url: string;
  expectedMode: "2xx" | "exact";
  expectedCode: string;
  method: Method;
  headers: HeaderRow[];
  /** The request body as typed; only sent when `bodyDirty`. */
  body: string;
  /** The monitor stores a body (hidden); it is kept unless `bodyDirty`. */
  bodyStored: boolean;
  bodyDirty: boolean;
  failureThreshold: number;
  notificationsEnabled: boolean;
  /** Selected notification channels; none means the active channels of the project. */
  channelIds: string[];
  /** The monitor's project; empty lets the server use the Default project. */
  projectId: string;
  /** Heartbeat fields, as typed (seconds). */
  expectedIntervalSeconds: string;
  graceSeconds: string;
}

export const formFields = [
  "type",
  "name",
  "url",
  "expectedStatus",
  "method",
  "headers",
  "body",
  "failureThreshold",
  "notificationsEnabled",
  "channelIds",
  "projectId",
  "expectedIntervalSeconds",
  "graceSeconds",
] as const;
export type FormField = (typeof formFields)[number];
export type MonitorFormErrors = Partial<Record<FormField, string>>;

/** The form field of a server field error; the type's settings are in `http` and `heartbeat`. */
const serverFields: Record<string, FormField> = {
  "http.url": "url",
  "http.expectedStatus": "expectedStatus",
  "http.method": "method",
  "http.headers": "headers",
  "http.body": "body",
  "http.failureThreshold": "failureThreshold",
  "heartbeat.expectedIntervalSeconds": "expectedIntervalSeconds",
  "heartbeat.graceSeconds": "graceSeconds",
};

export const NAME_MAX = 80;
export const URL_MAX = 2048;

/** `2xx`, an exact code 200-299, or an exact code 400-599 (1xx and 3xx are rejected). */
export const EXPECTED_STATUS_PATTERN = /^(2xx|2[0-9]{2}|[45][0-9]{2})$/;
export const EXPECTED_STATUS_MESSAGE = "Use 2xx, a code from 200 to 299, or a code from 400 to 599";

export const emptyMonitorForm: MonitorFormValues = {
  type: "http",
  name: "",
  url: "",
  expectedMode: "2xx",
  expectedCode: "200",
  method: "GET",
  headers: [],
  body: "",
  bodyStored: false,
  bodyDirty: false,
  failureThreshold: 2,
  notificationsEnabled: true,
  channelIds: [],
  projectId: "",
  expectedIntervalSeconds: String(DEFAULT_INTERVAL),
  graceSeconds: String(DEFAULT_GRACE),
};

export function valuesFromMonitor(m: MonitorDetail): MonitorFormValues {
  const expected = m.http?.expectedStatus ?? "2xx";
  const exact = expected !== "2xx";
  return {
    type: m.type,
    name: m.name,
    url: m.http?.url ?? "",
    expectedMode: exact ? "exact" : "2xx",
    expectedCode: exact ? expected : "200",
    method: m.http?.method ?? "GET",
    headers: (m.http?.headerNames ?? []).map((name) => ({ name, value: "", stored: true })),
    body: "",
    bodyStored: m.http?.bodySet ?? false,
    bodyDirty: false,
    failureThreshold: m.http?.failureThreshold ?? emptyMonitorForm.failureThreshold,
    notificationsEnabled: m.notificationsEnabled,
    channelIds: m.channelIds,
    projectId: m.projectId,
    expectedIntervalSeconds: String(m.heartbeat?.expectedIntervalSeconds ?? DEFAULT_INTERVAL),
    graceSeconds: String(m.heartbeat?.graceSeconds ?? DEFAULT_GRACE),
  };
}

/**
 * Drops selected channels that are not in the live channel list (deleted since the monitor was
 * loaded). Without a loaded list the selection is sent unchanged and the server decides.
 */
export function keepLiveChannels(
  v: MonitorFormValues,
  live: Channel[] | undefined,
): MonitorFormValues {
  if (!live) return v;
  const ids = new Set(live.map((c) => c.id));
  return { ...v, channelIds: v.channelIds.filter((id) => ids.has(id)) };
}

/** A whole number of seconds within [min, max], or undefined. */
function seconds(raw: string, min: number, max: number): number | undefined {
  const value = raw.trim();
  if (!/^[0-9]+$/.test(value)) return undefined;
  const n = Number(value);
  return n >= min && n <= max ? n : undefined;
}

/** Mirrors the server's URL policy that can be checked without DNS: https, no credentials, port 443. */
export function validateMonitorUrl(raw: string): string | undefined {
  const value = raw.trim();
  if (!value) return "Enter a URL.";
  if (value.length > URL_MAX) return `Use ${URL_MAX} characters or fewer.`;
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return "Enter a full URL, such as https://example.com/health.";
  }
  if (url.protocol !== "https:") return "Use an https:// URL.";
  if (!url.hostname) return "Enter a host name.";
  if (url.username || url.password) return "Remove the user name and password from the URL.";
  // URL drops the default port, so any remaining port is not 443.
  if (url.port !== "") return "Only port 443 is allowed.";
  return undefined;
}

export function validateMonitorForm(v: MonitorFormValues): MonitorFormErrors {
  const errors: MonitorFormErrors = {};
  const name = v.name.trim();
  if (!name) errors.name = "Enter a name.";
  else if ([...name].length > NAME_MAX) errors.name = `Use ${NAME_MAX} characters or fewer.`;
  if (v.type === "heartbeat") {
    if (seconds(v.expectedIntervalSeconds, INTERVAL_MIN, INTERVAL_MAX) === undefined) {
      errors.expectedIntervalSeconds = `Enter whole seconds from ${INTERVAL_MIN} to ${INTERVAL_MAX}.`;
    }
    if (seconds(v.graceSeconds, 0, GRACE_MAX) === undefined) {
      errors.graceSeconds = `Enter whole seconds from 0 to ${GRACE_MAX}.`;
    }
    return errors;
  }
  const urlError = validateMonitorUrl(v.url);
  if (urlError) errors.url = urlError;
  if (v.expectedMode === "exact" && !EXPECTED_STATUS_PATTERN.test(v.expectedCode.trim())) {
    errors.expectedStatus = EXPECTED_STATUS_MESSAGE;
  }
  if (![1, 2, 3].includes(v.failureThreshold)) {
    errors.failureThreshold = "Choose 1, 2 or 3 failed checks.";
  }
  const headersError = validateHeaders(v.headers);
  if (headersError) errors.headers = headersError;
  const allowsBody = BODY_METHODS.includes(v.method);
  if (!allowsBody && (v.bodyDirty ? v.body !== "" : v.bodyStored || v.body !== "")) {
    errors.body = `A ${v.method} request cannot have a body. Choose another method or remove the body.`;
  } else if (new TextEncoder().encode(v.body).length > BODY_MAX_BYTES) {
    errors.body = "Use 64 KiB or less.";
  }
  return errors;
}

/** Mirrors the server's header rules that can be checked without it. */
export function validateHeaders(rows: HeaderRow[]): string | undefined {
  if (rows.length > MAX_HEADERS) return `Use at most ${MAX_HEADERS} headers.`;
  const seen = new Set<string>();
  for (const [i, r] of rows.entries()) {
    const name = r.name.trim();
    const n = i + 1;
    if (!name || name.length > 64 || !HEADER_NAME.test(name)) {
      return `Header ${n}: enter a valid header name, such as Authorization.`;
    }
    if (DENIED_HEADER.test(name)) return `Header ${n}: ${name} cannot be set.`;
    if (seen.has(name.toLowerCase())) return `Header ${n}: ${name} is already set.`;
    seen.add(name.toLowerCase());
    if (!r.stored && r.value === "") return `Header ${n}: enter a value.`;
    if (r.value.length > 2048) return `Header ${n}: use 2048 characters or fewer for the value.`;
    // eslint-disable-next-line no-control-regex -- the server refuses control characters in values.
    if (/[\u0000-\u0008\u000a-\u001f\u007f]/.test(r.value)) {
      return `Header ${n}: the value must not contain line breaks or control characters.`;
    }
  }
  return undefined;
}

/** The settings sub-object of the form's type, as create and PATCH send it. */
function settings(
  v: MonitorFormValues,
): Pick<MonitorCreate, "http"> | Pick<MonitorCreate, "heartbeat"> {
  if (v.type === "heartbeat") {
    return {
      heartbeat: {
        expectedIntervalSeconds: Number(v.expectedIntervalSeconds.trim()),
        graceSeconds: Number(v.graceSeconds.trim()),
      },
    };
  }
  const send: Pick<NonNullable<MonitorCreate["http"]>, "method" | "headers" | "body"> = {
    method: v.method,
    headers: v.headers.map((h) => {
      const name = h.name.trim();
      // A stored header with no new value keeps what the server has.
      return h.stored && h.value === "" ? { name } : { name, value: h.value };
    }),
  };
  if (v.bodyDirty || (!v.bodyStored && v.body !== "")) send.body = v.body;
  return {
    http: {
      url: v.url.trim(),
      expectedStatus: v.expectedMode === "2xx" ? "2xx" : v.expectedCode.trim(),
      failureThreshold: v.failureThreshold,
      ...send,
    },
  };
}

/** The PATCH body: every field of the form, the settings in the sub-object of the type. */
export function toMonitorRequest(v: MonitorFormValues): MonitorUpdate {
  return {
    name: v.name.trim(),
    notificationsEnabled: v.notificationsEnabled,
    channelIds: v.channelIds,
    ...(v.projectId ? { projectId: v.projectId } : {}),
    ...settings(v),
  };
}

/**
 * Moving to another project keeps only the selected channels that a monitor of that project may
 * use: organization-wide ones and the new project's. Unknown ids are kept for the server to
 * decide.
 */
export function channelsForProject(
  selected: string[],
  projectId: string,
  known: Channel[] | undefined,
): string[] {
  return selected.filter((id) => {
    const c = known?.find((k) => k.id === id);
    return !c || c.projectId === null || c.projectId === projectId;
  });
}

/** The create body: the type and its settings sub-object. */
export function toCreateRequest(v: MonitorFormValues): MonitorCreate {
  return {
    type: v.type,
    name: v.name.trim(),
    notificationsEnabled: v.notificationsEnabled,
    channelIds: v.channelIds,
    ...(v.projectId ? { projectId: v.projectId } : {}),
    ...settings(v),
  };
}

/**
 * Splits a failed save into messages for form fields and one general message. Field errors for
 * fields the form does not show (for example `http.intervalSeconds`) go into the general message.
 */
export function mapServerErrors(err: unknown): {
  fields: MonitorFormErrors;
  general?: string;
} {
  if (!(err instanceof ApiError)) return { fields: {}, general: errorMessage(err) };
  const fields: MonitorFormErrors = {};
  const other: string[] = [];
  for (const fe of err.fieldErrors) {
    const header = /^http\.headers\[(\d+)\]\.(name|value)$/.exec(fe.field);
    if (header) {
      fields.headers ??= `Header ${Number(header[1]) + 1}: ${fe.message}`;
      continue;
    }
    const field = Object.hasOwn(serverFields, fe.field)
      ? serverFields[fe.field]
      : formFields.find((f) => f === fe.field);
    if (field) fields[field] ??= capitalize(fe.message);
    else other.push(`${fe.field}: ${fe.message}`);
  }
  if (other.length > 0) return { fields, general: other.join(" ") };
  if (Object.keys(fields).length > 0) return { fields };
  return { fields, general: errorMessage(err) };
}

const capitalize = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

/** "https://" plus what the user typed, unless they typed a scheme of their own. */
export function withHttps(typed: string): string {
  const value = typed.trim();
  if (!value) return "";
  return /^[a-z][a-z0-9+.-]*:\/\//i.test(value) ? value : `https://${value}`;
}

/** The URL as the field shows it, next to its fixed "https://". */
export const withoutHttps = (url: string) => url.replace(/^https:\/\//i, "");

/**
 * The setup in one plain sentence, as the user fills in the form. Parts not filled in yet are
 * `blank`, so the page can show them as placeholders.
 */
export function describeSetup(v: MonitorFormValues): { text: string; blank?: boolean }[] {
  const slack = v.notificationsEnabled;
  if (v.type === "heartbeat") {
    const interval = Number(v.expectedIntervalSeconds);
    const grace = Number(v.graceSeconds);
    const intervalOk = /^[0-9]+$/.test(v.expectedIntervalSeconds.trim()) && interval > 0;
    const graceOk = /^[0-9]+$/.test(v.graceSeconds.trim());
    return [
      { text: "Expects a ping every " },
      intervalOk ? { text: formatDuration(interval) } : { text: "…", blank: true },
      { text: slack ? " and alerts Slack " : " and marks it down " },
      ...(graceOk && grace === 0
        ? [{ text: "the moment one is late." }]
        : [
            { text: "if one is more than " },
            graceOk ? { text: formatDuration(grace) } : { text: "…", blank: true },
            { text: " late." },
          ]),
    ];
  }
  const host = withoutHttps(v.url.trim()).replace(/\/$/, "");
  const exact = v.expectedMode === "exact";
  return [
    { text: v.method === "GET" ? "Checks " : `Sends ${v.method} to ` },
    host ? { text: host } : { text: "your URL", blank: true },
    { text: " every minute" },
    ...(exact
      ? [
          { text: ", expects " },
          EXPECTED_STATUS_PATTERN.test(v.expectedCode.trim())
            ? { text: `HTTP ${v.expectedCode.trim()}` }
            : { text: "a status code", blank: true },
          { text: "," },
        ]
      : []),
    { text: slack ? " and alerts Slack after " : " and marks it down after " },
    {
      text:
        v.failureThreshold === 1
          ? "the first failure."
          : `${v.failureThreshold} failures in a row.`,
    },
  ];
}
