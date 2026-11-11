import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getCsrfToken } from "../api/client";
import { json, mockApi, problem, renderApp, session, signedIn } from "../test/utils";

const identities = (passwordSet = true, passwordLoginEnabled = true) => ({
  "GET /api/v1/auth/identities": () => json({ items: [], passwordSet, passwordLoginEnabled }),
  "GET /api/v1/auth/providers": () =>
    json({ providers: [], passwordLoginEnabled: true, registrationEnabled: false }),
});

const NEW = "a new long passphrase";

async function fill(current: string, next: string, confirm = next) {
  await userEvent.type(await screen.findByLabelText("Current password"), current);
  await userEvent.type(screen.getByLabelText("New password"), next);
  await userEvent.type(screen.getByLabelText("Confirm new password"), confirm);
}

const submit = () => userEvent.click(screen.getByRole("button", { name: "Change password" }));

describe("change password on the profile page", () => {
  it("changes the password and takes the new session", async () => {
    let body: unknown;
    const fetchMock = mockApi({
      ...signedIn,
      ...identities(),
      "POST /api/v1/auth/password": async (r) => {
        body = await r.json();
        return json({ ...session, csrfToken: "csrf-2" });
      },
    });
    renderApp("/profile");
    await fill("old passphrase 1", NEW);
    await submit();
    expect(
      await screen.findByText(/Password changed\. Your other devices were signed out/),
    ).toBeInTheDocument();
    expect(body).toEqual({ currentPassword: "old passphrase 1", newPassword: NEW });
    // The CSRF token of the new session is used from now on, and the form is empty again.
    expect(getCsrfToken()).toBe("csrf-2");
    expect(screen.getByLabelText("Current password")).toHaveValue("");
    expect(screen.getByLabelText("New password")).toHaveValue("");
    expect(
      fetchMock.mock.calls.filter(
        ([r]) => new URL((r as Request).url).pathname === "/api/v1/auth/me",
      ),
    ).toHaveLength(1);
  });

  it("checks the rules before sending anything and focuses the first problem", async () => {
    mockApi({ ...signedIn, ...identities() });
    renderApp("/profile");
    await fill("old passphrase 1", "short", "other");
    await submit();
    expect(await screen.findByText("Use at least 12 characters")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Check the highlighted fields.");
    expect(screen.getByLabelText("New password")).toHaveFocus();
    expect(screen.getByLabelText("New password")).toBeInvalid();
  });

  it("asks for a matching confirmation", async () => {
    mockApi({ ...signedIn, ...identities() });
    renderApp("/profile");
    await fill("old passphrase 1", NEW, "a different passphrase");
    await submit();
    expect(await screen.findByText("The passwords do not match")).toBeInTheDocument();
    expect(screen.getByLabelText("Confirm new password")).toHaveFocus();
  });

  it("shows a wrong current password on its field and stays signed in", async () => {
    mockApi({
      ...signedIn,
      ...identities(),
      "POST /api/v1/auth/password": () =>
        problem(422, "Validation failed", {
          type: "urn:proreta:problem:validation",
          fieldErrors: [{ field: "currentPassword", message: "is not correct" }],
        }),
    });
    renderApp("/profile");
    await fill("wrong one", NEW);
    await submit();
    expect(await screen.findByText("Current password is not correct")).toBeInTheDocument();
    expect(screen.getByLabelText("Current password")).toHaveFocus();
    // Typed values stay, so only the current password has to be retyped.
    expect(screen.getByLabelText("New password")).toHaveValue(NEW);
    expect(screen.getByRole("heading", { name: "Profile" })).toBeInTheDocument();
  });

  it("explains the rate limit", async () => {
    mockApi({
      ...signedIn,
      ...identities(),
      "POST /api/v1/auth/password": () =>
        new Response(
          JSON.stringify({ type: "urn:proreta:problem:rate-limited", title: "x", status: 429 }),
          {
            status: 429,
            headers: { "Content-Type": "application/problem+json", "Retry-After": "120" },
          },
        ),
    });
    renderApp("/profile");
    await fill("old passphrase 1", NEW);
    await submit();
    expect(await screen.findByText(/Too many attempts\. Try again in/)).toBeInTheDocument();
  });

  it("shows and hides the typed passwords", async () => {
    mockApi({ ...signedIn, ...identities() });
    renderApp("/profile");
    const current = await screen.findByLabelText("Current password");
    expect(current).toHaveAttribute("type", "password");
    await userEvent.click(screen.getByRole("button", { name: "Show passwords" }));
    expect(current).toHaveAttribute("type", "text");
    expect(screen.getByLabelText("New password")).toHaveAttribute("type", "text");
  });

  it("is not offered to a user without a password or with password sign-in off", async () => {
    mockApi({ ...signedIn, ...identities(false) });
    const first = renderApp("/profile");
    expect(await screen.findByText(/You have no password/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Current password")).not.toBeInTheDocument();
    first.unmount();

    mockApi({ ...signedIn, ...identities(true, false) });
    renderApp("/profile");
    expect(await screen.findByText(/password sign-in is turned off/)).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByLabelText("Current password")).not.toBeInTheDocument(),
    );
  });
});

describe("first sign-in: a password issued by somebody else", () => {
  const flagged = { ...session, mustChangePassword: true };
  const flaggedMe = { "GET /api/v1/auth/me": () => json(flagged) };

  it("shows only the set-a-new-password screen, on any address", async () => {
    mockApi({ ...flaggedMe, "GET /api/v1/monitors": () => json({ items: [] }) });
    renderApp("/settings");
    expect(await screen.findByRole("heading", { name: "Set a new password" })).toBeInTheDocument();
    expect(
      screen.getByText("This is your first sign-in. Choose a new password to continue."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Current password")).toHaveFocus();
    // No navigation of the app, no way around it.
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Monitors|Profile/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
  });

  it("sends an invitation visit and the login page to the same screen", async () => {
    mockApi({ ...flaggedMe, "GET /api/v1/auth/providers": () => json({ providers: [] }) });
    renderApp("/login");
    expect(await screen.findByRole("heading", { name: "Set a new password" })).toBeInTheDocument();
  });

  it("changes the password and continues to the page that was asked for", async () => {
    let body: unknown;
    mockApi({
      ...flaggedMe,
      "POST /api/v1/auth/password": async (r) => {
        body = await r.json();
        return json({ ...session, csrfToken: "csrf-2" });
      },
      "GET /api/v1/auth/identities": () =>
        json({ items: [], passwordSet: true, passwordLoginEnabled: true }),
      "GET /api/v1/auth/providers": () =>
        json({ providers: [], passwordLoginEnabled: true, registrationEnabled: false }),
    });
    renderApp("/profile");
    await fill("one-time password", NEW);
    await userEvent.click(screen.getByRole("button", { name: "Set new password" }));
    expect(await screen.findByRole("heading", { name: "Profile" })).toBeInTheDocument();
    expect(body).toEqual({ currentPassword: "one-time password", newPassword: NEW });
    expect(screen.queryByRole("heading", { name: "Set a new password" })).not.toBeInTheDocument();
  });

  it("refuses the one-time password as the new one before sending anything", async () => {
    const fetchMock = mockApi({ ...flaggedMe });
    renderApp("/");
    await fill("one-time password", "one-time password");
    await userEvent.click(screen.getByRole("button", { name: "Set new password" }));
    expect(
      await screen.findByText("Choose a password different from the current one"),
    ).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([r]) => (r as Request).url.endsWith("/auth/password"))).toBe(
      false,
    );
  });

  it("shows a wrong current password on its field", async () => {
    mockApi({
      ...flaggedMe,
      "POST /api/v1/auth/password": () =>
        problem(422, "Validation failed", {
          type: "urn:proreta:problem:validation",
          fieldErrors: [{ field: "currentPassword", message: "is not correct" }],
        }),
    });
    renderApp("/");
    await fill("wrong one", NEW);
    await userEvent.click(screen.getByRole("button", { name: "Set new password" }));
    expect(await screen.findByText("Current password is not correct")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Set a new password" })).toBeInTheDocument();
  });

  it("shows the screen when the server asks for the change after the page loaded", async () => {
    let flag = false;
    mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, mustChangePassword: flag }),
      "GET /api/v1/monitors": () => {
        flag = true;
        return problem(403, "Password change required", {
          type: "urn:proreta:problem:password-change-required",
        });
      },
      "GET /api/v1/projects": () => json({ items: [] }),
    });
    renderApp("/");
    expect(await screen.findByRole("heading", { name: "Set a new password" })).toBeInTheDocument();
  });

  it("signs out from the screen", async () => {
    mockApi({
      ...flaggedMe,
      "POST /api/v1/auth/logout": () => new Response(null, { status: 204 }),
    });
    renderApp("/");
    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    await waitFor(() =>
      expect(screen.queryByRole("heading", { name: "Set a new password" })).toBeNull(),
    );
  });
});
