import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AdminStatusPageDomain } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const asAdmin = {
  "GET /api/v1/auth/me": () =>
    json({ ...session, user: { ...session.user, login: "admin", isPlatformAdmin: true } }),
};

const domain = (over: Partial<AdminStatusPageDomain> = {}): AdminStatusPageDomain => ({
  id: "d-1",
  host: "status.acme.example",
  hostUnicode: "status.acme.example",
  state: "active",
  primary: true,
  disabled: false,
  disabledAt: null,
  organizationId: "t-1",
  organizationSlug: "alpha",
  statusPageId: "sp-1",
  statusPageSlug: "main",
  verifiedAt: "2026-11-01T10:00:00Z",
  lastCheckedAt: "2026-11-14T10:00:00Z",
  lastError: null,
  createdAt: "2026-11-01T09:00:00Z",
  ...over,
});

const list = (items: AdminStatusPageDomain[], total = items.length) =>
  json({ items, total, limit: 25, offset: 0 });

const posts = (mock: ReturnType<typeof mockApi>) =>
  mock.mock.calls.map(([r]) => r as Request).filter((r) => r.method === "POST");

describe("admin domains", () => {
  it("is only for the platform admin", async () => {
    mockApi({ "GET /api/v1/auth/me": () => json(session) });
    renderApp("/admin/domains");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
  });

  it("is in the navigation and lists domains with their organization", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () =>
        list([
          domain(),
          domain({ id: "d-2", host: "x.example", hostUnicode: "x.example", state: "failed" }),
          domain({
            id: "d-3",
            state: "verified",
            primary: false,
            disabled: true,
            disabledAt: "2026-11-10T08:00:00Z",
            host: "xn--bcher-kva.example",
            hostUnicode: "bücher.example",
          }),
        ]),
    });
    renderApp("/admin/domains");
    expect(await screen.findByText("status.acme.example")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Domains" })).toHaveAttribute("href", "/admin/domains");
    const rows = within(screen.getByRole("table")).getAllByRole("row");
    expect(within(rows[1]!).getByRole("link", { name: "alpha" })).toHaveAttribute(
      "href",
      "/admin/organizations/t-1",
    );
    expect(within(rows[1]!).getByText("Active")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Primary")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Failed")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("Disabled")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("xn--bcher-kva.example")).toBeInTheDocument();
    expect(within(rows[3]!).getByRole("button", { name: /Enable/ })).toBeInTheDocument();
  });

  it("searches, filters by state and paginates", async () => {
    const seen: URL[] = [];
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": (req: Request) => {
        const url = new URL(req.url);
        seen.push(url);
        const off = Number(url.searchParams.get("offset") ?? "0");
        return list(
          [
            domain(
              off > 0 ? { id: "d-9", host: "page2.example", hostUnicode: "page2.example" } : {},
            ),
          ],
          40,
        );
      },
    });
    renderApp("/admin/domains");
    expect(await screen.findByText("1–1 of 40")).toBeInTheDocument();

    await userEvent.type(screen.getByRole("searchbox", { name: "Search domains" }), "acme");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(seen.at(-1)!.searchParams.get("query")).toBe("acme"));

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "State" }), "failed");
    await waitFor(() => expect(seen.at(-1)!.searchParams.get("state")).toBe("failed"));

    await userEvent.click(screen.getByRole("button", { name: /next/i }));
    expect(await screen.findByText("page2.example")).toBeInTheDocument();
    expect(seen.at(-1)!.searchParams.get("offset")).toBe("25");
  });

  it("shows empty, filtered-empty and error states", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () => list([]),
    });
    const first = renderApp("/admin/domains");
    expect(await screen.findByText("No custom domains yet.")).toBeInTheDocument();
    first.unmount();
    renderApp("/admin/domains?state=failed");
    expect(await screen.findByText("No domains match the filter.")).toBeInTheDocument();
  });

  it("shows an error with a retry", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () => problem(500, "Boom"),
    });
    renderApp("/admin/domains");
    expect(await screen.findByText("Cannot load domains")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument();
  });

  it("says the feature is off on this installation", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () =>
        problem(404, "Custom domains are turned off", {
          type: "urn:proreta:problem:custom-domains-disabled",
        }),
    });
    renderApp("/admin/domains");
    expect(await screen.findByText(/turned off on this installation/)).toBeInTheDocument();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
  });

  it("disables a host with a required reason", async () => {
    let current = domain();
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () => list([current]),
      "POST /api/v1/admin/status-page-domains/d-1/disable": async (req: Request) => {
        expect(await req.json()).toEqual({ reason: "phishing report" });
        current = domain({ disabled: true, disabledAt: "2026-11-14T11:00:00Z" });
        return json(current);
      },
    });
    renderApp("/admin/domains");
    await userEvent.click(
      await screen.findByRole("button", { name: /Disable status.acme.example/ }),
    );
    const dialog = await screen.findByRole("dialog");
    const reason = within(dialog).getByRole("textbox", { name: "Reason" });
    expect(reason).toHaveFocus();
    expect(within(dialog).getByText(/answers 404/)).toBeInTheDocument();

    await userEvent.click(within(dialog).getByRole("button", { name: "Disable domain" }));
    expect(await within(dialog).findByText("Give a reason.")).toBeInTheDocument();
    expect(posts(mock)).toHaveLength(0);

    await userEvent.type(reason, "  phishing report ");
    expect(within(dialog).getByText(/18 \/ 500/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Disable domain" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(
      await screen.findByRole("button", { name: /Enable status.acme.example/ }),
    ).toBeInTheDocument();
    expect(screen.getByText("Disabled")).toBeInTheDocument();
  });

  it("enables a disabled host", async () => {
    let current = domain({ disabled: true, disabledAt: "2026-11-14T11:00:00Z" });
    const mock = mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () => list([current]),
      "POST /api/v1/admin/status-page-domains/d-1/enable": () => {
        current = domain();
        return json(current);
      },
    });
    renderApp("/admin/domains");
    await userEvent.click(
      await screen.findByRole("button", { name: /Enable status.acme.example/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Reason" }), "false alarm");
    await userEvent.click(within(dialog).getByRole("button", { name: "Enable domain" }));
    await waitFor(() => expect(posts(mock)).toHaveLength(1));
    expect(
      await screen.findByRole("button", { name: /Disable status.acme.example/ }),
    ).toBeInTheDocument();
  });

  it("explains a deleted organization calmly and keeps the dialog open", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () => list([domain()]),
      "POST /api/v1/admin/status-page-domains/d-1/disable": () =>
        problem(409, "The organization is deleted", {
          type: "urn:proreta:problem:organization-deleted",
        }),
    });
    renderApp("/admin/domains");
    await userEvent.click(
      await screen.findByRole("button", { name: /Disable status.acme.example/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Reason" }), "abuse");
    await userEvent.click(within(dialog).getByRole("button", { name: "Disable domain" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "The organization is deleted, so its domains cannot be changed any more.",
    );
  });

  it("cancels with Escape", async () => {
    mockApi({
      ...asAdmin,
      "GET /api/v1/admin/status-page-domains": () => list([domain()]),
    });
    renderApp("/admin/domains");
    await userEvent.click(
      await screen.findByRole("button", { name: /Disable status.acme.example/ }),
    );
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
