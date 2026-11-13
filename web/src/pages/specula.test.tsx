import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Specula } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session, signedIn } from "../test/utils";

const specula = (over: Partial<Specula> = {}): Specula => ({
  id: "s1",
  name: "Prod cluster",
  allProjects: true,
  projectIds: [],
  status: "online",
  version: "0.1.0",
  hostname: "pod-1",
  lastSeenAt: new Date().toISOString(),
  createdAt: "2026-11-01T00:00:00Z",
  revokedAt: null,
  ...over,
});

const projects = {
  items: [
    {
      id: "p1",
      name: "Default",
      slug: "default",
      isDefault: true,
      role: "owner",
      monitorCount: 0,
      channelCount: 0,
    },
    {
      id: "p2",
      name: "Payments",
      slug: "payments",
      isDefault: false,
      role: "owner",
      monitorCount: 0,
      channelCount: 0,
    },
  ],
};

const asRole = (role: string) => ({ "GET /api/v1/auth/me": () => json({ ...session, role }) });

describe("Specula page", () => {
  it("lists the executors with status, version and who can use them", async () => {
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () =>
        json({
          items: [
            specula(),
            specula({
              id: "s2",
              name: "Lab",
              status: "pending",
              version: "",
              hostname: "",
              lastSeenAt: null,
              allProjects: false,
              projectIds: ["p2"],
            }),
          ],
        }),
    });
    renderApp("/specula");
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]!).getByText("Prod cluster")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Online")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("All projects")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Waiting")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Payments")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("never")).toBeInTheDocument();
  });

  it("registers a Specula and shows the token once", async () => {
    let body: unknown;
    let listed = 0;
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () => {
        listed++;
        return json({ items: [] });
      },
      "POST /api/v1/specula": async (req) => {
        body = await req.json();
        return json(
          { specula: specula({ status: "pending", lastSeenAt: null }), token: "spc_secret-token" },
          201,
        );
      },
    });
    renderApp("/specula");
    expect(await screen.findByText(/No Specula yet/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Register a Specula" }));
    // Name is required; nothing is sent.
    await userEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByText("Enter a name.")).toBeInTheDocument();
    expect(body).toBeUndefined();
    await userEvent.type(screen.getByLabelText("Name"), "Production cluster");
    await userEvent.click(screen.getByRole("button", { name: "Register" }));

    const panel = await screen.findByRole("region", { name: "Prod cluster is registered" });
    expect(body).toEqual({ name: "Production cluster", allProjects: true });
    expect(within(panel).getByLabelText("Token")).toHaveValue("spc_secret-token");
    expect(within(panel).getByLabelText("Settings of the executor")).toHaveTextContent(
      "PRORETA_SPECULA_TOKEN=spc_secret-token",
    );
    await waitFor(() => expect(listed).toBeGreaterThan(1));
    await userEvent.click(within(panel).getByRole("button", { name: "I saved the token" }));
    expect(screen.queryByText("spc_secret-token")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Token")).not.toBeInTheDocument();
  });

  it("shares with chosen projects and needs at least one", async () => {
    let body: unknown;
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () => json({ items: [] }),
      "POST /api/v1/specula": async (req) => {
        body = await req.json();
        return json({ specula: specula(), token: "spc_x" }, 201);
      },
    });
    renderApp("/specula");
    await userEvent.click(await screen.findByRole("button", { name: "Register a Specula" }));
    await userEvent.type(screen.getByLabelText("Name"), "Lab");
    await userEvent.click(screen.getByLabelText("Chosen projects"));
    await userEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByText(/at least one project/)).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Payments"));
    await userEvent.click(screen.getByRole("button", { name: "Register" }));
    await waitFor(() => expect(body).toBeDefined());
    expect(body).toEqual({ name: "Lab", allProjects: false, projectIds: ["p2"] });
  });

  it("shows a server error about the name next to the field", async () => {
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () => json({ items: [] }),
      "POST /api/v1/specula": () =>
        problem(422, "Validation failed", {
          fieldErrors: [{ field: "name", message: "is already used by another Specula" }],
        }),
    });
    renderApp("/specula");
    await userEvent.click(await screen.findByRole("button", { name: "Register a Specula" }));
    await userEvent.type(screen.getByLabelText("Name"), "Prod");
    await userEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByText("Is already used by another Specula")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");
  });

  it("edits the name and sharing, and revokes after confirming", async () => {
    let patch: unknown;
    let revoked = false;
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () => json({ items: revoked ? [] : [specula()] }),
      "PATCH /api/v1/specula/s1": async (req) => {
        patch = await req.json();
        return json(specula({ name: "Prod" }));
      },
      "DELETE /api/v1/specula/s1": () => {
        revoked = true;
        return new Response(null, { status: 204 });
      },
    });
    renderApp("/specula");
    await userEvent.click(await screen.findByRole("button", { name: "Edit Prod cluster" }));
    const name = screen.getByLabelText("Name");
    await userEvent.clear(name);
    await userEvent.type(name, "Prod");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(patch).toEqual({ name: "Prod", allProjects: true }));

    await userEvent.click(await screen.findByRole("button", { name: "Revoke Prod cluster" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("cannot be undone");
    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));
    expect(await screen.findByText(/No Specula yet/)).toBeInTheDocument();
    expect(revoked).toBe(true);
  });

  it("is read-only for a member: no registering, editing or revoking", async () => {
    mockApi({
      ...asRole("member"),
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () => json({ items: [specula()] }),
    });
    renderApp("/specula");
    expect(await screen.findByText("Prod cluster")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Register a Specula" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit Prod cluster" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Revoke Prod cluster" })).not.toBeInTheDocument();
  });
  it("disables registering and says why when the plan limit is reached", async () => {
    const usage = (used: number) => ({
      plan: { key: "free", name: "Free" },
      limits: {
        monitors: 3,
        heartbeats: 1,
        channels: 1,
        members: 3,
        projects: 50,
        statusPages: 1,
        specula: 1,
        statusPageDomains: 0,
      },
      usage: {
        monitors: 0,
        heartbeats: 0,
        channels: 0,
        members: 1,
        projects: 1,
        statusPages: 0,
        specula: used,
        statusPageDomains: 0,
      },
    });
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () => json({ items: [specula()] }),
      "GET /api/v1/usage": () => json(usage(1)),
    });
    renderApp("/specula");
    expect(
      await screen.findByText(/Your plan allows 1 Specula, and the limit is reached/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Register a Specula" })).toBeDisabled();
  });
});
