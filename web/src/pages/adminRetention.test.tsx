import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminRetentionSettings } from "../api/endpoints";
import { json, mockApi, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const settings = (over: Partial<AdminRetentionSettings> = {}): AdminRetentionSettings => ({
  deliveryDays: 30,
  rollupMonths: 13,
  deliveryDefault: true,
  rollupDefault: true,
  updatedAt: null,
  ...over,
});

describe("admin retention page", () => {
  it("is only for the platform admin", async () => {
    mockApi({ "GET /api/v1/auth/me": () => json(session) });
    renderApp("/admin/retention");
    expect(await screen.findByText(/not found|Page not found/i)).toBeInTheDocument();
  });

  it("shows the effective values, that they are defaults, and what is kept", async () => {
    mockApi({ ...asAdmin, "GET /api/v1/admin/retention": () => json(settings()) });
    renderApp("/admin/retention");
    expect(await screen.findByRole("heading", { name: "Retention" })).toBeInTheDocument();
    expect(await screen.findByText("30 days")).toBeInTheDocument();
    expect(screen.getByText("13 months")).toBeInTheDocument();
    expect(screen.getAllByText(/the default/).length).toBeGreaterThan(0);
    expect(screen.getByText(/audit log\s+and incidents are kept/)).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: /finished deliveries/ })).toHaveValue(null);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("sends the chosen values and null for an empty field", async () => {
    let current = settings();
    let sent: unknown;
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/retention": () => json(current),
      "PUT /api/v1/admin/retention": async (req: Request) => {
        const body = (await req.json()) as {
          deliveryDays: number | null;
          rollupMonths: number | null;
        };
        sent = body;
        current = settings({
          deliveryDays: body.deliveryDays ?? 30,
          deliveryDefault: body.deliveryDays === null,
          updatedAt: "2026-11-13T09:00:00Z",
        });
        return json(current);
      },
    });
    renderApp("/admin/retention");
    const days = await screen.findByRole("spinbutton", { name: /finished deliveries/ });
    await userEvent.type(days, "14");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByText("14 days")).toBeInTheDocument());
    expect(sent).toEqual({ deliveryDays: 14, rollupMonths: null });
    expect(screen.getByText(/set here/)).toBeInTheDocument();
  });

  it("shows the field error of a refused value", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/retention": () => json(settings()),
      "PUT /api/v1/admin/retention": () =>
        json(
          {
            type: "urn:proreta:problem:validation",
            title: "Validation failed",
            status: 422,
            fieldErrors: [{ field: "deliveryDays", message: "must be between 7 and 365 days" }],
          },
          422,
        ),
    });
    renderApp("/admin/retention");
    await userEvent.type(
      await screen.findByRole("spinbutton", { name: /finished deliveries/ }),
      "3",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("must be between 7 and 365 days")).toBeInTheDocument();
  });
});
