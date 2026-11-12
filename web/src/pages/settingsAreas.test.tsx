import { screen, within } from "@testing-library/react";
import type { MembershipRole, PermissionDefinition } from "../api/endpoints";
import { json, mockApi, permissionsBody, renderApp, session } from "../test/utils";

type Persona = {
  name: string;
  role: MembershipRole | null;
  projects: { id: string; name: string; role: MembershipRole }[];
  nav: string[];
  sections: string[];
  organization: string[];
  managed: string[];
};

const everywhere = (role: MembershipRole) => [
  { id: "d", name: "Default", role },
  { id: "a", name: "Alpha", role },
];
const only = (role: MembershipRole) => [{ id: "a", name: "Alpha", role }];

const NAV = ["Monitors", "Status pages", "Channels", "Settings"];
const PEOPLE = ["People", "Projects"];
const MANAGE_ORG = [
  "People and invitations",
  "Projects",
  "Notification channels",
  "Status pages",
  "Plan and usage",
];
const AREAS = (...more: string[]) => ["Roles", ...more, "Appearance", "Account"];

// Written by hand per person, not derived from the permission tables.
const personas: Persona[] = [
  {
    name: "organization owner",
    role: "owner",
    projects: everywhere("owner"),
    nav: NAV,
    sections: AREAS("Organization", "Projects", "Plan", "Sign-in domains"),
    organization: [...MANAGE_ORG.slice(0, 4), "Sign-in domains", "Plan and usage"],
    managed: ["Default", "Alpha"],
  },
  {
    name: "organization maintainer",
    role: "maintainer",
    projects: everywhere("maintainer"),
    nav: NAV,
    sections: AREAS("Organization", "Projects", "Plan"),
    organization: MANAGE_ORG,
    managed: ["Default", "Alpha"],
  },
  {
    name: "organization member",
    role: "member",
    projects: everywhere("member"),
    nav: NAV,
    sections: AREAS("Organization"),
    organization: PEOPLE,
    managed: [],
  },
  {
    name: "organization viewer",
    role: "viewer",
    projects: everywhere("viewer"),
    nav: NAV,
    sections: AREAS("Organization"),
    organization: PEOPLE,
    managed: [],
  },
  {
    name: "project-only owner",
    role: null,
    projects: only("owner"),
    nav: NAV,
    sections: AREAS("Projects"),
    organization: [],
    managed: ["Alpha"],
  },
  {
    name: "project-only maintainer",
    role: null,
    projects: only("maintainer"),
    nav: NAV,
    sections: AREAS("Projects"),
    organization: [],
    managed: ["Alpha"],
  },
  {
    name: "project-only member",
    role: null,
    projects: only("member"),
    nav: NAV,
    sections: AREAS(),
    organization: [],
    managed: [],
  },
  {
    name: "project-only viewer",
    role: null,
    projects: only("viewer"),
    nav: NAV,
    sections: AREAS(),
    organization: [],
    managed: [],
  },
  {
    name: "person with no role and no project",
    role: null,
    projects: [],
    nav: ["Monitors", "Settings"],
    sections: AREAS(),
    organization: [],
    managed: [],
  },
];

const projectBody = (p: Persona["projects"][number]) => ({
  ...p,
  slug: p.id,
  isDefault: p.id === "d",
  allMembers: p.id === "d",
  monitorCount: 0,
  channelCount: 0,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
});

function serve(p: Persona, extra: Record<string, () => Response> = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ ...session, role: p.role }),
    "GET /api/v1/permissions": () => json(permissionsBody(p.role, p.projects)),
    "GET /api/v1/projects": () => json({ items: p.projects.map(projectBody) }),
    "GET /api/v1/monitors": () =>
      json({ summary: { up: 0, down: 0, paused: 0, unknown: 0 }, items: [] }),
    "GET /api/v1/channels": () => json({ items: [] }),
    "GET /api/v1/status-pages": () => json({ items: [] }),
    ...extra,
  });
}

describe.each(personas)("$name", (p) => {
  it("sees the main navigation its permissions allow", async () => {
    serve(p);
    renderApp("/");
    const nav = await screen.findByRole("navigation", { name: "Main" });
    await within(nav).findByRole("link", { name: "Settings" });
    // The permissions arrive after the first paint: wait for the final set of links.
    await screen.findByRole("heading", { name: "Monitors" });
    await vi.waitFor(() =>
      expect(
        within(nav)
          .getAllByRole("link")
          .map((l) => l.textContent),
      ).toEqual(p.nav),
    );
    expect(within(nav).queryByRole("link", { name: "Members" })).toBeNull();
    expect(within(nav).queryByRole("link", { name: "Projects" })).toBeNull();
  });

  it("sees the settings sections its permissions allow", async () => {
    serve(p);
    renderApp("/settings");
    await screen.findByRole("heading", { name: "Settings", level: 1 });
    await vi.waitFor(() =>
      expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(
        p.sections,
      ),
    );
    const org = screen.queryByRole("region", { name: "Organization" });
    expect(
      org
        ? within(org)
            .getAllByRole("link")
            .map((l) => l.textContent)
        : [],
    ).toEqual(p.organization);
    const projects = screen.queryByRole("region", { name: "Projects" });
    expect(
      projects
        ? within(projects)
            .getAllByRole("listitem")
            .map((li) => li.querySelector("strong")?.textContent)
        : [],
    ).toEqual(p.managed);
  });
});

describe("settings links", () => {
  it("lead to the screens that exist, with the project opened for its people", async () => {
    const maintainer = personas[5]!;
    serve(maintainer);
    renderApp("/settings");
    const projects = await screen.findByRole("region", { name: "Projects" });
    expect(
      within(projects).getByRole("link", { name: "People and invitations of Alpha" }),
    ).toHaveAttribute("href", "/projects?project=a");
    expect(within(projects).getByRole("link", { name: "Channels of Alpha" })).toHaveAttribute(
      "href",
      "/channels",
    );
    expect(within(projects).getByRole("link", { name: "Status pages of Alpha" })).toHaveAttribute(
      "href",
      "/status-page",
    );
    expect(screen.getByRole("link", { name: "Roles and permissions" })).toHaveAttribute(
      "href",
      "/help/roles",
    );
  });

  it("show a project member only what the member's role manages", async () => {
    const owner = personas[4]!;
    serve({ ...owner, projects: [{ id: "a", name: "Alpha", role: "member" }] });
    renderApp("/settings");
    await screen.findByRole("heading", { name: "Roles" });
    expect(screen.queryByRole("region", { name: "Projects" })).toBeNull();
  });
});

describe("route guards", () => {
  const guarded = ["/channels", "/status-page", "/projects"];

  it.each(guarded)(
    "shows the not-found page at %s to a person with nothing in any project",
    async (path) => {
      serve(personas[8]!);
      renderApp(path);
      expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
    },
  );

  it.each(guarded)("opens %s for a project-only viewer", async (path) => {
    serve(personas[7]!, { "GET /api/v1/channels": () => json({ items: [] }) });
    renderApp(path);
    expect(await screen.findByRole("heading", { level: 1 })).not.toHaveTextContent(
      "Page not found",
    );
  });

  it("does not flash the not-found page while the permissions load", async () => {
    let answer: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => (answer = resolve));
    const owner = personas[0]!;
    serve(owner, {
      "GET /api/v1/permissions": (() =>
        gate.then(() => json(permissionsBody(owner.role, owner.projects)))) as never,
    });
    renderApp("/channels");
    expect(await screen.findByText("Loading…")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Page not found" })).toBeNull();
    answer();
    expect(
      await screen.findByRole("heading", { name: "Notification channels", level: 1 }),
    ).toBeInTheDocument();
  });
});

const catalogue: PermissionDefinition[] = [
  {
    name: "account.use",
    level: "account",
    description: "Use the session",
    roles: ["owner", "maintainer", "member", "viewer"],
  },
  {
    name: "channel.manage",
    level: "project",
    description: "Change channels",
    roles: ["owner", "maintainer"],
  },
  {
    name: "organization.delete",
    level: "organization",
    description: "Delete the organization",
    roles: ["owner"],
  },
];

describe("role help page", () => {
  it("shows the roles against the permissions and marks the roles of the person", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, role: "viewer" }),
      "GET /api/v1/permissions": () =>
        json({
          ...permissionsBody("viewer", [{ id: "a", name: "Alpha", role: "maintainer" }]),
          catalogue,
        }),
    });
    renderApp("/help/roles");
    expect(
      await screen.findByRole("heading", { name: "Roles and permissions" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/the higher of the two applies/)).toBeInTheDocument();
    expect(screen.getByText(/applies to every project/)).toBeInTheDocument();

    const table = await screen.findByRole("table", { name: "What each role may do" });
    const header = within(table).getAllByRole("columnheader");
    expect(header.map((h) => h.textContent)).toEqual([
      "Permission",
      "Level",
      "Owner",
      "MaintainerYour role in Alpha",
      "Member",
      "ViewerYour organization role",
    ]);
    const row = within(table).getByRole("row", { name: /channel\.manage/ });
    expect(row).toHaveTextContent("Change channels");
    expect(row).toHaveTextContent("Project");
    expect(
      within(row)
        .getAllByRole("cell")
        .map((c) => c.textContent),
    ).toEqual(["channel.manageChange channels", "Project", "Yes", "Yes", "–No", "–No"]);
    expect(within(table).getByRole("row", { name: /organization\.delete/ })).toHaveTextContent(
      "Organization",
    );
    expect(
      screen.getByText(/Your organization role/, { selector: "strong, p" }),
    ).toBeInTheDocument();
  });

  it("says so when the person has no organization role", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, role: null }),
      "GET /api/v1/permissions": () => json({ ...permissionsBody(null, []), catalogue }),
    });
    renderApp("/help/roles");
    expect(await screen.findByText(/you belong through projects only/)).toBeInTheDocument();
  });

  it("is linked from the people screens", async () => {
    const owner = personas[0]!;
    serve(owner, { "GET /api/v1/members": () => json({ items: [] }) });
    renderApp("/projects");
    expect(await screen.findByRole("link", { name: "What each role may do" })).toHaveAttribute(
      "href",
      "/help/roles",
    );
  });
});
