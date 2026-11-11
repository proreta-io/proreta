import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { json, mockApi, problem, renderApp } from "../test/utils";

const noSession = { "GET /api/v1/auth/me": () => problem(401, "Not authenticated") };
/** resetOn is the platform admin's switch; it needs the password form and a tested server. */
const providers = (emailEnabled: boolean, passwordLoginEnabled = true, resetOn = emailEnabled) => ({
  "GET /api/v1/auth/providers": () =>
    json({
      providers: [],
      passwordLoginEnabled,
      registrationEnabled: false,
      emailEnabled,
      passwordResetEnabled: resetOn && emailEnabled && passwordLoginEnabled,
      emailVerificationEnabled: emailEnabled,
    }),
});
const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

describe("the forgot-password link", () => {
  it("is offered on the sign-in page only when email and the password form exist", async () => {
    mockApi({ ...noSession, ...providers(true) });
    renderApp("/login");
    expect(await screen.findByRole("link", { name: "Forgot your password?" })).toHaveAttribute(
      "href",
      "/forgot-password",
    );
  });

  it.each([
    ["the installation sends no email", providers(false)],
    ["the password form is off", providers(true, false)],
    ["the admin switch is off", providers(true, true, false)],
  ])("is not offered when %s", async (_n, p) => {
    mockApi({ ...noSession, ...p });
    renderApp("/login?admin");
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Forgot your password?" })).toBeNull();
  });
});

describe("asking for a reset link", () => {
  it("sends the address and says the same thing whatever the account", async () => {
    const mock = mockApi({
      ...noSession,
      ...providers(true),
      "POST /api/v1/auth/forgot-password": () => new Response(null, { status: 202 }),
    });
    renderApp("/forgot-password");
    await userEvent.type(await screen.findByLabelText("Email"), " Ada@Example.com ");
    await userEvent.click(screen.getByRole("button", { name: "Send reset link" }));
    expect(
      await screen.findByText("If that address has an account, a link is on its way."),
    ).toBeInTheDocument();
    const sent = requests(mock, "POST", "/api/v1/auth/forgot-password")[0]!;
    expect(await sent.clone().json()).toEqual({ email: "ada@example.com" });
  });

  it("says when to try again after too many requests", async () => {
    mockApi({
      ...noSession,
      ...providers(true),
      "POST /api/v1/auth/forgot-password": () =>
        new Response(
          JSON.stringify({ type: "urn:proreta:problem:rate-limited", title: "Slow", status: 429 }),
          {
            status: 429,
            headers: { "Content-Type": "application/problem+json", "Retry-After": "90" },
          },
        ),
    });
    renderApp("/forgot-password");
    await userEvent.type(await screen.findByLabelText("Email"), "ada@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Send reset link" }));
    expect(await screen.findByText("Too many requests. Try again in 1 min.")).toBeInTheDocument();
  });

  it("tells who to ask when the installation cannot email", async () => {
    mockApi({ ...noSession, ...providers(false) });
    renderApp("/forgot-password");
    expect(
      await screen.findByText("Password reset by email is not available."),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Email")).toBeNull();
  });

  it("shows the same message when only the switch is off", async () => {
    mockApi({ ...noSession, ...providers(true, true, false) });
    renderApp("/forgot-password");
    expect(
      await screen.findByText("Password reset by email is not available."),
    ).toBeInTheDocument();
  });
});

describe("choosing a new password", () => {
  async function fill(password: string, confirm = password) {
    await userEvent.type(await screen.findByLabelText("New password"), password);
    await userEvent.type(screen.getByLabelText("Confirm new password"), confirm);
    await userEvent.click(screen.getByRole("button", { name: "Change password" }));
  }

  it("sends the token from the link in the body and ends at the sign-in link", async () => {
    const mock = mockApi({
      ...noSession,
      ...providers(true),
      "POST /api/v1/auth/reset-password": () => new Response(null, { status: 204 }),
    });
    renderApp("/reset-password#TOKEN-9");
    await fill("a brand new passphrase");
    expect(await screen.findByText("Your password is changed.")).toBeInTheDocument();
    const sent = requests(mock, "POST", "/api/v1/auth/reset-password")[0]!;
    expect(await sent.clone().json()).toEqual({
      token: "TOKEN-9",
      password: "a brand new passphrase",
    });
    expect(new URL(sent.url).search).toBe("");
    expect(screen.getByRole("link", { name: "Go to sign in" })).toHaveAttribute("href", "/login");
    expect(globalThis.location.hash).toBe("");
  });

  it("checks length and confirmation before calling the server", async () => {
    const mock = mockApi({ ...noSession, ...providers(true) });
    renderApp("/reset-password#T");
    await fill("short");
    expect(await screen.findByText("Use 12 to 128 characters.")).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("New password"));
    await userEvent.clear(screen.getByLabelText("Confirm new password"));
    await fill("a brand new passphrase", "another new passphrase");
    expect(await screen.findByText("The two passwords are not the same.")).toBeInTheDocument();
    expect(requests(mock, "POST", "/api/v1/auth/reset-password")).toHaveLength(0);
  });

  it("offers a new link when the old one does not work", async () => {
    mockApi({
      ...noSession,
      ...providers(true),
      "POST /api/v1/auth/reset-password": () =>
        problem(404, "Not found", { type: "urn:proreta:problem:not-found" }),
    });
    renderApp("/reset-password#USED");
    await fill("a brand new passphrase");
    expect(await screen.findByText("This link is not valid.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Send a new link" })).toHaveAttribute(
      "href",
      "/forgot-password",
    );
  });

  it("explains a link without its token and calls nothing", async () => {
    const mock = mockApi({ ...noSession, ...providers(true) });
    renderApp("/reset-password");
    expect(await screen.findByText("This link is incomplete.")).toBeInTheDocument();
    expect(requests(mock, "POST", "/api/v1/auth/reset-password")).toHaveLength(0);
  });

  it("says the feature is not available, with a valid-looking link, when the switch is off", async () => {
    const mock = mockApi({ ...noSession, ...providers(true, true, false) });
    renderApp("/reset-password#VALID-TOKEN");
    expect(
      await screen.findByText("Password reset by email is not available."),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("New password")).toBeNull();
    expect(requests(mock, "POST", "/api/v1/auth/reset-password")).toHaveLength(0);
  });
});
