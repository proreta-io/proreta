import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { json, mockApi, renderApp, session, signedIn } from "../test/utils";

const adminSession = { ...session, user: { ...session.user, isPlatformAdmin: true } };
const asAdmin = { "GET /api/v1/auth/me": () => json(adminSession) };
const stats = {
  organizations: { total: 1, active: 1, suspended: 0, deleted: 0 },
  users: 1,
  monitors: 0,
  heartbeats: 0,
  plans: [],
};

describe("the Admin Area shell", () => {
  it("has its own navigation, a way back and none of the application chrome", async () => {
    mockApi({ ...asAdmin, "GET /api/v1/admin/stats": () => json(stats) });
    renderApp("/admin");
    expect(await screen.findByRole("heading", { name: "Admin" })).toBeInTheDocument();
    const banner = screen.getByRole("banner");
    expect(within(banner).getByText("Platform admin")).toBeInTheDocument();
    expect(within(banner).getByRole("link", { name: "Back to app" })).toHaveAttribute("href", "/");

    const nav = screen.getByRole("navigation", { name: "Admin" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((a) => [a.textContent, a.getAttribute("href")]),
    ).toEqual([
      ["Overview", "/admin"],
      ["Organizations", "/admin/organizations"],
      ["Users", "/admin/users"],
      ["Domains", "/admin/domains"],
      ["Plans", "/admin/plans"],
      ["Email", "/admin/email"],
      ["Registration", "/admin/registration"],
      ["Retention", "/admin/retention"],
    ]);

    // Nothing of the application: no organization or project switcher, no main navigation.
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
    expect(screen.queryByRole("button", { name: /organization/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /project/i })).toBeNull();
    expect(screen.queryByRole("link", { name: "Monitors" })).toBeNull();
  });

  it("returns to the application shell with Back to app", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/stats": () => json(stats),
      "GET /api/v1/monitors": () =>
        json({ summary: { up: 0, down: 0, paused: 0, unknown: 0 }, items: [] }),
    });
    renderApp("/admin");
    await userEvent.click(await screen.findByRole("link", { name: "Back to app" }));
    expect(await screen.findByRole("heading", { name: "Monitors" })).toBeInTheDocument();
    const banner = screen.getByRole("banner");
    expect(within(banner).getByRole("link", { name: "Admin" })).toHaveAttribute("href", "/admin");
    expect(within(banner).queryByRole("link", { name: "Back to app" })).toBeNull();
  });

  it("keeps the shell on the nested pages", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/users": () => json({ items: [], total: 0, limit: 25, offset: 0 }),
    });
    renderApp("/admin/users");
    expect(await screen.findByRole("link", { name: "Back to app" })).toBeInTheDocument();
    expect(
      within(screen.getByRole("navigation", { name: "Admin" })).getByRole("link", {
        name: "Users",
      }),
    ).toHaveAttribute("aria-current", "page");
  });
});

describe("the Admin entry of the application shell", () => {
  const monitors = {
    "GET /api/v1/monitors": () =>
      json({ summary: { up: 0, down: 0, paused: 0, unknown: 0 }, items: [] }),
  };

  it("is shown to a platform admin", async () => {
    mockApi({ ...asAdmin, ...monitors });
    renderApp("/");
    expect(await screen.findByRole("heading", { name: "Monitors" })).toBeInTheDocument();
    expect(within(screen.getByRole("banner")).getByRole("link", { name: "Admin" })).toHaveAttribute(
      "href",
      "/admin",
    );
  });

  it("is not shown to anybody else", async () => {
    mockApi({ ...signedIn, ...monitors });
    renderApp("/");
    expect(await screen.findByRole("heading", { name: "Monitors" })).toBeInTheDocument();
    expect(within(screen.getByRole("banner")).queryByRole("link", { name: "Admin" })).toBeNull();
  });
});
