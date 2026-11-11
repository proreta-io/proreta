import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminEmailSettings } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const settings = (over: Partial<AdminEmailSettings> = {}): AdminEmailSettings => ({
  configured: false,
  host: "",
  port: 587,
  tls: "starttls",
  from: "",
  username: "",
  passwordSet: false,
  sendInvitations: false,
  sendEmailVerification: false,
  allowPasswordReset: false,
  tested: false,
  testedAt: null,
  lastTest: null,
  updatedAt: "2026-11-16T09:00:00Z",
  ...over,
});

const configured = (over: Partial<AdminEmailSettings> = {}) =>
  settings({
    configured: true,
    host: "smtp.example.com",
    from: "Proreta <proreta@example.com>",
    username: "mailer",
    passwordSet: true,
    ...over,
  });

const succeeded = {
  state: "succeeded" as const,
  requestedAt: "2026-11-16T09:01:00Z",
  finishedAt: "2026-11-16T09:01:02Z",
  error: null,
  smtpCode: null,
};

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

describe("admin email page", () => {
  it("is only for the platform admin", async () => {
    mockApi({ "GET /api/v1/auth/me": () => json(session) });
    renderApp("/admin/email");
    expect(await screen.findByText(/not found|Page not found/i)).toBeInTheDocument();
  });

  it("explains the source of truth and starts with everything off", async () => {
    mockApi({ ...asAdmin, "GET /api/v1/admin/email": () => json(settings()) });
    renderApp("/admin/email");
    expect(await screen.findByRole("heading", { name: "SMTP server" })).toBeInTheDocument();
    expect(
      screen.getByText(/set here, not in the installation.s configuration/),
    ).toBeInTheDocument();
    for (const name of [
      "Send invitation links by email",
      "Send email verification at registration",
      "Allow password reset by email",
    ]) {
      const box = screen.getByRole("checkbox", { name });
      expect(box).not.toBeChecked();
      expect(box).toBeDisabled();
    }
    expect(screen.getByText(/stay off until a test message succeeds/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send test email" })).toBeDisabled();
  });

  it("saves the server without sending a password it does not have", async () => {
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(settings()),
      "PUT /api/v1/admin/email": () => json(configured({ updatedAt: "2026-11-16T09:05:00Z" })),
    });
    renderApp("/admin/email");
    await userEvent.type(await screen.findByLabelText("Host"), "smtp.example.com");
    await userEvent.type(screen.getByLabelText("Sender"), "proreta@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save server" }));
    expect(await screen.findByText("Email settings saved.")).toBeInTheDocument();
    const sent = requests(mock, "PUT", "/api/v1/admin/email")[0]!;
    expect(await sent.clone().json()).toEqual({
      host: "smtp.example.com",
      port: 587,
      tls: "starttls",
      from: "proreta@example.com",
      username: "",
    });
  });

  it("sends a new password only when one is typed, and never shows the stored one", async () => {
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(configured()),
      "PUT /api/v1/admin/email": () => json(configured({ updatedAt: "2026-11-16T09:05:00Z" })),
    });
    renderApp("/admin/email");
    expect(await screen.findByText("Configured, hidden")).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).toBeNull();
    await userEvent.clear(screen.getByLabelText("Sender"));
    await userEvent.type(screen.getByLabelText("Sender"), "noreply@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save server" }));
    await screen.findByText("Email settings saved.");
    const sent = requests(mock, "PUT", "/api/v1/admin/email")[0]!;
    const body = await sent.clone().json();
    expect(body.from).toBe("noreply@example.com");
    expect(body).not.toHaveProperty("password");
  });

  it("asks for the password again when the host changes", async () => {
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(configured()),
      "PUT /api/v1/admin/email": () => json(configured({ host: "other.example.com" })),
    });
    renderApp("/admin/email");
    await userEvent.clear(await screen.findByLabelText("Host"));
    await userEvent.type(screen.getByLabelText("Host"), "other.example.com");
    const password = await screen.findByLabelText("Password");
    expect(screen.getByText(/Enter the password again/)).toBeInTheDocument();
    await userEvent.type(password, "s3cret-value");
    await userEvent.click(screen.getByRole("button", { name: "Save server" }));
    await screen.findByText("Email settings saved.");
    const sent = requests(mock, "PUT", "/api/v1/admin/email")[0]!;
    expect((await sent.clone().json()).password).toBe("s3cret-value");
    expect(screen.queryByDisplayValue("s3cret-value")).toBeNull();
  });

  it("shows the server's complaints on the fields", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(settings()),
      "PUT /api/v1/admin/email": () =>
        problem(422, "Validation failed", {
          type: "urn:proreta:problem:validation",
          fieldErrors: [{ field: "host", message: "this address is never used for mail" }],
        }),
    });
    renderApp("/admin/email");
    await userEvent.type(await screen.findByLabelText("Host"), "169.254.169.254");
    await userEvent.type(screen.getByLabelText("Sender"), "a@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save server" }));
    expect(await screen.findByText("this address is never used for mail")).toBeInTheDocument();
  });

  it("sends a test email to the given address and shows the pending state, then the result", async () => {
    let state: AdminEmailSettings = configured();
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(state),
      "POST /api/v1/admin/email/test": () => {
        state = configured({
          lastTest: {
            state: "pending",
            requestedAt: "2026-11-16T09:01:00Z",
            finishedAt: null,
            error: null,
            smtpCode: null,
          },
        });
        return json(state, 202);
      },
    });
    renderApp("/admin/email");
    await userEvent.type(await screen.findByLabelText("Send the test to"), "ops@example.org");
    await userEvent.click(screen.getByRole("button", { name: "Send test email" }));
    expect(await screen.findByText(/Sending the test message/)).toBeInTheDocument();
    expect(await requests(mock, "POST", "/api/v1/admin/email/test")[0]!.clone().json()).toEqual({
      to: "ops@example.org",
    });
    state = configured({ tested: true, testedAt: "2026-11-16T09:01:02Z", lastTest: succeeded });
    expect(
      await screen.findByText(/accepted by the server/, undefined, { timeout: 4000 }),
    ).toBeInTheDocument();
  });

  it("explains a failed test with a category and no server text", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () =>
        json(
          configured({
            lastTest: { ...succeeded, state: "failed", error: "auth", smtpCode: 535 },
          }),
        ),
    });
    renderApp("/admin/email");
    expect(await screen.findByText(/refused the username or password/)).toBeInTheDocument();
    expect(screen.getByText(/auth, SMTP 535/)).toBeInTheDocument();
  });

  it("turns a switch on only after a successful test", async () => {
    let state = configured({ tested: true, testedAt: "2026-11-16T09:01:02Z", lastTest: succeeded });
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(state),
      "PUT /api/v1/admin/email/features": async (r) => {
        const body = await r.clone().json();
        state = { ...state, ...body };
        return json(state);
      },
    });
    renderApp("/admin/email");
    const box = await screen.findByRole("checkbox", { name: "Allow password reset by email" });
    expect(box).toBeEnabled();
    await userEvent.click(box);
    await waitFor(() => expect(box).toBeChecked());
    const sent = requests(mock, "PUT", "/api/v1/admin/email/features")[0]!;
    expect(await sent.clone().json()).toEqual({
      sendInvitations: false,
      sendEmailVerification: false,
      allowPasswordReset: true,
    });
  });

  it("removes the server after a confirmation", async () => {
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/email": () => json(configured()),
      "DELETE /api/v1/admin/email": () => new Response(null, { status: 204 }),
    });
    renderApp("/admin/email");
    await userEvent.click(await screen.findByRole("button", { name: "Remove server" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove the email server?" });
    expect(requests(mock, "DELETE", "/api/v1/admin/email")).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove server" }));
    await waitFor(() => expect(requests(mock, "DELETE", "/api/v1/admin/email")).toHaveLength(1));
  });
});
