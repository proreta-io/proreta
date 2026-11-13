import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  AdminMember,
  AdminOrganization,
  AdminUserSummary,
  Invitation,
} from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const adminSession = { ...session, user: { ...session.user, isPlatformAdmin: true } };

const organization: AdminOrganization = {
  id: "t-1",
  slug: "t-abcdefghij",
  name: "alpha.example",
  planKey: "free",
  planName: "Free",
  status: "active",
  createdAt: "2026-10-01T00:00:00Z",
  deletedAt: null,
  users: 2,
  monitors: 0,
  heartbeats: 0,
};

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

const summary = (over: Partial<AdminUserSummary> = {}): AdminUserSummary => ({
  id: "u-1",
  login: "alice@alpha.example",
  displayName: "",
  status: "active",
  isPlatformAdmin: false,
  isSystemAdmin: false,
  emailVerified: true,
  mustChangePassword: false,
  createdAt: "2026-10-01T00:00:00Z",
  organizationCount: 1,
  ...over,
});

const invitation = (over: Partial<Invitation> = {}): Invitation => ({
  id: "i-1",
  role: "member",
  label: "Eve",
  status: "open",
  createdAt: "2026-10-01T00:00:00Z",
  expiresAt: "2026-10-08T00:00:00Z",
  createdBy: "admin",
  projectId: null,
  projectName: null,
  acceptedBy: null,
  acceptedAt: null,
  revokedAt: null,
  ...over,
});

const members = [member(), member({ userId: "u-2", login: "bob@beta.example", role: "member" })];

const requests = (mock: ReturnType<typeof mockApi>, method: string, path: string) =>
  mock.mock.calls
    .map(([r]) => r as Request)
    .filter((r) => r.method === method && new URL(r.url).pathname === path);

const base = "/api/v1/admin/organizations/t-1";

function setup(extra: Record<string, (r: Request) => Response | Promise<Response>> = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json(adminSession),
    "GET /api/v1/admin/stats": () =>
      json({
        organizations: { total: 1, active: 1, suspended: 0, deleted: 0 },
        users: 2,
        monitors: 0,
        heartbeats: 0,
        plans: [{ key: "free", name: "Free", organizations: 1 }],
      }),
    "GET /api/v1/admin/organizations/t-1": () => json(organization),
    [`GET ${base}/users`]: () => json({ items: [], total: 0, limit: 100, offset: 0 }),
    [`GET ${base}/members`]: () => json({ items: members }),
    [`GET ${base}/invitations`]: () => json({ items: [invitation()] }),
    "GET /api/v1/auth/providers": () =>
      json({
        providers: [],
        passwordLoginEnabled: true,
        registrationEnabled: false,
        emailEnabled: false,
        passwordResetEnabled: false,
        emailVerificationEnabled: false,
      }),
    ...extra,
  });
}

describe("admin organization members", () => {
  it("lists members and changes a role without asking when no owner is involved", async () => {
    const fetchMock = setup({
      [`PUT ${base}/members/u-2`]: () => json(member({ userId: "u-2", role: "viewer" })),
    });
    renderApp("/admin/organizations/t-1");
    const select = await screen.findByLabelText("Role of bob@beta.example");
    expect(
      within(select)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Projects only", "Owner", "Maintainer", "Member", "Viewer"]);
    await userEvent.selectOptions(select, "viewer");
    await waitFor(() => expect(requests(fetchMock, "PUT", `${base}/members/u-2`)).toHaveLength(1));
    expect(await requests(fetchMock, "PUT", `${base}/members/u-2`)[0]!.json()).toEqual({
      role: "viewer",
    });
  });

  it("asks before making someone an owner or demoting an owner", async () => {
    const fetchMock = setup({
      [`PUT ${base}/members/u-2`]: () => json(member({ userId: "u-2", role: "owner" })),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.selectOptions(
      await screen.findByLabelText("Role of bob@beta.example"),
      "owner",
    );
    const dialog = await screen.findByRole("dialog", { name: /Make bob@beta.example owner/ });
    expect(requests(fetchMock, "PUT", `${base}/members/u-2`)).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole("button", { name: "Change role" }));
    await waitFor(() => expect(requests(fetchMock, "PUT", `${base}/members/u-2`)).toHaveLength(1));
  });

  it("shows the last-owner refusal and keeps the dialog's choice out of the way", async () => {
    setup({
      [`DELETE ${base}/members/u-1`]: () =>
        problem(409, "The organization needs an owner", {
          type: "urn:proreta:problem:last-owner",
          detail: "Make another member an owner first.",
        }),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.click(
      await screen.findByRole("button", { name: "Remove alice@alpha.example" }),
    );
    const dialog = await screen.findByRole("dialog", { name: /Remove alice@alpha.example/ });
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove member" }));
    expect(await screen.findByText("Make another member an owner first.")).toBeInTheDocument();
  });

  it("does not offer changes for the system admin", async () => {
    setup({
      [`GET ${base}/members`]: () =>
        json({
          items: [
            member({
              userId: "u-admin",
              login: "admin",
              isPlatformAdmin: true,
              isSystemAdmin: true,
            }),
          ],
        }),
    });
    renderApp("/admin/organizations/t-1");
    expect(await screen.findByText("System admin")).toBeInTheDocument();
    expect(screen.queryByLabelText("Role of admin")).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove admin" })).toBeNull();
  });

  it("finds a user by typing, adds them with the chosen role and hides current members", async () => {
    const fetchMock = setup({
      "GET /api/v1/admin/users": (r) => {
        const q = new URL(r.url).searchParams.get("query");
        return json({
          items:
            q === "ca"
              ? [
                  summary({ id: "u-3", login: "carol@gamma.example", displayName: "Carol" }),
                  summary({ id: "u-2", login: "bob@beta.example" }),
                  summary({
                    id: "u-admin",
                    login: "admin",
                    isPlatformAdmin: true,
                    isSystemAdmin: true,
                  }),
                ]
              : [],
          total: 3,
          limit: 25,
          offset: 0,
        });
      },
      [`PUT ${base}/members/u-3`]: () =>
        json(member({ userId: "u-3", login: "carol@gamma.example", role: "maintainer" })),
    });
    renderApp("/admin/organizations/t-1");
    const add = await screen.findByRole("button", { name: "Add member" });
    expect(add).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Find a user"), "ca");
    const list = await screen.findByRole("list", { name: "Matching users" });
    expect(within(list).getAllByRole("button")).toHaveLength(1);
    expect(within(list).getByText("carol@gamma.example")).toBeInTheDocument();
    await userEvent.click(within(list).getByRole("button"));
    expect(screen.queryByLabelText("Find a user")).toBeNull();
    await userEvent.selectOptions(
      screen.getByLabelText("Role", { selector: "select" }),
      "maintainer",
    );
    await userEvent.click(screen.getByRole("button", { name: "Add member" }));
    await waitFor(() => expect(requests(fetchMock, "PUT", `${base}/members/u-3`)).toHaveLength(1));
    expect(await requests(fetchMock, "PUT", `${base}/members/u-3`)[0]!.json()).toEqual({
      role: "maintainer",
    });
    expect(await screen.findByLabelText("Find a user")).toBeInTheDocument();
  });

  it("suggests an invitation when nothing matches", async () => {
    setup({
      "GET /api/v1/admin/users": () => json({ items: [], total: 0, limit: 25, offset: 0 }),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.type(await screen.findByLabelText("Find a user"), "zz");
    expect(await screen.findByText(/No account matches “zz”/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add member" })).toBeDisabled();
  });

  it("offers no changes for a deleted organization", async () => {
    setup({
      "GET /api/v1/admin/organizations/t-1": () =>
        json({ ...organization, deletedAt: "2026-10-02T00:00:00Z" }),
    });
    renderApp("/admin/organizations/t-1");
    await screen.findByText("bob@beta.example");
    expect(screen.queryByLabelText("Role of bob@beta.example")).toBeNull();
    expect(screen.queryByRole("button", { name: "Add member" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Invite someone" })).toBeNull();
  });
});

describe("admin organization invitations", () => {
  it("creates an invitation, shows the link once and revokes another", async () => {
    const fetchMock = setup({
      [`POST ${base}/invitations`]: () =>
        json(
          {
            invitation: invitation({ id: "i-2", label: "Dan" }),
            inviteUrl: "https://proreta.example.com/invite#SECRET-TOKEN",
            emailQueued: false,
          },
          201,
        ),
      [`POST ${base}/invitations/i-1/revoke`]: () => new Response(null, { status: 204 }),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Invite someone" }));
    const dialog = await screen.findByRole("dialog", { name: "Invite someone" });
    expect(
      within(within(dialog).getByLabelText("Role"))
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Maintainer", "Member", "Viewer"]);
    await userEvent.type(within(dialog).getByLabelText("Note (optional)"), "Dan");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create link" }));
    expect(await screen.findByLabelText("Invitation link")).toHaveValue(
      "https://proreta.example.com/invite#SECRET-TOKEN",
    );
    expect(await requests(fetchMock, "POST", `${base}/invitations`)[0]!.json()).toEqual({
      role: "member",
      label: "Dan",
    });
    await userEvent.click(screen.getByRole("button", { name: "I sent it, hide the link" }));
    expect(screen.queryByLabelText("Invitation link")).toBeNull();

    await userEvent.click(await screen.findByRole("button", { name: "Revoke" }));
    await userEvent.click(await screen.findByRole("button", { name: "Revoke invitation" }));
    await waitFor(() =>
      expect(requests(fetchMock, "POST", `${base}/invitations/i-1/revoke`)).toHaveLength(1),
    );
  });

  it("sends the address when the installation sends email and the box is checked", async () => {
    const fetchMock = setup({
      "GET /api/v1/auth/providers": () =>
        json({
          providers: [],
          passwordLoginEnabled: true,
          registrationEnabled: false,
          emailEnabled: true,
          passwordResetEnabled: false,
          emailVerificationEnabled: false,
        }),
      [`POST ${base}/invitations`]: () =>
        json(
          {
            invitation: invitation({ id: "i-3", label: "guest@example.org" }),
            inviteUrl: "https://proreta.example.com/invite#TOKEN",
            emailQueued: true,
          },
          201,
        ),
    });
    renderApp("/admin/organizations/t-1");
    await userEvent.click(await screen.findByRole("button", { name: "Invite someone" }));
    const dialog = await screen.findByRole("dialog", { name: "Invite someone" });
    await userEvent.type(
      await within(dialog).findByLabelText("Email the link to (optional)"),
      "guest@example.org",
    );
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "Send by email" }));
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Create link and send email" }),
    );
    expect(await screen.findByText(/on its way to/)).toHaveTextContent("guest@example.org");
    expect(await requests(fetchMock, "POST", `${base}/invitations`)[0]!.json()).toEqual({
      role: "member",
      email: "guest@example.org",
      sendEmail: true,
    });
  });
});
