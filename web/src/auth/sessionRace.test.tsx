import { screen } from "@testing-library/react";
import { json, mockApi, problem, renderApp } from "../test/utils";

/**
 * A 401 from /auth/me drops the session's cached queries. Public pages must survive it even when
 * their own request is still in flight at that moment.
 */
const providers = {
  providers: [],
  passwordLoginEnabled: true,
  registrationEnabled: true,
  emailEnabled: true,
  passwordResetEnabled: true,
  emailVerificationEnabled: true,
};
const preview = { organizationName: "Acme", role: "member", expiresAt: "2026-11-12T10:00:00Z" };

/** A response the test releases by hand. */
function gate(make: () => Response) {
  let open!: () => void;
  const opened = new Promise<void>((resolve) => (open = resolve));
  return { open, handler: async () => (await opened, make()) };
}

const pages: [string, string, string | RegExp][] = [
  ["login", "/login", "Sign in"],
  ["register", "/register", "Create account"],
  ["accept invitation", "/invite#tok", "Acme"],
  ["forgot password", "/forgot-password", "Send reset link"],
  ["reset password", "/reset-password#tok", "Choose a new password"],
];

describe.each(["before", "after"])(
  "the session check answers 401 %s the page's own request",
  (when) => {
    it.each(pages)("%s page still loads", async (_name, path, expected) => {
      const own = gate(() => json(providers));
      const lookup = gate(() => json(preview));
      const me = gate(() => problem(401, "Not authenticated"));
      const mock = mockApi({
        "GET /api/v1/auth/me": me.handler,
        "GET /api/v1/auth/providers": own.handler,
        "POST /api/v1/invitations/lookup": lookup.handler,
      });
      window.history.replaceState(null, "", path);
      renderApp(path);
      if (when === "before") {
        me.open();
        await vi.waitFor(() => expect(mock).toHaveBeenCalled());
        await new Promise((r) => setTimeout(r, 20));
        own.open();
        lookup.open();
      } else {
        own.open();
        lookup.open();
        await new Promise((r) => setTimeout(r, 20));
        me.open();
      }
      expect((await screen.findAllByText(expected, { exact: false })).length).toBeGreaterThan(0);
    });
  },
);

describe("a later 401 from the session check (another tab signed out or in)", () => {
  it.each(pages)("%s page still loads", async (_name, path, expected) => {
    const own = gate(() => json(providers));
    const lookup = gate(() => json(preview));
    mockApi({
      "GET /api/v1/auth/me": () => problem(401, "Not authenticated"),
      "GET /api/v1/auth/providers": own.handler,
      "POST /api/v1/invitations/lookup": lookup.handler,
    });
    window.history.replaceState(null, "", path);
    const { queryClient } = renderApp(path);
    await new Promise((r) => setTimeout(r, 20));
    await queryClient.invalidateQueries({ queryKey: ["me"] });
    await new Promise((r) => setTimeout(r, 20));
    own.open();
    lookup.open();
    expect((await screen.findAllByText(expected, { exact: false })).length).toBeGreaterThan(0);
  });
});
