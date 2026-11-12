import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Channel } from "../api/endpoints";
import { defaultTemplates } from "../lib/webhookTemplate";
import { json, mockApi, problem, renderApp, signedIn } from "../test/utils";

const webhook = (over: Partial<Channel> = {}): Channel => ({
  id: "w1",
  projectId: "p-default",
  name: "Incident tool",
  type: "webhook",
  configHidden: false,
  events: ["down"],
  mention: [],
  reminderMinutes: 0,
  contentType: "json",
  bodyTemplate: '{"e":"{{event}}"}',
  status: "active",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  ...over,
});

// The preview is requested 400 ms after the last keystroke.
const previewOf = (body: string) => ({
  "POST /api/v1/channels/preview-template": async () => json({ body }),
});

describe("webhook channels in settings", () => {
  it("lists the type of each channel", async () => {
    mockApi({ ...signedIn, "GET /api/v1/channels": () => json({ items: [webhook()] }) });
    renderApp("/channels");
    const row = await screen.findByRole("row", { name: /Incident tool/ });
    expect(row).toHaveTextContent("Incident toolWebhookDown");
  });

  it("creates a webhook channel with the default template and a live preview", async () => {
    let sent: unknown;
    let previewed: unknown;
    mockApi({
      ...signedIn,
      "GET /api/v1/channels": () => json({ items: [] }),
      "POST /api/v1/channels/preview-template": async (req) => {
        previewed = await req.json();
        return json({ body: "RENDERED SAMPLE" });
      },
      "POST /api/v1/channels": async (req) => {
        sent = await req.json();
        return json(webhook(), 201);
      },
    });
    renderApp("/channels");
    await userEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    await userEvent.click(screen.getByLabelText("Webhook"));

    expect(screen.queryByLabelText("Mention (optional)")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Message template")).toHaveValue(defaultTemplates.json);
    expect(await screen.findByTestId("template-preview")).toHaveTextContent("RENDERED SAMPLE");
    expect(previewed).toEqual({ contentType: "json", bodyTemplate: defaultTemplates.json });

    const url = screen.getByLabelText("Webhook URL");
    expect(url).toHaveClass("secret-text");
    await userEvent.type(screen.getByLabelText("Name"), "Incident tool");
    await userEvent.type(url, "https://example.com/hook");
    await userEvent.click(screen.getByRole("button", { name: "Add channel" }));

    await waitFor(() => expect(sent).toBeDefined());
    expect(sent).toEqual({
      type: "webhook",
      name: "Incident tool",
      webhookUrl: "https://example.com/hook",
      events: ["down", "recovered", "tls_expiring"],
      reminderMinutes: 0,
      contentType: "json",
      bodyTemplate: defaultTemplates.json,
    });
  });

  it("follows the content type with an untouched template and inserts placeholders", async () => {
    mockApi({
      ...signedIn,
      "GET /api/v1/channels": () => json({ items: [] }),
      ...previewOf("x"),
    });
    renderApp("/channels");
    await userEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    await userEvent.click(screen.getByLabelText("Webhook"));
    await userEvent.click(screen.getByLabelText("Plain text"));
    const template = screen.getByLabelText("Message template");
    expect(template).toHaveValue(defaultTemplates.text);

    await userEvent.clear(template);
    await userEvent.type(template, "down: ");
    await userEvent.click(screen.getByRole("button", { name: "{{monitor.name}}" }));
    expect(template).toHaveValue("down: {{monitor.name}}");

    // A template the user changed is kept when the content type changes.
    await userEvent.click(screen.getByLabelText("JSON"));
    expect(template).toHaveValue("down: {{monitor.name}}");
  });

  it("shows the server's template error in the preview and on the field after saving", async () => {
    const message = "unknown placeholder {{nope}}";
    mockApi({
      ...signedIn,
      "GET /api/v1/channels": () => json({ items: [] }),
      "POST /api/v1/channels/preview-template": () =>
        problem(422, "Validation failed", {
          type: "urn:proreta:problem:validation",
          fieldErrors: [{ field: "bodyTemplate", message }],
        }),
      "POST /api/v1/channels": () =>
        problem(422, "Validation failed", {
          type: "urn:proreta:problem:validation",
          fieldErrors: [{ field: "bodyTemplate", message }],
        }),
    });
    renderApp("/channels");
    await userEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    await userEvent.click(screen.getByLabelText("Webhook"));
    expect(await screen.findByText(message)).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.type(screen.getByLabelText("Webhook URL"), "https://example.com/hook");
    await userEvent.click(screen.getByRole("button", { name: "Add channel" }));
    expect(await screen.findByLabelText("Message template")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
  });

  it("checks the URL and the template before sending", async () => {
    const fetchMock = mockApi({
      ...signedIn,
      "GET /api/v1/channels": () => json({ items: [] }),
      ...previewOf("x"),
    });
    renderApp("/channels");
    await userEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    await userEvent.click(screen.getByLabelText("Webhook"));
    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.type(screen.getByLabelText("Webhook URL"), "http://example.com/hook");
    await userEvent.clear(screen.getByLabelText("Message template"));
    await userEvent.click(screen.getByRole("button", { name: "Add channel" }));
    expect(screen.getByLabelText("Webhook URL")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Message template")).toHaveAttribute("aria-invalid", "true");
    expect(
      fetchMock.mock.calls.some(
        ([r]) => (r as Request).method === "POST" && (r as Request).url.endsWith("/channels"),
      ),
    ).toBe(false);
  });

  it("edits a webhook channel: the URL is kept, the type is fixed, the template is sent", async () => {
    let sent: unknown;
    mockApi({
      ...signedIn,
      "GET /api/v1/channels": () => json({ items: [webhook()] }),
      ...previewOf("x"),
      "PATCH /api/v1/channels/w1": async (req) => {
        sent = await req.json();
        return json(webhook({ contentType: "text", bodyTemplate: "{{event}}" }));
      },
    });
    renderApp("/channels");
    await userEvent.click(await screen.findByRole("button", { name: "Edit Incident tool" }));
    expect(screen.queryByRole("radiogroup", { name: "Channel type" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Webhook URL")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Message template")).toHaveValue('{"e":"{{event}}"}');

    await userEvent.click(screen.getByLabelText("Plain text"));
    const template = screen.getByLabelText("Message template");
    await userEvent.clear(template);
    await userEvent.type(template, "{{{{event}}");
    await userEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() => expect(sent).toBeDefined());
    expect(sent).toEqual({
      name: "Incident tool",
      events: ["down"],
      reminderMinutes: 0,
      contentType: "text",
      bodyTemplate: "{{event}}",
    });
  });
});
