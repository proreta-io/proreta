import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const noSession = { "GET /api/v1/auth/me": () => problem(401, "Not authenticated") };
const providers = (emailEnabled: boolean) => ({
  "GET /api/v1/auth/providers": () =>
    json({
      providers: [],
      passwordLoginEnabled: true,
      registrationEnabled: true,
      emailEnabled,
      passwordResetEnabled: emailEnabled,
      emailVerificationEnabled: emailEnabled,
    }),
});
const user = (login: string, emailVerified: boolean, isPlatformAdmin = false) => ({
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login, emailVerified, isPlatformAdmin } }),
  "GET /api/v1/monitors": () =>
    json({ summary: { up: 0, down: 0, paused: 0, unknown: 0 }, items: [] }),
});

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

describe("the verification page", () => {
  it("verifies the token of the link once, without a session", async () => {
    const mock = mockApi({
      ...noSession,
      "POST /api/v1/auth/verify-email": () => new Response(null, { status: 204 }),
    });
    renderApp("/verify-email#TOKEN-123");
    expect(await screen.findByText("Your email address is verified.")).toBeInTheDocument();
    const calls = requests(mock, "POST", "/api/v1/auth/verify-email");
    expect(calls).toHaveLength(1);
    expect(await calls[0]!.clone().json()).toEqual({ token: "TOKEN-123" });
    expect(new URL(calls[0]!.url).search).toBe("");
    expect(screen.getByRole("link", { name: "Go to sign in" })).toHaveAttribute("href", "/login");
    expect(globalThis.location.hash).toBe("");
  });

  it("offers the way back into the app to a signed-in user", async () => {
    mockApi({
      ...user("ada@example.com", false),
      ...providers(true),
      "POST /api/v1/auth/verify-email": () => new Response(null, { status: 204 }),
    });
    renderApp("/verify-email#TOKEN-123");
    expect(await screen.findByText("Your email address is verified.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Continue to Proreta" })).toHaveAttribute("href", "/");
  });

  it("says the same thing for every link that does not work", async () => {
    mockApi({
      ...noSession,
      "POST /api/v1/auth/verify-email": () =>
        problem(404, "Not found", { type: "urn:proreta:problem:not-found" }),
    });
    renderApp("/verify-email#USED");
    expect(await screen.findByText("This link is not valid.")).toBeInTheDocument();
    expect(screen.getByText(/expired, been used or been replaced/)).toBeInTheDocument();
  });

  it("says when to try again after too many attempts", async () => {
    mockApi({
      ...noSession,
      "POST /api/v1/auth/verify-email": () =>
        new Response(
          JSON.stringify({
            type: "urn:proreta:problem:rate-limited",
            title: "Slow down",
            status: 429,
          }),
          {
            status: 429,
            headers: { "Content-Type": "application/problem+json", "Retry-After": "120" },
          },
        ),
    });
    renderApp("/verify-email#T");
    expect(await screen.findByText("Too many attempts.")).toBeInTheDocument();
    expect(screen.getByText("Try again in 2 min.")).toBeInTheDocument();
  });

  it("explains a link without its token and calls nothing", async () => {
    const mock = mockApi(noSession);
    renderApp("/verify-email");
    expect(await screen.findByText("This link is incomplete.")).toBeInTheDocument();
    expect(requests(mock, "POST", "/api/v1/auth/verify-email")).toHaveLength(0);
  });
});

describe("the verification banner", () => {
  it("asks an unverified user to verify and sends the email on request", async () => {
    const mock = mockApi({
      ...user("ada@example.com", false),
      ...providers(true),
      "POST /api/v1/auth/resend-verification": () => new Response(null, { status: 204 }),
    });
    renderApp("/");
    expect(await screen.findByText("Your email address is not verified.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Send verification email" }));
    expect(
      await screen.findByText("Verification email sent. Check your inbox."),
    ).toBeInTheDocument();
    expect(requests(mock, "POST", "/api/v1/auth/resend-verification")).toHaveLength(1);
  });

  it("shows the wait when the server limits the resend", async () => {
    mockApi({
      ...user("ada@example.com", false),
      ...providers(true),
      "POST /api/v1/auth/resend-verification": () =>
        new Response(
          JSON.stringify({
            type: "urn:proreta:problem:rate-limited",
            title: "Slow down",
            status: 429,
          }),
          {
            status: 429,
            headers: { "Content-Type": "application/problem+json", "Retry-After": "45" },
          },
        ),
    });
    renderApp("/");
    await userEvent.click(await screen.findByRole("button", { name: "Send verification email" }));
    expect(await screen.findByText("Too many requests. Try again in 45 s.")).toBeInTheDocument();
  });

  it.each([
    ["the installation sends no email", user("ada@example.com", false), providers(false)],
    ["the address is verified", user("ada@example.com", true), providers(true)],
    ["the login is not an address", user("admin", false), providers(true)],
    ["the user is the platform admin", user("ops@example.com", false, true), providers(true)],
    [
      "the providers cannot be loaded",
      user("ada@example.com", false),
      { "GET /api/v1/auth/providers": () => problem(500, "Boom") },
    ],
  ])("is not shown when %s", async (_name, me, config) => {
    const mock = mockApi({ ...me, ...config });
    renderApp("/");
    expect(await screen.findByRole("heading", { name: "Monitors" })).toBeInTheDocument();
    await waitFor(() => expect(requests(mock, "GET", "/api/v1/auth/providers")).toHaveLength(1));
    expect(screen.queryByText("Your email address is not verified.")).not.toBeInTheDocument();
  });
});
