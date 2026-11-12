import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { MembershipRole } from "../api/endpoints";
import { AuthProvider } from "../auth/AuthProvider";
import { json, mockApi, permissionsBody, renderApp, session } from "../test/utils";
import { Can, canManageChannelOf, canManageStatusPageOf, permissionsFrom } from "./permissions";

const alpha = { id: "alpha", name: "Alpha", role: "maintainer" as const };
const beta = { id: "beta", name: "Beta", role: "viewer" as const };

describe("permissionsFrom", () => {
  it("grants nothing before the server has answered", () => {
    const can = permissionsFrom(undefined);
    expect(can.ready).toBe(false);
    expect(can.organizationRole).toBeNull();
    expect(can.org("account.use")).toBe(false);
    expect(can.anywhere("monitor.read")).toBe(false);
    expect(can.inProject("alpha", "monitor.read")).toBe(false);
  });

  it("gives a person without an organization role only what their projects give", () => {
    const can = permissionsFrom(permissionsBody(null, [alpha, beta]));
    expect(can.ready).toBe(true);
    expect(can.organizationRole).toBeNull();
    expect(can.org("account.use")).toBe(true);
    expect(can.org("organization.members.read")).toBe(false);
    expect(can.org("channel.manage")).toBe(false);
    expect(can.anywhere("channel.manage")).toBe(true);
    expect(can.inProject("alpha", "channel.manage")).toBe(true);
    expect(can.inProject("beta", "channel.manage")).toBe(false);
    expect(can.inProject("beta", "channel.read")).toBe(true);
    expect(can.inProject("other", "channel.read")).toBe(false);
    expect(can.projectsWith("channel.manage").map((p) => p.name)).toEqual(["Alpha"]);
    expect(can.anywhere("usage.read")).toBe(false);
  });

  it("holds a project permission of the organization role in every project", () => {
    const can = permissionsFrom(permissionsBody("maintainer", [alpha]));
    expect(can.org("usage.read")).toBe(true);
    expect(can.org("channel.manage")).toBe(true);
    expect(can.inProject("not-listed", "channel.manage")).toBe(true);
    expect(can.org("organization.delete")).toBe(false);
  });

  it("decides who manages channels and status pages of a project or of the organization", () => {
    const projectOnly = permissionsFrom(permissionsBody(null, [alpha, beta]));
    expect(canManageChannelOf(projectOnly, "alpha")).toBe(true);
    expect(canManageChannelOf(projectOnly, "beta")).toBe(false);
    expect(canManageChannelOf(projectOnly, null)).toBe(false);
    expect(canManageStatusPageOf(projectOnly, "alpha")).toBe(true);
    expect(canManageStatusPageOf(projectOnly, undefined)).toBe(false);
    const maintainer = permissionsFrom(permissionsBody("maintainer", [alpha]));
    expect(canManageChannelOf(maintainer, null)).toBe(true);
    expect(canManageStatusPageOf(maintainer, null)).toBe(true);
    const member = permissionsFrom(permissionsBody("member", [{ ...alpha, role: "member" }]));
    expect(canManageChannelOf(member, null)).toBe(false);
    expect(canManageStatusPageOf(member, "alpha")).toBe(false);
  });
});

function Probe() {
  return (
    <>
      <Can permission="monitor.create" fallback={<p>cannot create anywhere</p>}>
        <p>can create anywhere</p>
      </Can>
      <Can permission="monitor.create" project="beta" fallback={<p>cannot create in beta</p>}>
        <p>can create in beta</p>
      </Can>
      <Can permission="usage.read" organization fallback={<p>no usage</p>}>
        <p>usage</p>
      </Can>
    </>
  );
}

describe("Can", () => {
  async function renderProbe(role: MembershipRole | null) {
    mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, role }),
      "GET /api/v1/permissions": () =>
        json(permissionsBody(role, [{ ...alpha, role: "member" }, beta])),
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <AuthProvider>
            <Probe />
          </AuthProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  it("renders nothing before the answer, then checks anywhere, in a project and in the organization", async () => {
    await renderProbe(null);
    expect(screen.queryAllByText(/create/)).toHaveLength(0);
    // Member of Alpha, viewer of Beta, no organization role.
    expect(await screen.findByText("can create anywhere")).toBeInTheDocument();
    expect(screen.getByText("cannot create in beta")).toBeInTheDocument();
    expect(screen.getByText("no usage")).toBeInTheDocument();
  });

  it("shows the organization-level content to a role that holds it", async () => {
    await renderProbe("maintainer");
    expect(await screen.findByText("usage")).toBeInTheDocument();
    expect(screen.getByText("can create in beta")).toBeInTheDocument();
  });
});

describe("usePermissions", () => {
  it("asks the server again after the organization changes", async () => {
    let switched = false;
    const fetchMock = mockApi({
      "GET /api/v1/auth/me": () => json({ ...session, role: "owner" }),
      "GET /api/v1/permissions": () => json(permissionsBody(switched ? "viewer" : "owner")),
      "GET /api/v1/monitors": () =>
        json({ summary: { up: 0, down: 0, paused: 0, unknown: 0 }, items: [] }),
      "GET /api/v1/auth/organizations": () =>
        json({
          items: [
            {
              id: "t1",
              slug: "acme",
              name: "Acme",
              role: "owner",
              status: "active",
              current: true,
            },
            {
              id: "t2",
              slug: "beta",
              name: "Beta",
              role: "viewer",
              status: "active",
              current: false,
            },
          ],
        }),
      "POST /api/v1/auth/switch-organization": () => {
        switched = true;
        return json({
          ...session,
          organization: { id: "t2", slug: "beta", name: "Beta" },
          role: "viewer",
        });
      },
    });
    renderApp("/");
    expect(await screen.findByRole("link", { name: "Add monitor" })).toBeInTheDocument();
    const banner = await screen.findByRole("banner");
    await userEvent.selectOptions(await within(banner).findByLabelText("Organization"), "t2");
    await waitFor(() => expect(screen.queryByRole("link", { name: "Add monitor" })).toBeNull());
    expect(
      fetchMock.mock.calls.filter(
        ([r]) => new URL((r as Request).url).pathname === "/api/v1/permissions",
      ),
    ).toHaveLength(2);
  });
});
