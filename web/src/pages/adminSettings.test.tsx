import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminOrganization, AuthDomain, Channel, Specula, StatusPage } from "../api/endpoints";
import { json, mockApi, renderApp, session } from "../test/utils";

const base = "/api/v1/admin/organizations/t-1";

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

const counts = (n: number) => ({
  monitors: n,
  heartbeats: n,
  channels: n,
  members: n,
  projects: n,
  statusPages: n,
  specula: n,
  statusPageDomains: 0,
});
const usage = { plan: { key: "free", name: "Free" }, limits: counts(10), usage: counts(1) };

const domain: AuthDomain = {
  id: "d-1",
  domain: "alpha.example",
  defaultRole: "viewer",
  enabled: true,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};
const channel: Channel = {
  id: "c-1",
  projectId: null,
  name: "ops-alerts",
  type: "slack_webhook",
  configHidden: false,
  events: ["down", "recovered"],
  mention: [],
  reminderMinutes: 60,
  status: "active",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};
const page: StatusPage = {
  id: "s-1",
  projectId: null,
  slug: "alpha-status",
  title: "Alpha status",
  description: "",
  logoUrl: null,
  accentColor: null,
  published: true,
  passwordProtected: true,
  publicUrl: "https://proreta.example/status/alpha-status",
  componentCount: 3,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};
const specula = (over: Partial<Specula> = {}): Specula => ({
  id: "sp-1",
  name: "Prod cluster",
  allProjects: true,
  projectIds: [],
  status: "online",
  version: "1.0.0",
  hostname: "node-1",
  lastSeenAt: new Date().toISOString(),
  createdAt: "2026-10-01T00:00:00Z",
  revokedAt: null,
  ...over,
});

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

function routes(
  org: AdminOrganization,
  extra: Record<string, () => Response | Promise<Response>> = {},
) {
  return {
    ...asAdmin,
    [`GET ${base}`]: () => json(org),
    [`GET ${base}/users`]: () => json({ items: [], total: 0, limit: 100, offset: 0 }),
    [`GET ${base}/projects`]: () => json({ items: [] }),
    [`GET ${base}/usage`]: () => json(usage),
    [`GET ${base}/auth-domains`]: () => json({ items: [domain] }),
    [`GET ${base}/channels`]: () => json({ items: [channel] }),
    [`GET ${base}/status-pages`]: () => json({ items: [page] }),
    [`GET ${base}/specula`]: () => json({ items: [specula()] }),
    ...extra,
  };
}

describe("admin organization settings", () => {
  it("shows every settings area of the organization", async () => {
    mockApi(routes(organization()));
    renderApp("/admin/organizations/t-1");
    const usageSection = (await screen.findByRole("heading", { name: "Plan and usage" })).closest(
      "section",
    ) as HTMLElement;
    expect(await within(usageSection).findByText("Status pages")).toBeInTheDocument();
    expect(within(usageSection).getByText("Specula")).toBeInTheDocument();
    expect(
      await screen.findByText("alpha.example", { selector: "span.font-mono" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("ops-alerts")).toBeInTheDocument();
    expect(screen.getByText(/Read-only\. The webhook URL/)).toBeInTheDocument();
    expect(await screen.findByText("Alpha status")).toBeInTheDocument();
    expect(screen.getByText("Password")).toBeInTheDocument();
    expect(await screen.findByText("Prod cluster")).toBeInTheDocument();
  });

  it("changes and releases a sign-in domain", async () => {
    const fetchMock = mockApi(
      routes(organization(), {
        [`PATCH ${base}/auth-domains/d-1`]: () => json({ ...domain, defaultRole: "member" }),
        [`DELETE ${base}/auth-domains/d-1`]: () => new Response(null, { status: 204 }),
      }),
    );
    renderApp("/admin/organizations/t-1");
    await userEvent.selectOptions(await screen.findByLabelText("Role for alpha.example"), "member");
    await waitFor(() =>
      expect(requests(fetchMock, "PATCH", `${base}/auth-domains/d-1`)).toHaveLength(1),
    );
    expect(
      await requests(fetchMock, "PATCH", `${base}/auth-domains/d-1`)[0]!.clone().json(),
    ).toEqual({
      defaultRole: "member",
    });
    await userEvent.click(screen.getByRole("button", { name: "Release alpha.example" }));
    await userEvent.click(screen.getByRole("button", { name: "Release" }));
    await waitFor(() =>
      expect(requests(fetchMock, "DELETE", `${base}/auth-domains/d-1`)).toHaveLength(1),
    );
  });

  it("registers a Specula and shows its token once, then revokes another", async () => {
    const fetchMock = mockApi(
      routes(organization(), {
        [`POST ${base}/specula`]: () =>
          json(
            { specula: specula({ id: "sp-2", name: "Edge" }), token: "spc_not-a-real-token" },
            201,
          ),
        [`DELETE ${base}/specula/sp-1`]: () => new Response(null, { status: 204 }),
      }),
    );
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Register a Specula" }));
    await userEvent.type(screen.getByLabelText("Name"), "Edge");
    await userEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByText("Edge is registered")).toBeInTheDocument();
    expect(screen.getAllByText(/spc_not-a-real-token/).length).toBeGreaterThan(0);
    expect(await requests(fetchMock, "POST", `${base}/specula`)[0]!.clone().json()).toEqual({
      name: "Edge",
      allProjects: true,
    });
    await userEvent.click(screen.getByRole("button", { name: "I saved the token" }));
    await userEvent.click(await screen.findByRole("button", { name: "Revoke Prod cluster" }));
    await userEvent.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() =>
      expect(requests(fetchMock, "DELETE", `${base}/specula/sp-1`)).toHaveLength(1),
    );
  });

  it("is read-only for a deleted organization and skips the usage", async () => {
    const fetchMock = mockApi(routes(organization({ deletedAt: "2026-11-01T00:00:00Z" })));
    renderApp("/admin/organizations/t-1");
    expect(await screen.findByText("Prod cluster")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Plan and usage" })).toBeNull();
    expect(requests(fetchMock, "GET", `${base}/usage`)).toHaveLength(0);
    expect(screen.queryByRole("button", { name: "Register a Specula" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Revoke Prod cluster" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Release alpha.example" })).toBeNull();
    expect(screen.getByLabelText("Role for alpha.example")).toBeDisabled();
  });
});
