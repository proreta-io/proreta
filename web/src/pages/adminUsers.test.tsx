import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminUserDetail, AdminUserSummary } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const summary = (over: Partial<AdminUserSummary> = {}): AdminUserSummary => ({
  id: "u-1",
  login: "alice@alpha.example",
  displayName: "Alice",
  status: "active",
  isPlatformAdmin: false,
  isSystemAdmin: false,
  emailVerified: true,
  mustChangePassword: false,
  createdAt: "2026-10-01T00:00:00Z",
  organizationCount: 2,
  ...over,
});

const detail = (over: Partial<AdminUserSummary> = {}, sessions = 2): AdminUserDetail => ({
  user: summary(over),
  openSessions: sessions,
  memberships: [
    {
      organizationId: "t-1",
      organizationName: "Alpha Corp",
      organizationSlug: "t-alpha",
      organizationStatus: "active",
      role: "owner",
      joinedAt: "2026-10-01T00:00:00Z",
    },
    {
      organizationId: "t-2",
      organizationName: "Beta Ltd",
      organizationSlug: "t-beta",
      organizationStatus: "suspended",
      role: "viewer",
      joinedAt: "2026-10-05T00:00:00Z",
    },
  ],
});

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

describe("admin users list", () => {
  it("is only for the platform admin", async () => {
    mockApi({ "GET /api/v1/auth/me": () => json(session) });
    renderApp("/admin/users");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
  });

  it("lists users, searches and filters by status, and paginates", async () => {
    const seen: URL[] = [];
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users": (req: Request) => {
        const url = new URL(req.url);
        seen.push(url);
        const off = Number(url.searchParams.get("offset") ?? "0");
        const status = url.searchParams.get("status");
        return json({
          items: [
            summary(
              status === "disabled"
                ? { id: "u-3", login: "carol@gamma.example", status: "disabled" }
                : off > 0
                  ? { id: "u-2", login: "bob@beta.example" }
                  : {},
            ),
          ],
          total: 40,
          limit: 25,
          offset: off,
        });
      },
    });
    renderApp("/admin/users");
    expect(await screen.findByRole("link", { name: "alice@alpha.example" })).toHaveAttribute(
      "href",
      "/admin/users/u-1",
    );
    expect(screen.getByText("1–1 of 40")).toBeInTheDocument();

    await userEvent.type(screen.getByRole("searchbox", { name: "Search users" }), "carol");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(seen.at(-1)!.searchParams.get("query")).toBe("carol"));

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Status" }), "disabled");
    expect(await screen.findByRole("link", { name: "carol@gamma.example" })).toBeInTheDocument();
    expect(seen.at(-1)!.searchParams.get("status")).toBe("disabled");
    expect(within(screen.getByRole("table")).getByText("Disabled")).toBeInTheDocument();
  });

  it("shows an empty note", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users": () => json({ items: [], total: 0, limit: 25, offset: 0 }),
    });
    renderApp("/admin/users?query=zed");
    expect(await screen.findByText(/No users match/)).toBeInTheDocument();
  });
});

describe("admin user details", () => {
  it("shows the memberships and links them to the organizations", async () => {
    mockApi({ ...asAdmin, "GET /api/v1/admin/users/u-1": () => json(detail()) });
    renderApp("/admin/users/u-1");
    expect(await screen.findByRole("heading", { name: "alice@alpha.example" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Alpha Corp/ })).toHaveAttribute(
      "href",
      "/admin/organizations/t-1",
    );
    const rows = within(screen.getByRole("table")).getAllByRole("row");
    expect(rows).toHaveLength(3);
    expect(within(rows[2]!).getByText("Suspended")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("viewer")).toBeInTheDocument();
  });

  it("says when the user does not exist", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-9": () => problem(404, "Not found", { type: "not-found" }),
    });
    renderApp("/admin/users/u-9");
    expect(await screen.findByRole("heading", { name: "User not found" })).toBeInTheDocument();
  });

  it("disables a user after a confirmation", async () => {
    let current = detail();
    const fetchMock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(current),
      "POST /api/v1/admin/users/u-1/disable": () => {
        current = detail({ status: "disabled" }, 0);
        return json(current);
      },
    });
    renderApp("/admin/users/u-1");
    await userEvent.click(await screen.findByRole("button", { name: "Disable" }));
    const dialog = screen.getByRole("dialog", { name: "Disable alice@alpha.example?" });
    expect(requests(fetchMock, "POST", "/api/v1/admin/users/u-1/disable")).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole("button", { name: "Disable user" }));
    expect(await screen.findByRole("button", { name: "Enable" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(requests(fetchMock, "POST", "/api/v1/admin/users/u-1/disable")).toHaveLength(1);
  });

  it("enables a disabled user", async () => {
    let current = detail({ status: "disabled" }, 0);
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(current),
      "POST /api/v1/admin/users/u-1/enable": () => {
        current = detail();
        return json(current);
      },
    });
    renderApp("/admin/users/u-1");
    await userEvent.click(await screen.findByRole("button", { name: "Enable" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Enable user" }),
    );
    expect(await screen.findByRole("button", { name: "Disable" })).toBeInTheDocument();
  });

  it("does not offer to disable the system admin", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () =>
        json(detail({ isPlatformAdmin: true, isSystemAdmin: true, login: "admin" })),
    });
    renderApp("/admin/users/u-1");
    expect(
      await screen.findByText("The system admin cannot be disabled here."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disable" })).toBeNull();
  });

  it("signs the user out everywhere", async () => {
    let sessions = 2;
    const fetchMock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(detail({}, sessions)),
      "POST /api/v1/admin/users/u-1/sessions/revoke": () => {
        const n = sessions;
        sessions = 0;
        return json({ sessionsRevoked: n });
      },
    });
    renderApp("/admin/users/u-1");
    await userEvent.click(await screen.findByRole("button", { name: "Sign out everywhere" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Sign out everywhere" }),
    );
    expect(await screen.findByText("Signed out: 2 sessions ended")).toBeInTheDocument();
    expect(requests(fetchMock, "POST", "/api/v1/admin/users/u-1/sessions/revoke")).toHaveLength(1);
  });

  it("shows a refusal from the API", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(detail()),
      "POST /api/v1/admin/users/u-1/disable": () =>
        problem(409, "Conflict", { type: "urn:proreta:problem:system-admin", detail: "Not here." }),
    });
    renderApp("/admin/users/u-1");
    await userEvent.click(await screen.findByRole("button", { name: "Disable" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Disable user" }),
    );
    expect(await screen.findByText("Not here.")).toBeInTheDocument();
  });
});

describe("admin user: platform admin", () => {
  it("grants the flag after a confirmation", async () => {
    let current = detail();
    const fetchMock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(current),
      "PUT /api/v1/admin/users/u-1/platform-admin": () => {
        current = detail({ isPlatformAdmin: true });
        return json(current);
      },
    });
    renderApp("/admin/users/u-1");
    expect(await screen.findByText("This user is not a platform admin.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Grant platform admin" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/full control of the whole instance/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Grant platform admin" }));
    expect(await screen.findByText("Platform admin granted")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Revoke platform admin" })).toBeEnabled();
    const put = requests(fetchMock, "PUT", "/api/v1/admin/users/u-1/platform-admin");
    expect(put).toHaveLength(1);
    expect(await put[0]!.clone().json()).toEqual({ enabled: true });
  });

  it("revokes the flag after a confirmation", async () => {
    let current = detail({ isPlatformAdmin: true });
    const fetchMock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(current),
      "PUT /api/v1/admin/users/u-1/platform-admin": () => {
        current = detail();
        return json(current);
      },
    });
    renderApp("/admin/users/u-1");
    await userEvent.click(await screen.findByRole("button", { name: "Revoke platform admin" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Revoke platform admin" }),
    );
    expect(await screen.findByText("Platform admin revoked")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Grant platform admin" })).toBeEnabled();
    const put = requests(fetchMock, "PUT", "/api/v1/admin/users/u-1/platform-admin");
    expect(await put[0]!.clone().json()).toEqual({ enabled: false });
  });

  it("cannot revoke the system admin and says why", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () =>
        json(detail({ isPlatformAdmin: true, isSystemAdmin: true, login: "root" })),
    });
    renderApp("/admin/users/u-1");
    const button = await screen.findByRole("button", { name: "Revoke platform admin" });
    expect(button).toBeDisabled();
    expect(button).toHaveAccessibleDescription(/configured by the deployment/);
    expect(screen.getByText("System admin")).toBeInTheDocument();
  });

  it("cannot revoke your own access", async () => {
    mockApi({
      "GET /api/v1/auth/me": () =>
        json({ ...session, user: { ...session.user, id: "u-1", isPlatformAdmin: true } }),
      "GET /api/v1/admin/users/u-1": () => json(detail({ isPlatformAdmin: true })),
    });
    renderApp("/admin/users/u-1");
    expect(await screen.findByRole("button", { name: "Revoke platform admin" })).toBeDisabled();
  });

  it("cannot grant a disabled user", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(detail({ status: "disabled" })),
    });
    renderApp("/admin/users/u-1");
    expect(await screen.findByRole("button", { name: "Grant platform admin" })).toBeDisabled();
  });

  it("shows the refusal for the last platform admin", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users/u-1": () => json(detail({ isPlatformAdmin: true })),
      "PUT /api/v1/admin/users/u-1/platform-admin": () =>
        problem(409, "Conflict", {
          type: "urn:proreta:problem:last-platform-admin",
          detail: "Grant platform admin access to another active user first.",
        }),
    });
    renderApp("/admin/users/u-1");
    await userEvent.click(await screen.findByRole("button", { name: "Revoke platform admin" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Revoke platform admin" }),
    );
    expect(
      await screen.findByText("Grant platform admin access to another active user first."),
    ).toBeInTheDocument();
  });

  it("marks admins in the list", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users": () =>
        json({
          items: [
            summary({ id: "u-2", login: "bob@beta.example", isPlatformAdmin: true }),
            summary({ id: "u-3", login: "root", isPlatformAdmin: true, isSystemAdmin: true }),
            summary(),
          ],
          total: 3,
          limit: 25,
          offset: 0,
        }),
    });
    renderApp("/admin/users");
    const rows = within(await screen.findByRole("table")).getAllByRole("row");
    expect(within(rows[1]!).getByText("Admin")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("System admin")).toBeInTheDocument();
    expect(within(rows[3]!).queryByText("Admin")).toBeNull();
  });
});
