import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { StatusPage, StatusPageDomain } from "../api/endpoints";
import { json, mockApi, problem, renderApp, session } from "../test/utils";

const page: StatusPage = {
  id: "sp",
  projectId: null,
  slug: "acme",
  title: "Acme status",
  description: "",
  logoUrl: null,
  accentColor: null,
  published: true,
  passwordProtected: false,
  publicUrl: "https://proreta.example.com/status/acme",
  componentCount: 0,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
};

const domain = (host: string, state: StatusPageDomain["state"], over = {}): StatusPageDomain => ({
  id: `d-${host}`,
  host,
  hostUnicode: host,
  state,
  primary: false,
  disabled: false,
  verification: {
    txtName: `_proreta-status.${host}`,
    txtValue: "proreta-verify=0123456789abcdef0123456789abcdef",
    cnameTarget: "customers.proreta.example.net",
  },
  verifiedAt: null,
  lastCheckedAt: "2026-11-14T10:00:00Z",
  lastError: null,
  expiresAt: state === "pending" ? "2026-11-20T10:00:00Z" : null,
  createdAt: "2026-11-13T10:00:00Z",
  ...over,
});

const usage = (limit: number, used = 0) => ({
  plan: { key: "team", name: "Team" },
  limits: { statusPageDomains: limit },
  usage: { statusPageDomains: used },
});

const base = "/api/v1/status-pages/sp";

function setup(
  items: StatusPageDomain[],
  { role = "owner", limit = 3, extra = {} }: { role?: string; limit?: number; extra?: object } = {},
) {
  mockApi({
    "GET /api/v1/auth/me": () => json({ ...session, role }),
    "GET /api/v1/status-pages": () => json({ items: [page] }),
    "GET /api/v1/usage": () => json(usage(limit, items.length)),
    "GET /api/v1/status-pages/sp/preview": () =>
      json({
        version: 1,
        page: {
          slug: "acme",
          title: "Acme status",
          description: "",
          logoUrl: null,
          accentColor: null,
        },
        status: "operational",
        generatedAt: "2026-11-14T10:00:00Z",
        components: [],
        activeIncidents: [],
        pastIncidents: [],
      }),
    "GET /api/v1/status-pages/sp/components": () => json({ items: [] }),
    "GET /api/v1/monitors": () => json({ items: [] }),
    [`GET ${base}/domains`]: () => json({ items }),
    ...extra,
  });
  renderApp("/status-page");
}

const section = async () =>
  within((await screen.findByRole("heading", { name: "Custom domain" })).closest("section")!);

const stubClipboard = (writeText: (text: string) => Promise<void>) =>
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

describe("custom domain section: hidden states", () => {
  it("is left out when the installation has custom domains off", async () => {
    setup([], {
      extra: {
        [`GET ${base}/domains`]: () =>
          problem(404, "Custom domains are not turned on", {
            type: "urn:proreta:problem:custom-domains-disabled",
          }),
      },
    });
    await screen.findByRole("heading", { name: "Embed and feeds" });
    await waitFor(() =>
      expect(screen.queryByRole("heading", { name: "Custom domain" })).not.toBeInTheDocument(),
    );
  });

  it("says the plan does not include domains instead of showing the form", async () => {
    setup([], { limit: 0 });
    const s = await section();
    expect(await s.findByText("Your plan does not include custom domains.")).toBeInTheDocument();
    expect(s.queryByRole("form", { name: "Add a domain" })).not.toBeInTheDocument();
    expect(s.queryByLabelText("Domain")).not.toBeInTheDocument();
  });

  it("shows a viewer the list without actions or form", async () => {
    setup([domain("status.acme.com", "active", { primary: true })], { role: "viewer" });
    const s = await section();
    expect(await s.findByText("status.acme.com")).toBeInTheDocument();
    expect(s.queryByRole("button")).not.toBeInTheDocument();
    expect(s.queryByLabelText("Domain")).not.toBeInTheDocument();
  });
});

describe("custom domain section: states", () => {
  it("explains each state with its next step and the records still needed", async () => {
    setup([
      domain("a.acme.com", "active", { primary: true }),
      domain("p.acme.com", "pending", { lastError: "txt-missing" }),
      domain("v.acme.com", "verified", { lastError: "cname-missing" }),
      domain("f.acme.com", "failed", { lastError: "txt-missing" }),
    ]);
    await section();
    const row = (host: string) => within(screen.getByRole("listitem", { name: host }));

    const active = row("a.acme.com");
    expect(active.getByText("Active")).toBeInTheDocument();
    expect(active.getByText("Primary")).toBeInTheDocument();
    expect(active.getByRole("link", { name: "https://a.acme.com/" })).toHaveAttribute(
      "href",
      "https://a.acme.com/",
    );
    expect(active.queryByRole("group")).not.toBeInTheDocument();
    expect(active.getByText(/if both records are missing for 7 days/)).toBeInTheDocument();

    const pending = row("p.acme.com");
    expect(pending.getByText("Pending")).toBeInTheDocument();
    expect(pending.getByText(/removed if the record is not found by Nov 20/)).toBeInTheDocument();
    expect(pending.getByText(/The TXT record was not found/)).toBeInTheDocument();
    expect(pending.getByLabelText("TXT name")).toHaveValue("_proreta-status.p.acme.com");
    expect(pending.getByLabelText("CNAME name")).toHaveValue("p.acme.com");
    expect(pending.getByLabelText("CNAME value")).toHaveValue("customers.proreta.example.net");
    expect(pending.queryByRole("link")).not.toBeInTheDocument();

    const verified = row("v.acme.com");
    expect(verified.getByText("Verified")).toBeInTheDocument();
    expect(verified.getByText(/Add the CNAME record/)).toBeInTheDocument();
    expect(verified.queryByLabelText("TXT value")).not.toBeInTheDocument();
    expect(verified.getByLabelText("CNAME value")).toBeInTheDocument();
    expect(verified.getByRole("button", { name: "Make v.acme.com primary" })).toBeInTheDocument();

    const failed = row("f.acme.com");
    expect(failed.getByText("Failed")).toBeInTheDocument();
    expect(failed.getByText(/neither record was found for 7 days/)).toBeInTheDocument();
    expect(failed.getByLabelText("TXT value")).toBeInTheDocument();
    expect(failed.queryByRole("button", { name: /Make .* primary/ })).not.toBeInTheDocument();
  });

  it("copies a record value with a named button", async () => {
    setup([domain("p.acme.com", "pending")]);
    await section();
    const writeText = vi.fn(() => Promise.resolve());
    stubClipboard(writeText);
    await userEvent.click(await screen.findByRole("button", { name: "Copy TXT value" }));
    expect(writeText).toHaveBeenCalledWith("proreta-verify=0123456789abcdef0123456789abcdef");
    expect(screen.getByRole("button", { name: "Copy CNAME name" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy CNAME value" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy TXT name" })).toBeInTheDocument();
  });

  it("says when the CNAME target is not configured", async () => {
    setup([
      domain("v.acme.com", "verified", {
        verification: { txtName: "_x.v.acme.com", txtValue: "proreta-verify=1", cnameTarget: "" },
      }),
    ]);
    expect(await screen.findByText(/CNAME target is not set up/)).toBeInTheDocument();
    expect(screen.queryByLabelText("CNAME value")).not.toBeInTheDocument();
  });
});

describe("custom domain section: add", () => {
  it("adds a domain and shows the records it returns", async () => {
    let items: StatusPageDomain[] = [];
    let body: unknown;
    setup([], {
      extra: {
        [`GET ${base}/domains`]: () => json({ items }),
        [`POST ${base}/domains`]: async (r: Request) => {
          body = await r.json();
          items = [domain("status.acme.com", "pending")];
          return json(items[0], 201);
        },
      },
    });
    await userEvent.type(await screen.findByLabelText("Domain"), "status.acme.com");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));
    expect(await screen.findByLabelText("TXT name")).toHaveValue("_proreta-status.status.acme.com");
    expect(body).toEqual({ host: "status.acme.com" });
    expect(screen.getByLabelText("Domain")).toHaveValue("");
  });

  it("checks the input before sending it", async () => {
    setup([]);
    const input = await screen.findByLabelText("Domain");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));
    expect(await screen.findByText("Enter a domain.")).toBeInTheDocument();
    await userEvent.type(input, "https://status.acme.com/");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));
    expect(await screen.findByText(/Enter the host name only/)).toBeInTheDocument();
  });

  it("shows the server's reason on the field", async () => {
    setup([], {
      extra: {
        [`POST ${base}/domains`]: () =>
          problem(422, "This host cannot be used", {
            type: "urn:proreta:problem:domain-forbidden",
            fieldErrors: [{ field: "host", message: "Use a name below your own domain." }],
          }),
      },
    });
    await userEvent.type(await screen.findByLabelText("Domain"), "acme.com");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));
    expect(await screen.findByText("Use a name below your own domain.")).toBeInTheDocument();
    expect(screen.getByLabelText("Domain")).toBeInvalid();
  });

  it("gives the same calm answer for a host that is not available", async () => {
    setup([], {
      extra: {
        [`POST ${base}/domains`]: () =>
          problem(409, "This host is not available", {
            type: "urn:proreta:problem:domain-unavailable",
          }),
      },
    });
    await userEvent.type(await screen.findByLabelText("Domain"), "status.acme.com");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));
    expect(await screen.findByText("This host is not available.")).toBeInTheDocument();
  });

  it("shows the plan limit", async () => {
    setup([], {
      extra: {
        [`POST ${base}/domains`]: () =>
          problem(409, "Plan limit reached", {
            type: "urn:proreta:problem:plan-limit-reached",
            limit: "statusPageDomains",
            max: 1,
          }),
      },
    });
    await userEvent.type(await screen.findByLabelText("Domain"), "status.acme.com");
    await userEvent.click(screen.getByRole("button", { name: "Add domain" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Your plan allows at most 1 status page domain",
    );
  });

  it("replaces the form with a note at the per-page cap", async () => {
    setup([
      domain("a.acme.com", "active", { primary: true }),
      domain("b.acme.com", "active"),
      domain("c.acme.com", "active"),
    ]);
    expect(await screen.findByText(/at most 3 domains/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Domain")).not.toBeInTheDocument();
  });
});

describe("custom domain section: check now", () => {
  it("announces the result in a live region", async () => {
    setup([domain("p.acme.com", "pending")], {
      extra: {
        [`POST ${base}/domains/d-p.acme.com/verify`]: () =>
          json(domain("p.acme.com", "verified", { lastError: "cname-missing" }), 202),
      },
    });
    await section();
    await userEvent.click(await screen.findByRole("button", { name: "Check p.acme.com now" }));
    const live = await screen.findByText(/the TXT record was found. Waiting for the CNAME/);
    expect(live.closest("[aria-live]")).toHaveAttribute("aria-live", "polite");
  });

  it("shows a rate limit as a wait, not an error", async () => {
    setup([domain("p.acme.com", "pending")], {
      extra: {
        [`POST ${base}/domains/d-p.acme.com/verify`]: () =>
          new Response(
            JSON.stringify({ type: "about:blank", title: "Too many requests", status: 429 }),
            {
              status: 429,
              headers: { "Content-Type": "application/problem+json", "Retry-After": "30" },
            },
          ),
      },
    });
    await section();
    await userEvent.click(await screen.findByRole("button", { name: "Check p.acme.com now" }));
    expect(await screen.findByText(/Try again in 30 seconds/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("custom domain section: primary and remove", () => {
  it("makes a domain primary after confirming", async () => {
    let patched = false;
    setup([domain("a.acme.com", "active", { primary: true }), domain("b.acme.com", "active")], {
      extra: {
        [`PATCH ${base}/domains/d-b.acme.com`]: async (r: Request) => {
          patched = ((await r.json()) as { primary: boolean }).primary;
          return json(domain("b.acme.com", "active", { primary: true }));
        },
      },
    });
    await section();
    await userEvent.click(await screen.findByRole("button", { name: "Make b.acme.com primary" }));
    const dialog = screen.getByRole("dialog", { name: "Make this the primary domain?" });
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
    await userEvent.click(within(dialog).getByRole("button", { name: "Make primary" }));
    await waitFor(() => expect(patched).toBe(true));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("warns that removing a serving primary stops serving and promotes another", async () => {
    let removed = false;
    setup([domain("a.acme.com", "active", { primary: true }), domain("b.acme.com", "active")], {
      extra: {
        [`DELETE ${base}/domains/d-a.acme.com`]: () => {
          removed = true;
          return new Response(null, { status: 204 });
        },
      },
    });
    await section();
    await userEvent.click(await screen.findByRole("button", { name: "Remove a.acme.com" }));
    const dialog = screen.getByRole("dialog", { name: "Remove a.acme.com?" });
    expect(dialog).toHaveTextContent("stops being served on this domain at once");
    expect(dialog).toHaveTextContent("Another domain of this page becomes the primary one");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(removed).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Remove a.acme.com" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Remove domain" }),
    );
    await waitFor(() => expect(removed).toBe(true));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("does not claim a pending domain was serving", async () => {
    setup([domain("p.acme.com", "pending")]);
    await section();
    await userEvent.click(await screen.findByRole("button", { name: "Remove p.acme.com" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog).not.toHaveTextContent("stops being served");
    expect(dialog).not.toHaveTextContent("becomes the primary");
  });
});
