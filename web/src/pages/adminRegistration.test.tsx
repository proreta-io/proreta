import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminRegistrationSettings } from "../api/endpoints";
import { json, mockApi, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const settings = (over: Partial<AdminRegistrationSettings> = {}): AdminRegistrationSettings => ({
  effective: false,
  source: "environment",
  environmentValue: false,
  updatedAt: null,
  ...over,
});

describe("admin registration page", () => {
  it("is only for the platform admin", async () => {
    mockApi({ "GET /api/v1/auth/me": () => json(session) });
    renderApp("/admin/registration");
    expect(await screen.findByText(/not found|Page not found/i)).toBeInTheDocument();
  });

  it("shows the effective value and where it comes from", async () => {
    mockApi({ ...asAdmin, "GET /api/v1/admin/registration": () => json(settings()) });
    renderApp("/admin/registration");
    expect(await screen.findByRole("heading", { name: "Registration" })).toBeInTheDocument();
    expect(
      await screen.findByText(/following the installation's configuration/),
    ).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Registration" })).toHaveValue("environment");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("opens registration and sends the choice", async () => {
    let current = settings();
    let sent: unknown;
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/registration": () => json(current),
      "PUT /api/v1/admin/registration": async (req: Request) => {
        const body = (await req.json()) as { enabled: boolean | null };
        sent = body;
        current = settings({
          effective: body.enabled === true,
          source: "database",
          updatedAt: "2026-11-17T09:00:00Z",
        });
        return json(current);
      },
    });
    renderApp("/admin/registration");
    const select = await screen.findByRole("combobox", { name: "Registration" });
    await userEvent.selectOptions(select, "open");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText(/set here/)).toBeInTheDocument());
    expect(sent).toEqual({ enabled: true });
    expect(screen.getByText("open", { selector: "strong" })).toBeInTheDocument();
  });

  it("returns to the installation setting with null", async () => {
    let current = settings({
      effective: true,
      source: "database",
      updatedAt: "2026-11-17T09:00:00Z",
    });
    let sent: unknown;
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/registration": () => json(current),
      "PUT /api/v1/admin/registration": async (req: Request) => {
        sent = await req.json();
        current = settings();
        return json(current);
      },
    });
    renderApp("/admin/registration");
    const select = await screen.findByRole("combobox", { name: "Registration" });
    expect(select).toHaveValue("open");
    await userEvent.selectOptions(select, "environment");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText(/following the installation/)).toBeInTheDocument());
    expect(sent).toEqual({ enabled: null });
  });
});
