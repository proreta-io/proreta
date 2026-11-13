import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminMember, AdminOrganization } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session, signedIn } from "../test/utils";

const adminSession = { ...session, user: { ...session.user, isPlatformAdmin: true } };
const asAdmin = { "GET /api/v1/auth/me": () => json(adminSession) };

const organization = (over: Partial<AdminOrganization> = {}): AdminOrganization => ({
  id: "t-1",
  slug: "t-abcdefghij",
  name: "alpha.example",
  planKey: "free",
  planName: "Free",
  status: "active",
  createdAt: "2026-10-01T00:00:00Z",
  deletedAt: null,
  users: 1,
  monitors: 2,
  heartbeats: 1,
  ...over,
});

const member = (over: Partial<AdminMember> = {}): AdminMember => ({
  userId: "u-1",
  login: "alice@alpha.example",
  displayName: "",
  role: "owner",
  status: "active",
  joinedAt: "2026-10-01T00:00:00Z",
  isPlatformAdmin: false,
  isSystemAdmin: false,
  ...over,
});

const stats = {
  organizations: { total: 3, active: 2, suspended: 1, deleted: 1 },
  users: 4,
  monitors: 7,
  heartbeats: 2,
  plans: [
    { key: "free", name: "Free", organizations: 2 },
    { key: "internal", name: "Internal", organizations: 1 },
  ],
};

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

describe("admin area guard", () => {
  it("is not found for a user without the platform-admin flag and has no top-bar link", async () => {
    const fetchMock = mockApi({ ...signedIn });
    renderApp("/admin/organizations");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
    expect(within(screen.getByRole("banner")).queryByRole("link", { name: "Admin" })).toBeNull();
    expect(fetchMock.mock.calls.some(([r]) => (r as Request).url.includes("/api/v1/admin/"))).toBe(
      false,
    );
  });

  it("shows the overview to the platform admin", async () => {
    mockApi({ ...asAdmin, "GET /api/v1/admin/stats": () => json(stats) });
    renderApp("/admin");
    expect(await screen.findByRole("heading", { name: "Admin" })).toBeInTheDocument();
    expect(await screen.findByText("3")).toBeInTheDocument();
    expect(screen.getByText("2 active · 1 suspended · 1 deleted")).toBeInTheDocument();
    const rows = within(screen.getByRole("table")).getAllByRole("row");
    expect(rows).toHaveLength(3);
    expect(within(rows[2]!).getByText("Internal")).toBeInTheDocument();
  });

  it("explains a 403 from the API", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/stats": () =>
        problem(403, "Not allowed", {
          type: "urn:proreta:problem:forbidden",
          detail: "Only the platform administrator may do this.",
        }),
    });
    renderApp("/admin");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Only the platform administrator may do this.",
    );
  });
});

describe("admin organizations list", () => {
  it("renders, searches and pages through organizations", async () => {
    const page1 = Array.from({ length: 25 }, (_, i) =>
      organization({ id: `t-${i}`, name: `organization-${i}.example`, slug: `t-${i}` }),
    );
    page1[1] = organization({ id: "t-1", name: "suspended.example", status: "suspended" });
    const fetchMock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/organizations": (r) => {
        const q = new URL(r.url).searchParams;
        if (q.get("query") === "alpha")
          return json({ items: [organization()], total: 1, limit: 25, offset: 0 });
        if (q.get("offset") === "25")
          return json({
            items: [organization({ id: "t-last", name: "last.example" })],
            total: 26,
            limit: 25,
            offset: 25,
          });
        return json({ items: page1, total: 26, limit: 25, offset: 0 });
      },
    });
    renderApp("/admin/organizations");
    expect(await screen.findByText("organization-0.example")).toBeInTheDocument();
    expect(screen.getByText("1–25 of 26")).toBeInTheDocument();
    expect(screen.getByText("Suspended")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("last.example")).toBeInTheDocument();
    expect(screen.getByText("26–26 of 26")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();

    await userEvent.type(screen.getByLabelText("Search organizations"), "alpha");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("alpha.example")).toBeInTheDocument();
    const last = requests(fetchMock, "GET", "/api/v1/admin/organizations").at(-1)!;
    const params = new URL(last.url).searchParams;
    expect(params.get("query")).toBe("alpha");
    expect(params.get("offset")).toBe("0");
    expect(params.get("limit")).toBe("25");
    expect(params.get("includeDeleted")).toBe("false");

    await userEvent.click(screen.getByLabelText("Show deleted organizations"));
    await waitFor(() =>
      expect(
        new URL(
          requests(fetchMock, "GET", "/api/v1/admin/organizations").at(-1)!.url,
        ).searchParams.get("includeDeleted"),
      ).toBe("true"),
    );
  });

  it("opens the details when a row is clicked", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/organizations": () =>
        json({ items: [organization()], total: 1, limit: 25, offset: 0 }),
      "GET /api/v1/admin/organizations/t-1": () => json(organization()),
      "GET /api/v1/admin/stats": () => json(stats),
    });
    renderApp("/admin/organizations");
    await userEvent.click(await screen.findByText("Free"));
    expect(
      await screen.findByRole("heading", { name: "alpha.example", level: 1 }),
    ).toBeInTheDocument();
  });

  it("says when nothing matches", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/organizations": () => json({ items: [], total: 0, limit: 25, offset: 0 }),
    });
    renderApp("/admin/organizations?query=zzz");
    expect(await screen.findByText("No organizations match “zzz”.")).toBeInTheDocument();
  });
});

describe("admin organization details", () => {
  let patchBodies: unknown[] = [];
  function details(extra: Record<string, (r: Request) => Response | Promise<Response>> = {}) {
    let current = organization();
    patchBodies = [];
    return mockApi({
      ...asAdmin,
      "GET /api/v1/admin/stats": () => json(stats),
      "GET /api/v1/admin/organizations/t-1": () => json(current),
      "GET /api/v1/admin/organizations/t-1/members": () =>
        json({
          items: [
            member(),
            member({
              userId: "u-admin",
              login: "admin",
              role: "maintainer",
              isPlatformAdmin: true,
              isSystemAdmin: true,
            }),
          ],
        }),
      "PATCH /api/v1/admin/organizations/t-1": async (r) => {
        const body = (await r.json()) as { planKey?: string; status?: "active" | "suspended" };
        patchBodies.push(body);
        current = { ...current, ...(body.status ? { status: body.status } : {}) };
        if (body.planKey) current = { ...current, planKey: body.planKey, planName: "Internal" };
        return json(current);
      },
      "DELETE /api/v1/admin/organizations/t-1": () => {
        current = { ...current, status: "suspended", deletedAt: "2026-10-21T10:00:00Z" };
        return new Response(null, { status: 204 });
      },
      ...extra,
    });
  }

  it("offers the community plan", async () => {
    details({
      "GET /api/v1/admin/stats": () =>
        json({
          ...stats,
          plans: [{ key: "community", name: "Community", organizations: 0 }, ...stats.plans],
        }),
    });
    renderApp("/admin/organizations/t-1");
    const select = await screen.findByLabelText("Plan");
    await waitFor(() =>
      expect(
        within(select)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["Community", "Free", "Internal"]),
    );
  });

  it("suspends and unsuspends only after confirming", async () => {
    const fetchMock = details();
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Suspend" }));
    const dialog = screen.getByRole("dialog", { name: "Suspend alpha.example?" });
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(requests(fetchMock, "PATCH", "/api/v1/admin/organizations/t-1")).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: "Suspend" }));
    await userEvent.click(screen.getByRole("button", { name: "Suspend organization" }));
    expect(await screen.findByText("Organization suspended")).toBeInTheDocument();
    const patch = requests(fetchMock, "PATCH", "/api/v1/admin/organizations/t-1")[0]!;
    expect(patchBodies).toEqual([{ status: "suspended" }]);
    expect(patch.headers.get("X-CSRF-Token")).toBe("csrf-1");

    await userEvent.click(await screen.findByRole("button", { name: "Unsuspend" }));
    await userEvent.click(screen.getByRole("button", { name: "Unsuspend organization" }));
    expect(await screen.findByText("Organization unsuspended")).toBeInTheDocument();
  });

  it("deletes after confirming and then offers no more changes", async () => {
    const fetchMock = details();
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete organization" }));
    expect(await screen.findByText("Organization deleted")).toBeInTheDocument();
    expect(requests(fetchMock, "DELETE", "/api/v1/admin/organizations/t-1")).toHaveLength(1);
    expect(await screen.findByText(/A deleted organization cannot be changed/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Suspend" })).toBeNull();
  });

  it("changes the plan and shows a 422 next to the selector", async () => {
    let fail = true;
    const fetchMock = details({
      "PATCH /api/v1/admin/organizations/t-1": async () =>
        fail
          ? problem(422, "Validation failed", {
              type: "urn:proreta:problem:validation",
              fieldErrors: [{ field: "planKey", message: "unknown plan" }],
            })
          : json(organization({ planKey: "internal", planName: "Internal" })),
    });
    renderApp("/admin/organizations/t-1");
    const select = await screen.findByLabelText("Plan");
    await waitFor(() => expect(within(select).getAllByRole("option")).toHaveLength(2));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    await userEvent.selectOptions(select, "internal");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Plan: unknown plan.")).toBeInTheDocument();
    expect(select).toHaveAttribute("aria-invalid", "true");

    fail = false;
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Plan changed to Internal")).toBeInTheDocument();
    const last = requests(fetchMock, "PATCH", "/api/v1/admin/organizations/t-1").at(-1)!;
    expect(await last.json()).toEqual({ planKey: "internal" });
  });

  it("shows a 409 guard rail as a toast", async () => {
    details({
      "PATCH /api/v1/admin/organizations/t-1": () =>
        problem(409, "This organization cannot be suspended or deleted", {
          type: "urn:proreta:problem:protected-organization",
          detail:
            "The default organization and the organization you belong to cannot be suspended or deleted.",
        }),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Suspend" }));
    await userEvent.click(screen.getByRole("button", { name: "Suspend organization" }));
    expect(
      await screen.findByText(
        "The default organization and the organization you belong to cannot be suspended or deleted.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("offers no suspension or deletion of the platform's own organization", async () => {
    details({
      "GET /api/v1/admin/organizations/t-1": () => json(organization({ slug: "default" })),
    });
    renderApp("/admin/organizations/t-1");
    expect(await screen.findByText(/platform's own organization/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Suspend" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
  });

  it("shows not found for an unknown organization", async () => {
    details({ "GET /api/v1/admin/organizations/t-1": () => problem(404, "Not found") });
    renderApp("/admin/organizations/t-1");
    expect(
      await screen.findByRole("heading", { name: "Organization not found" }),
    ).toBeInTheDocument();
  });

  it("shows a temporary password once, only in memory, and never for the system admin", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const fetchMock = details({
      "POST /api/v1/admin/users/u-1/reset-password": () =>
        json({ temporaryPassword: "abcdefghijkmnpqrstuvwxyz" }),
      "GET /api/v1/admin/organizations": () =>
        json({ items: [organization()], total: 1, limit: 25, offset: 0 }),
    });
    renderApp("/admin/organizations/t-1");
    const rows = within((await screen.findAllByRole("table"))[0]!).getAllByRole("row");
    const adminRow = rows.find((r) => within(r).queryByText("admin"))!;
    const adminReset = within(adminRow).getByRole("button", { name: "Reset password of admin" });
    expect(adminReset).toBeDisabled();
    expect(adminReset).toHaveAccessibleDescription(
      "Managed by the proreta-admin Secret, not here.",
    );

    const aliceRow = rows.find((r) => within(r).queryByText("alice@alpha.example"))!;
    await userEvent.click(
      within(aliceRow).getByRole("button", { name: "Reset password of alice@alpha.example" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Reset the password of alice@alpha.example?",
    });
    await userEvent.click(within(dialog).getByRole("button", { name: "Reset password" }));

    const field = await screen.findByLabelText("Temporary password");
    expect(field).toHaveValue("abcdefghijkmnpqrstuvwxyz");
    expect(field).toHaveAccessibleDescription(/shown only once/);
    expect(requests(fetchMock, "POST", "/api/v1/admin/users/u-1/reset-password")).toHaveLength(1);

    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await userEvent.click(screen.getByRole("button", { name: "Copy password" }));
    expect(writeText).toHaveBeenCalledWith("abcdefghijkmnpqrstuvwxyz");
    expect(await screen.findByRole("button", { name: "Copied ✓" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "I handed it over, hide it" }));
    expect(screen.queryByLabelText("Temporary password")).toBeNull();

    // Shown again after another reset, then gone after leaving the page.
    await userEvent.click(
      within(aliceRow).getByRole("button", { name: "Reset password of alice@alpha.example" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Reset password" }),
    );
    expect(await screen.findByLabelText("Temporary password")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "← Organizations" }));
    expect(await screen.findByRole("heading", { name: "Organizations" })).toBeInTheDocument();
    await userEvent.click(await screen.findByText("alpha.example"));
    expect(
      await screen.findByRole("heading", { name: "alpha.example", level: 1 }),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Temporary password")).toBeNull();
    expect(document.body.textContent).not.toContain("abcdefghijkmnpqrstuvwxyz");
    expect(setItem.mock.calls.flat().join(" ")).not.toContain("abcdefghijkmnpqrstuvwxyz");
  });

  it("keeps no temporary password in the mutation cache", async () => {
    details({
      "POST /api/v1/admin/users/u-1/reset-password": () =>
        json({ temporaryPassword: "abcdefghijkmnpqrstuvwxyz" }),
    });
    const { queryClient } = renderApp("/admin/organizations/t-1");
    const cached = () =>
      JSON.stringify(
        queryClient
          .getMutationCache()
          .getAll()
          .map((m) => m.state),
      );
    const rows = within((await screen.findAllByRole("table"))[0]!).getAllByRole("row");
    const aliceRow = rows.find((r) => within(r).queryByText("alice@alpha.example"))!;
    await userEvent.click(
      within(aliceRow).getByRole("button", { name: "Reset password of alice@alpha.example" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Reset password" }),
    );
    expect(await screen.findByLabelText("Temporary password")).toHaveValue(
      "abcdefghijkmnpqrstuvwxyz",
    );
    // Copied into the panel's state; the mutation itself no longer holds it.
    await waitFor(() => expect(cached()).not.toContain("temporaryPassword"));

    await userEvent.click(screen.getByRole("button", { name: "I handed it over, hide it" }));
    expect(screen.queryByLabelText("Temporary password")).toBeNull();
    await waitFor(() => expect(cached()).not.toContain("temporaryPassword"));
    expect(cached()).not.toContain("abcdefghijkmnpqrstuvwxyz");
  });

  it("shows a 409 for the system admin and other reset errors as a toast", async () => {
    details({
      "POST /api/v1/admin/users/u-1/reset-password": () =>
        problem(409, "The system administrator's password cannot be reset here", {
          type: "urn:proreta:problem:system-admin",
          detail: "Its credentials come from the proreta-admin Secret.",
        }),
    });
    renderApp("/admin/organizations/t-1");
    const rows = within((await screen.findAllByRole("table"))[0]!).getAllByRole("row");
    const aliceRow = rows.find((r) => within(r).queryByText("alice@alpha.example"))!;
    await userEvent.click(
      within(aliceRow).getByRole("button", { name: "Reset password of alice@alpha.example" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Reset password" }),
    );
    expect(
      await screen.findByText("Its credentials come from the proreta-admin Secret."),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Temporary password")).toBeNull();
  });
});

describe("login of a suspended account", () => {
  it("explains the suspension after a correct password", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => problem(401, "Unauthenticated"),
      "POST /api/v1/auth/login": () =>
        problem(403, "Account suspended", { type: "urn:proreta:problem:account-suspended" }),
    });
    renderApp("/login");
    await userEvent.type(await screen.findByLabelText("Email or login"), "alice@alpha.example");
    await userEvent.type(screen.getByLabelText("Password"), "right password");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "This account is suspended. Contact the administrator of this Proreta installation.",
    );
  });
});
