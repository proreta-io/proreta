import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminOrganization, AdminProject, ProjectMember } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const organization = (over: Partial<AdminOrganization> = {}): AdminOrganization => ({
  id: "t-1",
  slug: "alpha",
  name: "alpha.example",
  planKey: "free",
  planName: "Free",
  status: "active",
  createdAt: "2026-10-01T00:00:00Z",
  deletedAt: null,
  users: 2,
  monitors: 1,
  heartbeats: 0,
  ...over,
});

const project = (over: Partial<AdminProject>): AdminProject => ({
  id: "p",
  name: "P",
  slug: "p",
  isDefault: false,
  allMembers: false,
  memberCount: 1,
  monitorCount: 0,
  channelCount: 0,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  ...over,
});
const def = project({
  id: "d",
  name: "Default",
  slug: "default",
  isDefault: true,
  allMembers: true,
  memberCount: 3,
  monitorCount: 1,
});
const web = project({ id: "w", name: "Web", slug: "web" });

const pm = (over: Partial<ProjectMember>): ProjectMember => ({
  userId: "u-owner",
  login: "alice@alpha.example",
  displayName: "",
  organizationRole: "owner",
  projectRole: null,
  effectiveRole: "owner",
  explicit: false,
  allProjects: true,
  ...over,
});

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

const base = "/api/v1/admin/organizations/t-1";

const sent: unknown[] = [];
beforeEach(() => {
  sent.length = 0;
});

function organizationRoutes(over: AdminOrganization, items: () => AdminProject[]) {
  return {
    ...asAdmin,
    [`GET ${base}`]: () => json(over),
    [`GET ${base}/users`]: () => json({ items: [], total: 0, limit: 100, offset: 0 }),
    [`GET ${base}/projects`]: () => json({ items: items() }),
  };
}

describe("admin organization projects", () => {
  it("creates, renames and deletes a project after confirming", async () => {
    let items = [def, web];
    const fetchMock = mockApi({
      ...organizationRoutes(organization(), () => items),
      [`POST ${base}/projects`]: async (r) => {
        const body = (await r.json()) as { name: string };
        sent.push(body);
        items = [...items, project({ id: "n", name: body.name, slug: "payments" })];
        return json(items.at(-1), 201);
      },
      [`PATCH ${base}/projects/w`]: async (r) => {
        const body = (await r.json()) as { name: string };
        items = items.map((p) => (p.id === "w" ? { ...p, name: body.name } : p));
        return json(items[1]);
      },
      [`DELETE ${base}/projects/n`]: () => {
        items = items.filter((p) => p.id !== "n");
        return new Response(null, { status: 204 });
      },
    });
    renderApp("/admin/organizations/t-1");
    expect(await screen.findByRole("heading", { name: "Projects" })).toBeInTheDocument();
    expect(await screen.findByText("Web")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete Default" })).toBeDisabled();

    await userEvent.type(screen.getByLabelText("New project"), "Payments");
    await userEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText("Payments")).toBeInTheDocument();
    expect(sent).toEqual([{ name: "Payments" }]);

    await userEvent.click(screen.getByRole("button", { name: "Rename Web" }));
    const field = screen.getByLabelText("Project name");
    await userEvent.clear(field);
    await userEvent.type(field, "Website");
    await userEvent.click(
      within(screen.getByRole("form", { name: "Rename Web" })).getByRole("button", {
        name: "Save",
      }),
    );
    expect(await screen.findByText("Website")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Delete Payments" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete project" }));
    await waitFor(() => expect(screen.queryByText("Payments")).toBeNull());
    expect(requests(fetchMock, "DELETE", `${base}/projects/n`)).toHaveLength(1);
  });

  it("explains why a project with monitors cannot be deleted and sends nothing", async () => {
    const busy = project({ id: "b", name: "Busy", slug: "busy", monitorCount: 2 });
    const fetchMock = mockApi(organizationRoutes(organization(), () => [def, busy]));
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Delete Busy" }));
    expect(await screen.findByText(/It still has 2 monitors/)).toBeInTheDocument();
    expect(requests(fetchMock, "DELETE", `${base}/projects/b`)).toHaveLength(0);
  });

  it("adds a member, changes the project role and removes it", async () => {
    let listed: ProjectMember[] = [pm({})];
    const fetchMock = mockApi({
      ...organizationRoutes(organization(), () => [def, web]),
      // The people the project picker offers are the organization's members.
      [`GET ${base}/members`]: () =>
        json({
          items: [
            adminMember({ userId: "u-owner", login: "alice@alpha.example", role: "owner" }),
            adminMember({ userId: "u-bob", login: "bob@alpha.example", role: "member" }),
          ],
        }),
      [`GET ${base}/projects/w/members`]: () => json({ items: listed }),
      [`PUT ${base}/projects/w/members/u-bob`]: async (r) => {
        const body = (await r.json()) as { role: "member" | "viewer" };
        sent.push(body);
        const member = pm({
          userId: "u-bob",
          login: "bob@alpha.example",
          organizationRole: "member",
          projectRole: body.role,
          effectiveRole: "member",
          explicit: true,
          allProjects: false,
        });
        listed = [pm({}), member];
        return json(member);
      },
      [`DELETE ${base}/projects/w/members/u-bob`]: () => {
        listed = [pm({})];
        return new Response(null, { status: 204 });
      },
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "People in Web" }));
    await screen.findByRole("heading", { name: "People in Web" });

    await userEvent.selectOptions(await screen.findByLabelText("Add someone"), "u-bob");
    await userEvent.click(screen.getByRole("button", { name: "Add to project" }));
    const role = await screen.findByLabelText("Role of bob@alpha.example in Web");
    expect(sent).toEqual([{ role: "member" }]);

    await userEvent.selectOptions(role, "viewer");
    await waitFor(() =>
      expect(requests(fetchMock, "PUT", `${base}/projects/w/members/u-bob`)).toHaveLength(2),
    );
    expect(sent).toEqual([{ role: "member" }, { role: "viewer" }]);

    await userEvent.click(
      screen.getByRole("button", { name: "Remove bob@alpha.example from Web" }),
    );
    await waitFor(() =>
      expect(requests(fetchMock, "DELETE", `${base}/projects/w/members/u-bob`)).toHaveLength(1),
    );
    await waitFor(() => expect(screen.queryByLabelText(/Role of bob.* in Web/)).toBeNull());
  });

  it("shows a refused change as a message", async () => {
    mockApi({
      ...organizationRoutes(organization(), () => [def]),
      [`POST ${base}/projects`]: () =>
        problem(409, "Plan limit", {
          type: "urn:proreta:problem:slug-taken",
          detail: "This slug is already used by another project",
        }),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.type(await screen.findByLabelText("New project"), "Default");
    await userEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/already used/)).toBeInTheDocument();
  });

  it("is read-only for a deleted organization", async () => {
    mockApi(
      organizationRoutes(organization({ deletedAt: "2026-11-02T00:00:00Z" }), () => [def, web]),
    );
    renderApp("/admin/organizations/t-1");
    expect(await screen.findByText("Web")).toBeInTheDocument();
    expect(screen.queryByLabelText("New project")).toBeNull();
    expect(screen.queryByRole("button", { name: "Rename Web" })).toBeNull();
    expect(within(screen.getByRole("table")).getAllByText("Default").length).toBeGreaterThan(0);
  });
});

function adminMember(over: { userId: string; login: string; role: "owner" | "member" }) {
  return {
    displayName: "",
    status: "active",
    joinedAt: "2026-10-01T00:00:00Z",
    isPlatformAdmin: false,
    isSystemAdmin: false,
    ...over,
  };
}
