import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { setCsrfToken } from "../api/client";
import type { MembershipRole, PermissionsInfo, Project } from "../api/endpoints";
import App from "../App";
import { AuthProvider } from "../auth/AuthProvider";
import { ToastProvider } from "../components/Toasts";

type Handler = (request: Request) => Response | Promise<Response>;

export const json = (body: unknown, status = 200) => Response.json(body, { status });

export const problem = (status: number, title: string, extra: object = {}) =>
  new Response(JSON.stringify({ type: "about:blank", title, status, ...extra }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });

const PROJECT_VIEWER = [
  "channel.read",
  "incident.read",
  "monitor.read",
  "project.members.read",
  "project.read",
  "specula.read",
  "statuspage.read",
];
const PROJECT_MEMBER = [
  ...PROJECT_VIEWER,
  "heartbeat.manage",
  "monitor.create",
  "monitor.delete",
  "monitor.update",
];
const PROJECT_MAINTAINER = [
  ...PROJECT_MEMBER,
  "channel.manage",
  "project.members.manage",
  "project.update",
  "statuspage.manage",
];
/** Project permissions per role, written out as the role table of the server has them. */
export const PROJECT_PERMISSIONS: Record<MembershipRole, string[]> = {
  viewer: PROJECT_VIEWER,
  member: PROJECT_MEMBER,
  maintainer: PROJECT_MAINTAINER,
  owner: [...PROJECT_MAINTAINER, "project.delete"],
};
const ORGANIZATION_READ = ["organization.members.read", "organization.statuspages.read"];
const ORGANIZATION_MANAGE = [
  ...ORGANIZATION_READ,
  "organization.members.manage",
  "organization.statuspages.manage",
  "project.create",
  "specula.manage",
  "usage.read",
];
const ORGANIZATION_PERMISSIONS: Record<MembershipRole, string[]> = {
  viewer: ORGANIZATION_READ,
  member: ORGANIZATION_READ,
  maintainer: ORGANIZATION_MANAGE,
  owner: [...ORGANIZATION_MANAGE, "organization.delete", "organization.domains.manage"],
};

/** The body of GET /api/v1/permissions for an organization role and the projects as the person sees them (their role there is the effective one). */
export function permissionsBody(
  role: MembershipRole | null | undefined,
  projects: { id: string; name?: string; role?: MembershipRole | null }[] = [],
): PermissionsInfo {
  const org = role ? [...ORGANIZATION_PERMISSIONS[role], ...PROJECT_PERMISSIONS[role]] : [];
  return {
    organizationRole: role ?? null,
    organization: ["account.use", ...org],
    projects: projects.flatMap((p) =>
      p.role
        ? [
            {
              projectId: p.id,
              name: p.name ?? p.id,
              role: p.role,
              permissions: PROJECT_PERMISSIONS[p.role],
            },
          ]
        : [],
    ),
    catalogue: [],
  };
}

/**
 * Replaces the global fetch with a router keyed by "METHOD /path" (no query string). Unknown
 * routes answer 501, so a test fails visibly on a call it did not expect. Returns the mock.
 */
export function mockApi(routes: Record<string, Handler>) {
  const mock = vi.fn(async (input: RequestInfo | URL) => {
    const request = input instanceof Request ? input : new Request(input);
    const url = new URL(request.url);
    const handler = routes[`${request.method} ${url.pathname}`];
    if (!handler && request.method === "GET" && url.pathname === "/api/v1/permissions") {
      // Derived from the session and project list the test already serves.
      const me = routes["GET /api/v1/auth/me"];
      const list = routes["GET /api/v1/projects"];
      const who = me
        ? ((await (await me(request)).json()) as { role?: MembershipRole | null })
        : {};
      const projects = list
        ? (((await (await list(request)).json()) as { items?: Project[] }).items ?? [])
        : [];
      return json(permissionsBody(who.role, projects));
    }
    if (!handler) return problem(501, "Not implemented yet");
    return handler(request);
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

export const session = {
  user: { id: "u1", login: "admin", displayName: "Admin", isPlatformAdmin: false },
  organization: { id: "t1", slug: "acme", name: "Acme" },
  role: "owner",
  csrfToken: "csrf-1",
  expiresAt: "2030-01-01T00:00:00Z",
  features: [] as string[],
  mustChangePassword: false,
};

export const signedIn = { "GET /api/v1/auth/me": () => json(session) };

export function renderApp(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const view = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <AuthProvider>
          <ToastProvider>
            <App />
          </ToastProvider>
        </AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, queryClient };
}

export function renderWithClient(ui: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
}

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken(null);
});
