import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminPlan } from "../api/endpoints";
import { json, mockApi, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const limits = {
  monitors: 3,
  heartbeats: 1,
  channels: 1,
  members: 3,
  projects: 50,
  statusPages: 1,
  specula: 2147483647,
  statusPageDomains: 0,
};
const plan = (over: Partial<AdminPlan> = {}): AdminPlan => ({
  key: "free",
  name: "Free",
  limits,
  system: true,
  organizations: 2,
  deletable: false,
  ...over,
});

describe("admin plans", () => {
  it("lists the plans with their limits and a way to add one", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/plans": () =>
        json({
          items: [plan(), plan({ key: "gold", name: "Gold", system: false, deletable: true })],
        }),
    });
    renderApp("/admin/plans");
    expect(await screen.findByRole("heading", { name: "Plans" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Free" })).toHaveAttribute(
      "href",
      "/admin/plans/free",
    );
    expect(screen.getByText("System")).toBeInTheDocument();
    expect(screen.getAllByText("Unlimited").length).toBeGreaterThan(0);
    expect(screen.getByRole("link", { name: "New plan" })).toHaveAttribute(
      "href",
      "/admin/plans/new",
    );
  });

  it("creates a plan with a number and an unlimited limit, and shows a taken key", async () => {
    let sent: unknown;
    let taken = true;
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/plans": () => json({ items: [] }),
      "POST /api/v1/admin/plans": async (req: Request) => {
        sent = await req.json();
        if (taken)
          return json(
            {
              type: "urn:proreta:problem:validation",
              title: "Validation failed",
              status: 422,
              fieldErrors: [{ field: "key", message: "a plan with this key already exists" }],
            },
            422,
          );
        return json(plan({ key: "gold", name: "Gold", system: false, deletable: true }), 201);
      },
    });
    renderApp("/admin/plans/new");
    await userEvent.type(await screen.findByLabelText("Key"), "gold");
    await userEvent.type(screen.getByLabelText("Name"), "Gold");
    for (const label of [
      "Monitors",
      "Heartbeats",
      "Notification channels",
      "Members",
      "Projects",
      "Status pages",
      "Status page domains",
    ])
      await userEvent.type(screen.getByLabelText(label), "5");
    await userEvent.click(screen.getByRole("checkbox", { name: "Specula: unlimited" }));
    expect(screen.getByLabelText("Specula")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByText("a plan with this key already exists")).toBeInTheDocument();
    expect(sent).toEqual({
      key: "gold",
      name: "Gold",
      limits: {
        monitors: 5,
        heartbeats: 5,
        channels: 5,
        members: 5,
        projects: 5,
        statusPages: 5,
        specula: 2147483647,
        statusPageDomains: 5,
      },
    });
    taken = false;
    await userEvent.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByText("Plan Gold created.")).toBeInTheDocument();
  });

  it("asks for every limit before it sends anything", async () => {
    const post = vi.fn();
    mockApi({ ...asAdmin, "POST /api/v1/admin/plans": post });
    renderApp("/admin/plans/new");
    await userEvent.click(await screen.findByRole("button", { name: "Create plan" }));
    expect(await screen.findByText("Enter a name.")).toBeInTheDocument();
    expect(screen.getAllByText(/Enter a whole number from 0 up/).length).toBe(8);
    expect(post).not.toHaveBeenCalled();
  });

  it("edits a plan with a fixed key and confirms a limit below current use", async () => {
    let sent: unknown;
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/plans": () => json({ items: [plan()] }),
      "GET /api/v1/admin/plans/free": () =>
        json(plan({ peakUsage: { ...limits, monitors: 4, specula: 0 } })),
      "PUT /api/v1/admin/plans/free": async (req: Request) => {
        sent = await req.json();
        return json(plan({ name: "Starter" }));
      },
    });
    renderApp("/admin/plans/free");
    expect(await screen.findByRole("heading", { name: "Free" })).toBeInTheDocument();
    expect(
      screen.getByText("2 organizations are on this plan.", { exact: false }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Key" })).toBeNull();
    expect(screen.getByText("Never changes")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete plan" })).toBeNull();
    const name = screen.getByLabelText("Name");
    await userEvent.clear(name);
    await userEvent.type(name, "Starter");
    const monitors = screen.getByLabelText("Monitors");
    await userEvent.clear(monitors);
    await userEvent.type(monitors, "2");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/monitors \(4 in use\)/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Save anyway" }));
    await waitFor(() => expect(sent).toMatchObject({ name: "Starter", limits: { monitors: 2 } }));
  });

  it("deletes a custom plan only after a confirmation", async () => {
    const del = vi.fn(() => new Response(null, { status: 204 }));
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/plans": () => json({ items: [] }),
      "GET /api/v1/admin/plans/gold": () =>
        json(
          plan({
            key: "gold",
            name: "Gold",
            system: false,
            deletable: true,
            organizations: 0,
            peakUsage: { ...limits, specula: 0 },
          }),
        ),
      "DELETE /api/v1/admin/plans/gold": del,
    });
    renderApp("/admin/plans/gold");
    await userEvent.click(await screen.findByRole("button", { name: "Delete plan" }));
    expect(del).not.toHaveBeenCalled();
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete plan" }),
    );
    expect(await screen.findByText("Plan Gold deleted.")).toBeInTheDocument();
    expect(del).toHaveBeenCalled();
  });
});
