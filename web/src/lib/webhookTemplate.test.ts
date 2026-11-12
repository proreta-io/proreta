import {
  defaultTemplates,
  insertAt,
  placeholders,
  validateTemplate,
  validateWebhookUrl,
} from "./webhookTemplate";

describe("webhook url", () => {
  it("accepts an https URL", () => {
    expect(validateWebhookUrl(" https://example.com/hook?x=1 ")).toBeUndefined();
    expect(validateWebhookUrl("https://example.com:443/hook")).toBeUndefined();
  });

  it("rejects what the server rejects", () => {
    for (const bad of [
      "",
      "example.com/hook",
      "http://example.com/hook",
      "https://user:pw@example.com/",
      "https://example.com:8443/",
      `https://example.com/${"a".repeat(2048)}`,
    ]) {
      expect(validateWebhookUrl(bad), bad).toBeTruthy();
    }
  });
});

describe("template", () => {
  it("checks emptiness and size in bytes", () => {
    expect(validateTemplate("{{event}}")).toBeUndefined();
    expect(validateTemplate("  ")).toBeTruthy();
    expect(validateTemplate("é".repeat(4097))).toBeTruthy();
    expect(validateTemplate("é".repeat(4096))).toBeUndefined();
  });

  it("uses only known placeholders in the defaults", () => {
    const known = new Set(placeholders.map((p) => p.name));
    for (const template of Object.values(defaultTemplates)) {
      for (const m of template.matchAll(/\{\{([^}]+)\}\}/g)) expect(known.has(m[1]!)).toBe(true);
    }
    expect(JSON.parse(defaultTemplates.json)).toHaveProperty("monitor.name", "{{monitor.name}}");
  });

  it("inserts at the selection", () => {
    expect(insertAt("ab", 1, 1, "X")).toEqual({ value: "aXb", caret: 2 });
    expect(insertAt("abc", 0, 2, "{{link}}")).toEqual({ value: "{{link}}c", caret: 8 });
  });
});
