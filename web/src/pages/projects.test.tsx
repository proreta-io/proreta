import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  Channel,
  Invitation,
  Member,
  MonitorListItem,
  Project,
  ProjectMember,
} from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const as = (role: string | null) => ({
  "GET /api/v1/auth/me": () => json({ ...session, role }),
});

const project = (over: Partial<Project>): Project => ({
  id: "p",
  name: "P",
  slug: "p",
  isDefault: false,
  allMembers: false,
  role: "owner",
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
  monitorCount: 2,
});
const web = project({ id: "w", name: "Web", slug: "web" });

const pm = (over: Partial<ProjectMember>): ProjectMember => ({
  userId: "u2",
  login: "bob@example.com",
  displayName: "",
  organizationRole: "member",
  projectRole: null,
  effectiveRole: "member",
  explicit: true,
  allProjects: false,
  ...over,
});

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

afterEach(() => localStorage.clear());

describe("projects page", () => {
  it("lets a maintainer create, rename and delete a project after confirming", async () => {
    let items = [def, web];
    const fetchMock = mockApi({
      ...as("maintainer"),
      "GET /api/v1/projects": () => json({ items }),
      "POST /api/v1/projects": async (r) => {
        const body = (await r.json()) as { name: string };
        const created = project({ id: "n", name: body.name, slug: "payments", role: "maintainer" });
        items = [...items, created];
        return json(created, 201);
      },
      "PATCH /api/v1/projects/w": async (r) => {
        const body = (await r.json()) as { name: string };
        items = items.map((p) => (p.id === "w" ? { ...p, name: body.name } : p));
        return json(items[1]);
      },
      "DELETE /api/v1/projects/w": () => {
        items = items.filter((p) => p.id !== "w");
        return new Response(null, { status: 204 });
      },
    });
    renderApp("/projects");
    expect(await screen.findByRole("row", { name: /Default/ })).toHaveTextContent("Default");
    expect(screen.getByRole("button", { name: "Delete Default" })).toBeDisabled();

    await userEvent.type(screen.getByLabelText("New project"), "Payments");
    await userEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByRole("row", { name: /Payments/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Rename Web" }));
    const name = screen.getByLabelText("Project name");
    await userEvent.clear(name);
    await userEvent.type(name, "Website{Enter}");
    expect(await screen.findByRole("row", { name: /Website/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Delete Website" }));
    const dialog = await screen.findByRole("dialog");
    expect(requests(fetchMock, "DELETE", "/api/v1/projects/w")).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete project" }));
    await waitFor(() =>
      expect(screen.queryByRole("row", { name: /Website/ })).not.toBeInTheDocument(),
    );
    expect(requests(fetchMock, "DELETE", "/api/v1/projects/w")).toHaveLength(1);
  });

  it("does not offer to delete a project that still has monitors", async () => {
    const fetchMock = mockApi({
      ...as("owner"),
      "GET /api/v1/projects": () => json({ items: [def, { ...web, monitorCount: 3 }] }),
    });
    renderApp("/projects");
    await userEvent.click(await screen.findByRole("button", { name: "Delete Web" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("3 monitors");
    await userEvent.click(within(dialog).getByRole("button", { name: "OK" }));
    expect(requests(fetchMock, "DELETE", "/api/v1/projects/w")).toHaveLength(0);
  });

  it("manages the people of a project", async () => {
    let people = [
      pm({
        userId: "u1",
        login: "admin",
        organizationRole: "owner",
        effectiveRole: "owner",
        explicit: false,
        allProjects: true,
      }),
    ];
    const orgMembers: Member[] = [
      {
        userId: "u2",
        login: "bob@example.com",
        displayName: "",
        role: "member",
        joinedAt: "",
        isPlatformAdmin: false,
        isSystemAdmin: false,
      },
    ];
    const bodies: unknown[] = [];
    mockApi({
      ...as("owner"),
      "GET /api/v1/projects": () => json({ items: [def, web] }),
      "GET /api/v1/members": () => json({ items: orgMembers }),
      "GET /api/v1/projects/w/members": () => json({ items: people }),
      "PUT /api/v1/projects/w/members/u2": async (r) => {
        const body = (await r.json()) as { role: "member" | "viewer" | "maintainer" };
        bodies.push(body);
        const m = pm({
          projectRole: body.role,
          effectiveRole: body.role === "maintainer" ? "maintainer" : "member",
        });
        people = [people[0]!, m];
        return json(m);
      },
    });
    renderApp("/projects");
    await userEvent.click(await screen.findByRole("button", { name: "People in Web" }));
    expect(await screen.findByRole("row", { name: /admin/ })).toHaveTextContent(
      "Organization role",
    );
    await userEvent.selectOptions(screen.getByLabelText("Add someone"), "u2");
    await userEvent.click(screen.getByRole("button", { name: "Add to project" }));
    const role = await screen.findByRole("combobox", { name: "Role of bob@example.com in Web" });
    await userEvent.selectOptions(role, "viewer");
    await waitFor(() => expect(bodies).toEqual([{ role: "member" }, { role: "viewer" }]));
  });

  it("shows members a read-only list without management controls", async () => {
    mockApi({
      ...as("member"),
      "GET /api/v1/projects": () =>
        json({
          items: [
            { ...def, role: "member" },
            { ...web, role: "viewer" },
          ],
        }),
    });
    renderApp("/projects");
    expect(await screen.findByRole("row", { name: /Web/ })).toHaveTextContent("Viewer");
    expect(screen.queryByLabelText("New project")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Web" })).not.toBeInTheDocument();
  });
});

describe("projects page with two-level roles", () => {
  it("lets a project maintainer without an organization role manage their project only", async () => {
    const bodies: unknown[] = [];
    const people = [
      pm({
        userId: "u1",
        login: null,
        displayName: "Dana",
        organizationRole: null,
        projectRole: "maintainer",
        effectiveRole: "maintainer",
      }),
      pm({
        userId: "u2",
        organizationRole: "viewer",
        effectiveRole: "viewer",
        explicit: false,
        allProjects: true,
      }),
      pm({
        userId: "u3",
        login: null,
        displayName: "Olga",
        organizationRole: null,
        projectRole: "owner",
        effectiveRole: "owner",
      }),
    ];
    mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, role: null }),
      "GET /api/v1/projects": () =>
        json({
          items: [
            { ...web, role: "maintainer" },
            project({ id: "x", name: "Other", role: "viewer" }),
          ],
        }),
      "GET /api/v1/projects/w/members": () => json({ items: people }),
      "PUT /api/v1/projects/w/members/u2": async (r) => {
        bodies.push(await r.json());
        return json({
          ...people[1]!,
          projectRole: "maintainer",
          effectiveRole: "maintainer",
          explicit: true,
        });
      },
    });
    renderApp("/projects");
    // Organization-level controls are not offered; the project's own are.
    expect(await screen.findByRole("row", { name: /Web/ })).toHaveTextContent("Maintainer");
    expect(screen.queryByLabelText("New project")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rename Web" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Web" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rename Other" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "People in Web" }));
    // The inherited organization role shows; a maintainer grants up to maintainer, never owner,
    // and cannot touch the project owner.
    const viewerRole = await screen.findByRole("combobox", {
      name: "Role of bob@example.com in Web",
    });
    expect(
      within(viewerRole)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Inherited: Viewer", "Maintainer", "Member", "Viewer"]);
    expect(screen.queryByRole("combobox", { name: "Role of Olga in Web" })).not.toBeInTheDocument();
    expect(screen.getByRole("row", { name: /Olga/ })).toHaveTextContent("Owner");
    expect(screen.getByRole("row", { name: /bob@example.com/ })).toHaveTextContent(
      "Organization role",
    );
    // No organization member list for a person without an organization role: no add form.
    expect(screen.queryByLabelText("Add someone")).not.toBeInTheDocument();
    await userEvent.selectOptions(viewerRole, "maintainer");
    await waitFor(() => expect(bodies).toEqual([{ role: "maintainer" }]));
  });
});

describe("project invitations", () => {
  const inv = (over: Partial<Invitation>): Invitation => ({
    id: "i1",
    role: "member",
    projectId: "w",
    projectName: "Web",
    label: "Carol",
    status: "open",
    createdAt: "2026-11-01T00:00:00Z",
    expiresAt: "2026-11-08T00:00:00Z",
    createdBy: "dana",
    acceptedBy: null,
    acceptedAt: null,
    revokedAt: null,
    ...over,
  });

  it("lets a project maintainer invite into the project with a role up to their own", async () => {
    let items = [inv({})];
    const bodies: unknown[] = [];
    const fetchMock = mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, role: null }),
      "GET /api/v1/projects": () => json({ items: [{ ...web, role: "maintainer" }] }),
      "GET /api/v1/projects/w/members": () => json({ items: [] }),
      "GET /api/v1/invitations": () => json({ items }),
      "POST /api/v1/invitations": async (r) => {
        const body = (await r.json()) as { role: "member" | "viewer" | "maintainer" };
        bodies.push(body);
        const created = inv({ id: "i2", role: body.role, label: "" });
        items = [created, ...items];
        return json(
          {
            invitation: created,
            inviteUrl: "https://proreta.example.com/invite#SECRET",
            emailQueued: false,
          },
          201,
        );
      },
      "POST /api/v1/invitations/i1/revoke": () => {
        items = items.map((i) => (i.id === "i1" ? { ...i, status: "revoked" } : i));
        return new Response(null, { status: 204 });
      },
    });
    renderApp("/projects");
    await userEvent.click(await screen.findByRole("button", { name: "People in Web" }));
    const list = await screen.findByRole("heading", { name: "Invitations to Web" });
    expect(list).toBeInTheDocument();
    expect(await screen.findByRole("row", { name: /Carol/ })).toHaveTextContent("Member");
    expect(new URL(requests(fetchMock, "GET", "/api/v1/invitations")[0]!.url).search).toBe(
      "?projectId=w",
    );

    await userEvent.click(screen.getByRole("button", { name: "Invite someone" }));
    const dialog = await screen.findByRole("dialog");
    const role = within(dialog).getByLabelText("Role in this project");
    expect(
      within(role)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Maintainer", "Member", "Viewer"]);
    await userEvent.selectOptions(role, "viewer");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create link" }));
    expect(
      await screen.findByDisplayValue("https://proreta.example.com/invite#SECRET"),
    ).toBeInTheDocument();
    expect(bodies).toEqual([{ role: "viewer", projectId: "w" }]);

    await userEvent.click(screen.getByRole("button", { name: "Revoke the invitation Carol" }));
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Revoke invitation" }),
    );
    await waitFor(() =>
      expect(requests(fetchMock, "POST", "/api/v1/invitations/i1/revoke")).toHaveLength(1),
    );
  });

  it("offers no invitations to people who only read or write monitors in the project", async () => {
    mockApi({
      ...as("member"),
      "GET /api/v1/projects": () => json({ items: [{ ...web, role: "member" }] }),
      "GET /api/v1/projects/w/members": () => json({ items: [] }),
    });
    renderApp("/projects");
    await userEvent.click(await screen.findByRole("button", { name: "People in Web" }));
    await screen.findByRole("heading", { name: "People in Web" });
    expect(screen.queryByRole("heading", { name: "Invitations to Web" })).not.toBeInTheDocument();
  });
});

const listItem = (id: string, projectId: string): MonitorListItem => ({
  id,
  projectId,
  name: id,
  type: "http",
  notificationsEnabled: true,
  paused: false,
  status: "up",
  statusSince: new Date().toISOString(),
  lastErrorKind: null,
  lastCheckedAt: null,
  ongoingIncident: null,
  uptime24h: null,
  http: {
    url: "https://example.com/",
    intervalSeconds: 60,
    timeoutSeconds: 10,
    expectedStatus: "2xx",
    method: "GET",
    headerNames: [],
    bodySet: false,
    failureThreshold: 2,
    tlsExpiresAt: null,
    lastResponseMs: null,
  },
});

describe("project switcher", () => {
  it("filters the monitors and remembers the choice", async () => {
    const fetchMock = mockApi({
      ...as("member"),
      "GET /api/v1/projects": () =>
        json({
          items: [
            { ...def, role: "member" },
            { ...web, role: "member" },
          ],
        }),
      "GET /api/v1/monitors": (r) => {
        const p = new URL(r.url).searchParams.get("projectId");
        const all = [listItem("in-default", "d"), listItem("in-web", "w")];
        return json({
          items: all.filter((m) => !p || m.projectId === p),
          summary: { up: 2, down: 0, paused: 0, unknown: 0 },
        });
      },
    });
    const view = renderApp("/");
    const select = await screen.findByRole("combobox", { name: "Project filter" });
    expect(within(select).getByRole("option", { name: "All projects" })).toBeInTheDocument();
    expect(await screen.findByText("in-default")).toBeInTheDocument();
    await userEvent.selectOptions(select, "w");
    await waitFor(() => expect(screen.queryByText("in-default")).not.toBeInTheDocument());
    expect(screen.getByText("in-web")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([r]) => new URL((r as Request).url).search === "?projectId=w"),
    ).toBe(true);
    expect(localStorage.getItem("proreta-project:u1:t1")).toBe("w");
    view.unmount();
  });
});

describe("monitor form with projects", () => {
  const ch = (id: string, projectId: string | null): Channel => ({
    id,
    projectId,
    name: id,
    type: "slack_webhook",
    configHidden: false,
    events: ["down", "recovered", "tls_expiring"],
    mention: [],
    reminderMinutes: 0,
    status: "active",
    createdAt: "",
    updatedAt: "",
  });

  it("offers only projects the user may edit and loads that project's channels", async () => {
    const fetchMock = mockApi({
      ...as(null),
      "GET /api/v1/projects": () =>
        json({
          items: [
            { ...def, role: "member" },
            { ...web, role: "member" },
            project({ id: "r", name: "Read only", role: "viewer" }),
          ],
        }),
      "GET /api/v1/usage": () => problem(500, "x"),
      "GET /api/v1/channels": (r) => {
        const p = new URL(r.url).searchParams.get("projectId");
        return json({
          items:
            p === "w"
              ? [ch("web-alerts", "w"), ch("org", null)]
              : [ch("default-alerts", "d"), ch("org", null)],
        });
      },
      "POST /api/v1/monitors": () => problem(500, "stop"),
    });
    renderApp("/monitors/new");
    const field = await screen.findByLabelText("Project");
    expect(within(field).queryByRole("option", { name: "Read only" })).not.toBeInTheDocument();
    expect(await screen.findByLabelText("default-alerts")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("org"));
    await userEvent.selectOptions(field, "w");
    expect(await screen.findByLabelText("web-alerts")).toBeInTheDocument();
    // The organization-wide channel stays selected after the move.
    expect(screen.getByLabelText("org")).toBeChecked();
    expect(screen.getByLabelText("org")).toHaveAccessibleDescription("Organization-wide");
    await userEvent.type(screen.getByLabelText(/Name/), "Shop");
    await userEvent.type(screen.getByLabelText(/URL/), "shop.example.com");
    await userEvent.click(screen.getByRole("button", { name: "Create monitor" }));
    await waitFor(() => expect(requests(fetchMock, "POST", "/api/v1/monitors")).toHaveLength(1));
    const body = (await requests(fetchMock, "POST", "/api/v1/monitors")[0]!.json()) as {
      projectId: string;
      channelIds: string[];
    };
    expect(body.projectId).toBe("w");
    expect(body.channelIds).toEqual(["org"]);
  });
});

describe("remembered project that is gone", () => {
  it("falls back to all projects when the filtered list answers 404", async () => {
    localStorage.setItem("proreta-project:u1:t1", "w");
    let projectsCalls = 0;
    mockApi({
      ...as("member"),
      "GET /api/v1/projects": () => {
        projectsCalls++;
        // A stale list still names Web the first time.
        return json({
          items:
            projectsCalls === 1
              ? [
                  { ...def, role: "member" },
                  { ...web, role: "member" },
                ]
              : [{ ...def, role: "member" }],
        });
      },
      "GET /api/v1/monitors": (r) =>
        new URL(r.url).searchParams.get("projectId")
          ? problem(404, "Not found")
          : json({
              items: [listItem("in-default", "d")],
              summary: { up: 1, down: 0, paused: 0, unknown: 0 },
            }),
    });
    renderApp("/");
    expect(await screen.findByText("in-default")).toBeInTheDocument();
    expect(localStorage.getItem("proreta-project:u1:t1")).toBeNull();
    await waitFor(() => expect(projectsCalls).toBeGreaterThan(1));
  });
});
