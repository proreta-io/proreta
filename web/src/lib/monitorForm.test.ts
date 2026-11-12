import { ApiError, PLAN_LIMIT_PROBLEM_TYPE } from "../api/client";
import {
  emptyMonitorForm,
  mapServerErrors,
  toCreateRequest,
  toMonitorRequest,
  validateMonitorForm,
  validateMonitorUrl,
  describeSetup,
  withHttps,
  withoutHttps,
  type MonitorFormValues,
} from "./monitorForm";
import { validateSlackUrl } from "./slack";

describe("validateMonitorUrl", () => {
  it.each([
    ["https://example.com/health", undefined],
    ["https://example.com:443/health", undefined],
    ["  https://example.com  ", undefined],
    ["", "Enter a URL."],
    ["example.com", "Enter a full URL, such as https://example.com/health."],
    ["http://example.com", "Use an https:// URL."],
    ["ftp://example.com", "Use an https:// URL."],
    ["https://user:pass@example.com", "Remove the user name and password from the URL."],
    ["https://user@example.com", "Remove the user name and password from the URL."],
    ["https://example.com:8443/", "Only port 443 is allowed."],
    [`https://example.com/${"a".repeat(2048)}`, "Use 2048 characters or fewer."],
  ])("%s -> %s", (url, expected) => {
    expect(validateMonitorUrl(url)).toBe(expected);
  });
});

describe("validateMonitorForm", () => {
  const valid = { ...emptyMonitorForm, name: "Website", url: "https://example.com" };

  it("accepts a valid form", () => {
    expect(validateMonitorForm(valid)).toEqual({});
  });

  it("checks the name length after trimming", () => {
    expect(validateMonitorForm({ ...valid, name: "   " }).name).toBe("Enter a name.");
    expect(validateMonitorForm({ ...valid, name: "a".repeat(80) }).name).toBeUndefined();
    expect(validateMonitorForm({ ...valid, name: "a".repeat(81) }).name).toBe(
      "Use 80 characters or fewer.",
    );
  });

  it("checks an exact expected status code", () => {
    const exact = { ...valid, expectedMode: "exact" as const };
    expect(validateMonitorForm({ ...exact, expectedCode: "204" })).toEqual({});
    expect(validateMonitorForm({ ...exact, expectedCode: "99" }).expectedStatus).toBeDefined();
    expect(validateMonitorForm({ ...exact, expectedCode: "301" }).expectedStatus).toBeDefined();
    expect(validateMonitorForm({ ...exact, expectedCode: "600" }).expectedStatus).toBeDefined();
    expect(validateMonitorForm({ ...valid, expectedCode: "x" })).toEqual({});
  });

  it.each(["2xx", "200", "204", "299", "400", "404", "599"])(
    "accepts expected status %s",
    (code) => {
      const exact = { ...valid, expectedMode: "exact" as const, expectedCode: code };
      expect(validateMonitorForm(exact)).toEqual({});
    },
  );

  it.each(["100", "101", "199", "300", "301", "399", "600", "20", "2000", "abc", ""])(
    "rejects expected status %j",
    (code) => {
      const exact = { ...valid, expectedMode: "exact" as const, expectedCode: code };
      expect(validateMonitorForm(exact).expectedStatus).toBe(
        "Use 2xx, a code from 200 to 299, or a code from 400 to 599",
      );
    },
  );

  it("counts the name in code points", () => {
    expect(validateMonitorForm({ ...valid, name: "😀".repeat(80) }).name).toBeUndefined();
    expect(validateMonitorForm({ ...valid, name: "😀".repeat(81) }).name).toBe(
      "Use 80 characters or fewer.",
    );
  });

  it("builds the request body", () => {
    expect(
      toMonitorRequest({ ...valid, name: " Site ", expectedMode: "exact", expectedCode: "204" }),
    ).toEqual({
      name: "Site",
      http: {
        url: "https://example.com",
        expectedStatus: "204",
        failureThreshold: 2,
        method: "GET",
        headers: [],
      },
      notificationsEnabled: true,
      channelIds: [],
      location: { type: "cloud" },
    });
  });

  it("sends the location: always on edit, only for a Specula on create", () => {
    const atSpecula = { ...valid, speculaId: "s1" };
    expect(toMonitorRequest(atSpecula).location).toEqual({ type: "specula", speculaId: "s1" });
    expect(toCreateRequest(atSpecula).location).toEqual({ type: "specula", speculaId: "s1" });
    expect(toCreateRequest(valid)).not.toHaveProperty("location");
    expect(toMonitorRequest({ ...valid, type: "heartbeat" })).not.toHaveProperty("location");
  });

  it("checks the URL with the rules of where the monitor runs", () => {
    const private_ = { ...valid, url: "http://orders.shop.svc:8080/health" };
    expect(validateMonitorForm(private_).url).toBe("Use an https:// URL.");
    expect(validateMonitorForm({ ...private_, speculaId: "s1" }).url).toBeUndefined();
    expect(validateMonitorUrl("https://example.com:8443/", true)).toBeUndefined();
    expect(validateMonitorUrl("https://example.com:8443/")).toBe("Only port 443 is allowed.");
    expect(validateMonitorUrl("ftp://svc/", true)).toBe("Use an http:// or https:// URL.");
    expect(validateMonitorUrl("http://u:p@svc/", true)).toBe(
      "Remove the user name and password from the URL.",
    );
  });

  it("sends a stored header by name only and a body only when it was touched", () => {
    const stored = { ...valid, method: "POST" as const, bodyStored: true };
    const kept = toMonitorRequest({
      ...stored,
      headers: [
        { name: "Authorization", value: "", stored: true },
        { name: "X-New", value: "v", stored: false },
        { name: "X-Replaced", value: "new", stored: true },
      ],
    });
    expect(kept.http?.headers).toEqual([
      { name: "Authorization" },
      { name: "X-New", value: "v" },
      { name: "X-Replaced", value: "new" },
    ]);
    expect(kept.http).not.toHaveProperty("body");
    expect(toMonitorRequest({ ...stored, bodyDirty: true, body: "" }).http?.body).toBe("");
    expect(toMonitorRequest({ ...valid, method: "POST", body: "x" }).http?.body).toBe("x");
  });

  it("checks the method, headers and body", () => {
    const row = (name: string, value = "v") => ({ name, value, stored: false });
    const errs = (over: Partial<MonitorFormValues>) => validateMonitorForm({ ...valid, ...over });
    expect(errs({ method: "POST", headers: [row("Authorization")], body: "{}" })).toEqual({});
    expect(errs({ body: "x" }).body).toMatch(/GET request cannot have a body/);
    expect(errs({ method: "HEAD", bodyStored: true }).body).toMatch(/HEAD request/);
    expect(
      errs({ method: "HEAD", bodyStored: true, bodyDirty: true, body: "" }).body,
    ).toBeUndefined();
    expect(errs({ method: "POST", body: "a".repeat(64 * 1024 + 1) }).body).toBe(
      "Use 64 KiB or less.",
    );
    expect(errs({ headers: [row("Bad Name")] }).headers).toMatch(/Header 1: enter a valid/);
    expect(errs({ headers: [row("Host")] }).headers).toBe("Header 1: Host cannot be set.");
    expect(errs({ headers: [row("proxy-authorization")] }).headers).toMatch(/cannot be set/);
    expect(errs({ headers: [row("X-A"), row("x-a")] }).headers).toBe(
      "Header 2: x-a is already set.",
    );
    expect(errs({ headers: [row("X-A", "a\nb")] }).headers).toMatch(/control characters/);
    expect(errs({ headers: [row("X-A", "")] }).headers).toBe("Header 1: enter a value.");
    expect(errs({ headers: [{ name: "X-A", value: "", stored: true }] }).headers).toBeUndefined();
    expect(errs({ headers: Array.from({ length: 11 }, (_, i) => row(`X-${i}`)) }).headers).toBe(
      "Use at most 10 headers.",
    );
  });

  it("describes a non-GET method", () => {
    const text = describeSetup({ ...valid, method: "POST" })
      .map((p) => p.text)
      .join("");
    expect(text).toMatch(/^Sends POST to example\.com every minute/);
  });
});

describe("mapServerErrors", () => {
  const apiError = (status: number, fieldErrors: { field: string; message: string }[]) =>
    new ApiError({ type: "x", title: "Validation failed", status, fieldErrors });

  it("maps header errors to the headers", () => {
    const mapped = mapServerErrors(
      new ApiError({
        type: "x",
        title: "Validation failed",
        status: 422,
        fieldErrors: [
          { field: "http.headers[1].value", message: "must not contain control characters" },
          { field: "http.body", message: "is not allowed with GET or HEAD" },
        ],
      }),
    );
    expect(mapped.fields.headers).toBe("Header 2: must not contain control characters");
    expect(mapped.fields.body).toBe("Is not allowed with GET or HEAD");
  });

  it("maps a Specula error to the Run from field", () => {
    const mapped = mapServerErrors(
      new ApiError({
        type: "x",
        title: "Validation failed",
        status: 422,
        fieldErrors: [
          {
            field: "location.speculaId",
            message: "does not exist or is not available to this project",
          },
        ],
      }),
    );
    expect(mapped.fields.speculaId).toBe("Does not exist or is not available to this project");
  });

  it("puts known field errors next to fields", () => {
    expect(
      mapServerErrors(
        apiError(422, [
          { field: "http.url", message: "host resolves to a private address" },
          { field: "name", message: "is required" },
          { field: "heartbeat.graceSeconds", message: "must be between 0 and 3600 seconds" },
        ]),
      ),
    ).toEqual({
      fields: {
        url: "Host resolves to a private address",
        name: "Is required",
        graceSeconds: "Must be between 0 and 3600 seconds",
      },
    });
  });

  it("reports unknown fields in the general message", () => {
    const mapped = mapServerErrors(
      apiError(422, [
        { field: "http.intervalSeconds", message: "60" },
        { field: "constructor", message: "x" },
      ]),
    );
    expect(mapped.general).toBe("http.intervalSeconds: 60 constructor: x");
  });

  it("uses the problem for errors without fields", () => {
    const conflict = new ApiError({
      type: "x",
      title: "Conflict",
      status: 409,
      detail: "The monitor was changed meanwhile.",
    });
    expect(mapServerErrors(conflict)).toEqual({
      fields: {},
      general: "The monitor was changed meanwhile.",
    });
  });

  it("explains a plan limit in plain words", () => {
    const limit = new ApiError({
      type: PLAN_LIMIT_PROBLEM_TYPE,
      title: "Plan limit reached",
      status: 409,
      detail: "The Free plan allows at most 3 monitors.",
      limit: "monitors",
      max: 3,
    });
    expect(mapServerErrors(limit)).toEqual({
      fields: {},
      general:
        "Your plan allows at most 3 monitors, and the limit is reached. Delete a monitor to add a new one.",
    });
  });
});

describe("validateSlackUrl", () => {
  it.each([
    ["https://hooks.slack.com/services/T0/B0/x", undefined],
    ["", "Enter the webhook URL."],
    ["https://hooks.slack.com/services/", expect.stringContaining("Incoming Webhook")],
    ["https://example.com/services/x", expect.stringContaining("Incoming Webhook")],
  ])("%s", (url, expected) => {
    expect(validateSlackUrl(url)).toEqual(expected);
  });
});

describe("withHttps and withoutHttps", () => {
  it("adds https:// unless the user typed a scheme", () => {
    expect(withHttps("example.com/health")).toBe("https://example.com/health");
    expect(withHttps(" https://example.com ")).toBe("https://example.com");
    expect(withHttps("http://example.com")).toBe("http://example.com");
    expect(withHttps("  ")).toBe("");
    expect(withoutHttps("https://example.com/")).toBe("example.com/");
    expect(withoutHttps("http://example.com/")).toBe("http://example.com/");
  });
});

describe("describeSetup", () => {
  const sentence = (v: Partial<MonitorFormValues>) =>
    describeSetup({ ...emptyMonitorForm, ...v })
      .map((p) => p.text)
      .join("");

  it("describes an HTTP check, with placeholders until it is filled in", () => {
    expect(sentence({})).toBe(
      "Checks your URL every minute and alerts Slack after 2 failures in a row.",
    );
    expect(
      sentence({
        url: "https://example.com/health",
        expectedMode: "exact",
        expectedCode: "204",
        failureThreshold: 1,
        notificationsEnabled: false,
      }),
    ).toBe(
      "Checks example.com/health every minute, expects HTTP 204, and marks it down after the first failure.",
    );
    expect(describeSetup(emptyMonitorForm).some((p) => p.blank)).toBe(true);
  });

  it("describes a watchdog", () => {
    expect(
      sentence({ type: "heartbeat", expectedIntervalSeconds: "900", graceSeconds: "120" }),
    ).toBe("Expects a ping every 15 min and alerts Slack if one is more than 2 min late.");
    expect(sentence({ type: "heartbeat", expectedIntervalSeconds: "60", graceSeconds: "0" })).toBe(
      "Expects a ping every 1 min and alerts Slack the moment one is late.",
    );
  });
});
