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

export interface MonitorFormValues {
  type: MonitorKind;
  name: string;
  url: string;
  expectedMode: "2xx" | "exact";
  expectedCode: string;
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
  return errors;
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
  return {
    http: {
      url: v.url.trim(),
      expectedStatus: v.expectedMode === "2xx" ? "2xx" : v.expectedCode.trim(),
      failureThreshold: v.failureThreshold,
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
    { text: "Checks " },
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
