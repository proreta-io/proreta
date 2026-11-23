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
  capabilities: ["schedule/1"],
  scheduleIssuedAt: null,
  bufferDepth: null,
  oldestUnsentAt: null,
  bufferDegraded: false,
  clockOffsetMs: null,
  duplicateInstance: false,
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
  it("asks before another Specula's edit form drops unsaved changes", async () => {
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () =>
        json({ items: [specula(), specula({ id: "s2", name: "Lab", hostname: "lab-1" })] }),
    });
    renderApp("/specula");
    await userEvent.click(await screen.findByRole("button", { name: "Prod cluster" }));
    await userEvent.type(screen.getByLabelText("Name"), " EU");
    await userEvent.click(screen.getByRole("button", { name: "Lab" }));
    const dialog = screen.getByRole("dialog", { name: "Discard the changes to Prod cluster?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.getByLabelText("Name")).toHaveValue("Prod cluster EU");

    await userEvent.click(screen.getByRole("button", { name: "Lab" }));
    await userEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(screen.getByLabelText("Name")).toHaveValue("Lab");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    // The same for a new Specula with something typed.
    await userEvent.click(screen.getByRole("button", { name: "Register a Specula" }));
    await userEvent.type(screen.getByLabelText("Name"), "Edge");
    await userEvent.click(screen.getByRole("button", { name: "Lab" }));
    expect(screen.getByRole("dialog", { name: "Discard the new Specula?" })).toBeInTheDocument();
  });

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

  it("shows the schedule, the replay, a degraded buffer and the unreachable wording", async () => {
    const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString();
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () =>
        json({
          items: [
            specula({
              scheduleIssuedAt: ago(300),
              bufferDepth: 25,
              oldestUnsentAt: ago(3600),
              bufferDegraded: true,
            }),
            specula({ id: "s2", name: "Lab", status: "offline", lastSeenAt: ago(600) }),
          ],
        }),
    });
    renderApp("/specula");
    const rows = within(await screen.findByRole("table")).getAllByRole("row");
    expect(within(rows[1]!).getByText("Schedule issued 5 min ago")).toBeInTheDocument();
    expect(
      within(rows[1]!).getByText(/Replaying 25 results, oldest checked 1 h ago/),
    ).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Buffer degraded")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Unreachable")).toBeInTheDocument();
    expect(within(rows[2]!).queryByText("Buffer degraded")).not.toBeInTheDocument();
  });

  it("warns about a skewed clock and two executors, and stays quiet within 30 s", async () => {
    mockApi({
      ...signedIn,
      "GET /api/v1/projects": () => json(projects),
      "GET /api/v1/specula": () =>
        json({
          items: [
            specula({ clockOffsetMs: -95_000, duplicateInstance: true }),
            specula({ id: "s2", name: "Lab", clockOffsetMs: 20_000 }),
          ],
        }),
    });
    renderApp("/specula");
    const rows = within(await screen.findByRole("table")).getAllByRole("row");
    expect(within(rows[1]!).getByText("Clock 1 min ahead of Proreta's")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Clock skew")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Two executors")).toBeInTheDocument();
    expect(within(rows[2]!).queryByText("Clock skew")).not.toBeInTheDocument();
    expect(within(rows[2]!).queryByText("Two executors")).not.toBeInTheDocument();
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
    // The name opens the edit form, like the Edit button.
    await userEvent.click(await screen.findByRole("button", { name: "Prod cluster" }));
    expect(screen.getByRole("form", { name: "Edit the Specula" })).toBeInTheDocument();
    const name = screen.getByLabelText("Name");
    // Focus brings the form above the table into view.
    expect(name).toHaveFocus();
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
    expect(within(await screen.findByRole("table")).getByText("Prod cluster")).toBeInTheDocument();
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

  describe("missed and lost checks", () => {
    const losses = (over: Record<string, unknown> = {}) => ({
      range: "24h",
      reporting: true,
      totals: [
        { kind: "missed", reason: "busy", count: 1200 },
        { kind: "dropped", reason: "tooOld", count: 3 },
      ],
      buckets: [],
      ...over,
    });

    it("shows the totals by reason with what to do, and reloads for another range", async () => {
      const ranges: (string | null)[] = [];
      mockApi({
        ...signedIn,
        "GET /api/v1/projects": () => json(projects),
        "GET /api/v1/specula": () => json({ items: [specula()] }),
        "GET /api/v1/specula/s1/slot-losses": (req) => {
          const range = new URL(req.url).searchParams.get("range");
          ranges.push(range);
          return json(losses({ range }));
        },
      });
      renderApp("/specula");
      const block = await screen.findByRole("group", { name: "Missed checks of Prod cluster" });
      expect(await within(block).findByText(/Still busy/)).toBeInTheDocument();
      expect(within(block).getByText("1,200")).toBeInTheDocument();
      expect(within(block).getByText(/raise its concurrency/i)).toBeInTheDocument();
      expect(within(block).getByText(/Too old/)).toBeInTheDocument();
      expect(within(block).getByText(/checked, then dropped/)).toBeInTheDocument();
      await userEvent.click(screen.getByRole("radio", { name: "7 days" }));
      await waitFor(() => expect(ranges).toEqual(["24h", "7d"]));
    });

    it("says so when the Specula does not report, and when nothing was lost", async () => {
      mockApi({
        ...signedIn,
        "GET /api/v1/projects": () => json(projects),
        "GET /api/v1/specula": () =>
          json({ items: [specula(), specula({ id: "s2", name: "Lab" })] }),
        "GET /api/v1/specula/s1/slot-losses": () => json(losses({ reporting: false, totals: [] })),
        "GET /api/v1/specula/s2/slot-losses": () => json(losses({ totals: [] })),
      });
      renderApp("/specula");
      expect(
        await screen.findByText("This Specula does not report missed checks. Upgrade it."),
      ).toBeInTheDocument();
      expect(
        await screen.findByText("No missed or lost checks in this range."),
      ).toBeInTheDocument();
    });

    it("keeps the rest of the page when one Specula's numbers cannot be loaded", async () => {
      mockApi({
        ...signedIn,
        "GET /api/v1/projects": () => json(projects),
        "GET /api/v1/specula": () => json({ items: [specula()] }),
        "GET /api/v1/specula/s1/slot-losses": () => problem(500, "boom"),
      });
      renderApp("/specula");
      expect(await screen.findByText("Cannot load missed checks")).toBeInTheDocument();
      expect(screen.getByRole("table")).toBeInTheDocument();
    });
  });
});
