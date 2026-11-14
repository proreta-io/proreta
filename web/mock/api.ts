/**
 * A fake Proreta API for trying the UI without a backend: `npm run dev:mock`. Dev only, never in
 * the build. State lives in the dev server's memory: it survives page reloads and is reset by
 * restarting `npm run dev:mock`. Operations it does not know answer 501, which the app shows as
 * "This feature is not available yet".
 *
 * The data is deliberately awkward: long logins, people with project roles only, several projects
 * and organizations, a failed channel, a down monitor.
 */
import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";
import type { components } from "../src/api/generated/schema";
import { RANK } from "../src/lib/roles";
import { permissionsBody } from "../src/test/permissions";

type S = components["schemas"];
type Role = S["MembershipRole"];

const higher = (a: Role | null, b: Role | null): Role | null =>
  a && b ? (RANK[a] >= RANK[b] ? a : b) : (a ?? b);

const now = Date.now();
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const day = (n: number) => ago(n * 24 * 60);
let seq = 1000;
const id = (prefix: string) => `${prefix}-${++seq}`;

// --- state -----------------------------------------------------------------------------------

interface User {
  id: string;
  login: string;
  displayName: string;
  isPlatformAdmin: boolean;
  disabled: boolean;
}
interface Org {
  id: string;
  slug: string;
  name: string;
  status: S["OrganizationStatus"];
  planKey: string;
  createdAt: string;
  deletedAt: string | null;
}
interface Membership {
  orgId: string;
  userId: string;
  role: Role | null;
  joinedAt: string;
}
interface ProjectRow {
  id: string;
  orgId: string;
  name: string;
  slug: string;
  isDefault: boolean;
  createdAt: string;
}
interface ProjectRole {
  projectId: string;
  userId: string;
  role: Role;
}

const users: User[] = [
  ["u-me", "jonathan.whitfield@acme.example.com", "Jonathan Whitfield", true],
  ["u-max", "peter.lindqvist@acme.example.com", "Peter Lindqvist", false],
  ["u-vy", "r.okafor@acme.example.com", "", false],
  ["u-yk", "m.castellano@acme.example.com", "Maria Castellano", false],
  [
    "u-long",
    "alexandra.fitzgerald-montgomery.infrastructure@acme-operations-group.example.com",
    "Alexandra Fitzgerald-Montgomery",
    false,
  ],
  [
    "u-ext",
    "contractor.devops.team.lead+monitoring@external-partner-agency.example.com",
    "",
    false,
  ],
  ["u-ana", "elisabeth.vandenberg@acme.example.com", "Elisabeth", false],
  ["u-dmy", "daniel.okonkwo@acme.example.com", "", false],
  ["u-ira", "irene.nakamura@acme.example.com", "Irene Nakamura", false],
  ["u-bot", "ci-bot-readonly-access-for-dashboards@acme.example.com", "", false],
].map(([uid, login, displayName, admin]) => ({
  id: uid as string,
  login: login as string,
  displayName: displayName as string,
  isPlatformAdmin: admin as boolean,
  disabled: uid === "u-bot",
}));

const orgs: Org[] = [
  {
    id: "o-acme",
    slug: "acme-example-com",
    name: "acme.example.com",
    status: "active",
    planKey: "business",
    createdAt: day(30),
    deletedAt: null,
  },
  {
    id: "o-lab",
    slug: "acme-lab",
    name: "Acme Laboratory & Research Infrastructure",
    status: "active",
    planKey: "starter",
    createdAt: day(12),
    deletedAt: null,
  },
  {
    id: "o-old",
    slug: "legacy",
    name: "Legacy Monitoring (archived)",
    status: "suspended",
    planKey: "free",
    createdAt: day(200),
    deletedAt: null,
  },
];

const memberships: Membership[] = [
  { orgId: "o-acme", userId: "u-me", role: "owner", joinedAt: day(30) },
  { orgId: "o-acme", userId: "u-max", role: null, joinedAt: day(29) },
  { orgId: "o-acme", userId: "u-vy", role: "maintainer", joinedAt: day(29) },
  { orgId: "o-acme", userId: "u-yk", role: "member", joinedAt: day(1) },
  { orgId: "o-acme", userId: "u-long", role: "viewer", joinedAt: day(9) },
  { orgId: "o-acme", userId: "u-ext", role: null, joinedAt: day(4) },
  { orgId: "o-acme", userId: "u-ana", role: "member", joinedAt: day(20) },
  { orgId: "o-acme", userId: "u-dmy", role: null, joinedAt: day(15) },
  { orgId: "o-acme", userId: "u-ira", role: "owner", joinedAt: day(28) },
  { orgId: "o-acme", userId: "u-bot", role: "viewer", joinedAt: day(25) },
  { orgId: "o-lab", userId: "u-me", role: "maintainer", joinedAt: day(12) },
  { orgId: "o-lab", userId: "u-ana", role: "owner", joinedAt: day(12) },
  { orgId: "o-old", userId: "u-me", role: "viewer", joinedAt: day(200) },
];

const projects: ProjectRow[] = [
  {
    id: "p-def",
    orgId: "o-acme",
    name: "Default",
    slug: "default",
    isDefault: true,
    createdAt: day(30),
  },
  {
    id: "p-node",
    orgId: "o-acme",
    name: "Node",
    slug: "node",
    isDefault: false,
    createdAt: day(20),
  },
  {
    id: "p-pay",
    orgId: "o-acme",
    name: "Payments & Billing Gateway (production, EU region)",
    slug: "payments",
    isDefault: false,
    createdAt: day(18),
  },
  {
    id: "p-int",
    orgId: "o-acme",
    name: "Internal tools",
    slug: "internal-tools",
    isDefault: false,
    createdAt: day(10),
  },
  {
    id: "p-lab",
    orgId: "o-lab",
    name: "Default",
    slug: "default",
    isDefault: true,
    createdAt: day(12),
  },
  {
    id: "p-old",
    orgId: "o-old",
    name: "Default",
    slug: "default",
    isDefault: true,
    createdAt: day(200),
  },
];

const projectRoles: ProjectRole[] = [
  { projectId: "p-node", userId: "u-max", role: "member" },
  { projectId: "p-def", userId: "u-max", role: "member" },
  { projectId: "p-pay", userId: "u-ext", role: "maintainer" },
  { projectId: "p-int", userId: "u-ext", role: "viewer" },
  { projectId: "p-pay", userId: "u-yk", role: "maintainer" },
  { projectId: "p-node", userId: "u-dmy", role: "owner" },
  { projectId: "p-int", userId: "u-long", role: "member" },
];

const invitations: (S["Invitation"] & { orgId: string })[] = [
  {
    orgId: "o-acme",
    id: "i-1",
    role: "member",
    projectId: null,
    projectName: null,
    label: "New backend engineer starting on Monday (Andrii)",
    status: "open",
    createdAt: day(1),
    expiresAt: day(-6),
    createdBy: "jonathan.whitfield@acme.example.com",
    acceptedBy: null,
    acceptedAt: null,
    revokedAt: null,
  },
  {
    orgId: "o-acme",
    id: "i-2",
    role: "maintainer",
    projectId: "p-pay",
    projectName: "Payments & Billing Gateway (production, EU region)",
    label: "",
    status: "open",
    createdAt: day(2),
    expiresAt: day(-5),
    createdBy: "r.okafor@acme.example.com",
    acceptedBy: null,
    acceptedAt: null,
    revokedAt: null,
  },
  {
    orgId: "o-acme",
    id: "i-3",
    role: "viewer",
    projectId: null,
    projectName: null,
    label: "Auditor",
    status: "accepted",
    createdAt: day(9),
    expiresAt: day(2),
    createdBy: "jonathan.whitfield@acme.example.com",
    acceptedBy: "alexandra.fitzgerald-montgomery.infrastructure@acme-operations-group.example.com",
    acceptedAt: day(9),
    revokedAt: null,
  },
  {
    orgId: "o-acme",
    id: "i-4",
    role: "member",
    projectId: null,
    projectName: null,
    label: "Old link",
    status: "expired",
    createdAt: day(20),
    expiresAt: day(13),
    createdBy: "irene.nakamura@acme.example.com",
    acceptedBy: null,
    acceptedAt: null,
    revokedAt: null,
  },
];

const allEvents: S["ChannelEvents"] = ["down", "recovered", "tls_expiring"];
const channels: (S["Channel"] & { orgId: string })[] = [
  ["#test-alerts", "slack_webhook", null, allEvents, [], 15, "active"],
  [
    "#payments-production-incidents-and-escalations",
    "slack_webhook",
    "p-pay",
    ["down", "recovered", "tls_expiring", "agent_offline", "agent_online"],
    ["@oncall-payments", "<!subteam^S0123>"],
    30,
    "active",
  ],
  ["Opsgenie webhook (EU)", "webhook", "p-pay", ["down", "recovered"], [], 0, "failed"],
  ["#node-team", "slack_webhook", "p-node", ["down"], ["@here"], 0, "active"],
  ["#internal", "slack_webhook", "p-int", allEvents, [], 60, "active"],
].map(([name, type, projectId, events, mention, reminderMinutes, status]) => ({
  orgId: "o-acme",
  id: id("c"),
  projectId: projectId as string | null,
  name: name as string,
  type: type as S["ChannelType"],
  configHidden: false,
  events: events as S["ChannelEvents"],
  mention: mention as string[],
  reminderMinutes: reminderMinutes as number,
  status: status as "active" | "failed",
  createdAt: day(10),
  updatedAt: day(2),
}));

const monitorNames: [string, string, string, S["MonitorStatus"], boolean][] = [
  ["Public website", "p-def", "https://acme.example.com/", "up", false],
  ["API health", "p-def", "https://api.acme.example.com/healthz", "up", false],
  [
    "Payments gateway – card authorisation endpoint (EU-West primary)",
    "p-pay",
    "https://payments.eu-west-1.internal.acme-operations-group.example.com/v2/authorise/health?deep=true",
    "down",
    false,
  ],
  ["Payments webhooks", "p-pay", "https://payments.acme.example.com/webhooks/status", "up", false],
  ["Node RPC", "p-node", "https://rpc.node.acme.example.com/", "up", false],
  ["Node explorer", "p-node", "https://explorer.node.acme.example.com/", "unknown", true],
  ["Grafana", "p-int", "https://grafana.internal.acme.example.com/login", "up", false],
  ["Jira", "p-int", "https://jira.acme.example.com/status", "up", false],
];
const monitors: S["MonitorListItem"][] = monitorNames.map(
  ([name, projectId, url, status, paused], i) => ({
    id: `m-${i + 1}`,
    projectId,
    type: "http",
    name,
    notificationsEnabled: true,
    paused,
    status: paused ? "unknown" : status,
    statusSince: ago(status === "down" ? 42 : 60 * 24 * (i + 1)),
    lastErrorKind: status === "down" ? "http_status" : null,
    lastCheckedAt: paused ? null : ago(1),
    ongoingIncident:
      status === "down" ? { id: "inc-1", startedAt: ago(42), reason: "http_status" } : null,
    uptime24h: paused ? null : status === "down" ? 97.08 : 100 - i * 0.013,
    http: {
      url,
      expectedStatus: "2xx",
      method: "GET",
      headerNames: [],
      bodySet: false,
      intervalSeconds: 60,
      timeoutSeconds: 10,
      failureThreshold: 3,
      tlsExpiresAt: day(-40 + i * 9),
      lastResponseMs: paused ? null : 80 + i * 37,
    },
  }),
);

const session = { userId: "u-me", orgId: "o-acme", signedIn: true };

// --- views -----------------------------------------------------------------------------------

const userOf = (uid: string) => users.find((u) => u.id === uid)!;
const orgOf = (oid: string) => orgs.find((o) => o.id === oid)!;
const membersOf = (oid: string) => memberships.filter((m) => m.orgId === oid);
const roleIn = (oid: string, uid: string) =>
  memberships.find((m) => m.orgId === oid && m.userId === uid)?.role ?? null;
const projectsOf = (oid: string) => projects.filter((p) => p.orgId === oid);

function projectMembers(p: ProjectRow): S["ProjectMember"][] {
  return membersOf(p.orgId).flatMap((m) => {
    const direct =
      projectRoles.find((r) => r.projectId === p.id && r.userId === m.userId)?.role ?? null;
    const effective = higher(m.role, direct);
    if (!effective) return [];
    const u = userOf(m.userId);
    return [
      {
        userId: u.id,
        login: u.login,
        displayName: u.displayName,
        organizationRole: m.role,
        projectRole: direct,
        effectiveRole: effective,
        explicit: direct !== null,
        allProjects: m.role !== null,
      },
    ];
  });
}

/** The actor's effective role in a project (the higher of the organization and project roles). */
const myRoleIn = (p: ProjectRow) =>
  higher(
    roleIn(p.orgId, session.userId),
    projectRoles.find((r) => r.projectId === p.id && r.userId === session.userId)?.role ?? null,
  );

function project(p: ProjectRow): S["Project"] {
  return {
    id: p.id,
    name: p.name,
    slug: p.slug,
    isDefault: p.isDefault,
    allMembers: p.isDefault,
    role: myRoleIn(p) ?? "viewer",
    monitorCount: monitors.filter((m) => m.projectId === p.id).length,
    channelCount: channels.filter((c) => c.projectId === p.id).length,
    createdAt: p.createdAt,
    updatedAt: p.createdAt,
  };
}

function adminProject(p: ProjectRow): S["AdminProject"] {
  const { role: _role, ...rest } = project(p);
  return { ...rest, memberCount: projectMembers(p).length };
}

function member(m: Membership): S["Member"] {
  const u = userOf(m.userId);
  return {
    userId: u.id,
    login: u.login,
    displayName: u.displayName,
    role: m.role,
    joinedAt: m.joinedAt,
    isPlatformAdmin: u.isPlatformAdmin,
    isSystemAdmin: false,
  };
}

const adminMember = (m: Membership): S["AdminMember"] => ({
  ...member(m),
  login: userOf(m.userId).login,
  status: userOf(m.userId).disabled ? "disabled" : "active",
});

function sessionInfo(): S["SessionInfo"] {
  const u = userOf(session.userId);
  const o = orgOf(session.orgId);
  return {
    user: {
      id: u.id,
      login: u.login,
      displayName: u.displayName,
      isPlatformAdmin: u.isPlatformAdmin,
      emailVerified: true,
    },
    mustChangePassword: false,
    organization: { id: o.id, slug: o.slug, name: o.name },
    role: roleIn(o.id, u.id),
    emailInvitations: false,
    csrfToken: "mock-csrf",
    expiresAt: day(-7),
    features: [],
  };
}

const domains: S["AdminStatusPageDomain"][] = [
  ["status.acme.example", "o-acme", "acme-example-com", "main", "active", true, 3, null],
  ["uptime.acme.example", "o-acme", "acme-example-com", "main", "verified", false, 2, null],
  [
    "status.xn--bcher-kva.example",
    "o-acme",
    "acme-example-com",
    "main",
    "pending",
    false,
    null,
    "txt-missing",
  ],
  [
    "status.old.example",
    "o-acme",
    "acme-example-com",
    "main",
    "failed",
    false,
    null,
    "txt-mismatch",
  ],
].map(([host, orgId, slug, page, state, primary, verified, lastError], i) => ({
  id: `d-${i + 1}`,
  host: String(host),
  hostUnicode: String(host).replace("xn--bcher-kva", "bücher"),
  state: state as S["StatusPageDomainState"],
  primary: primary as boolean,
  disabled: false,
  disabledAt: null as string | null,
  organizationId: String(orgId),
  organizationSlug: String(slug),
  statusPageId: `sp-${i + 1}`,
  statusPageSlug: String(page),
  verifiedAt: verified === null ? null : day(verified as number),
  lastCheckedAt: day(0),
  lastError: lastError as S["StatusPageDomainError"] | null,
  createdAt: day(5),
}));

function adminOrg(o: Org): S["AdminOrganization"] {
  return {
    id: o.id,
    slug: o.slug,
    name: o.name,
    planKey: o.planKey,
    planName: o.planKey[0]!.toUpperCase() + o.planKey.slice(1),
    status: o.status,
    createdAt: o.createdAt,
    deletedAt: o.deletedAt,
    users: membersOf(o.id).length,
    monitors: o.id === "o-acme" ? monitors.length : 0,
    heartbeats: 0,
  };
}

function adminUserSummary(u: User): S["AdminUserSummary"] {
  return {
    id: u.id,
    login: u.login,
    displayName: u.displayName,
    status: u.disabled ? "disabled" : "active",
    isPlatformAdmin: u.isPlatformAdmin,
    isSystemAdmin: false,
    emailVerified: true,
    mustChangePassword: false,
    createdAt: day(30),
    organizationCount: memberships.filter((m) => m.userId === u.id).length,
  };
}

// --- routes ----------------------------------------------------------------------------------

type Params = Record<string, string>;
interface Req {
  params: Params;
  query: URLSearchParams;
  body: Record<string, unknown>;
}
type Result = { status?: number; body?: unknown } | undefined;
type Route = [method: string, pattern: string, handle: (r: Req) => Result];

const ok = (body: unknown): Result => ({ body });
const created = (body: unknown): Result => ({ status: 201, body });
const noContent: Result = { status: 204 };
const notFound = (): Result => ({
  status: 404,
  body: { type: "about:blank", title: "Not found", status: 404 },
});

/** Removes the row with this id; a stale id removes nothing (splice(-1) would drop the last row). */
function dropById(list: { id: string }[], rowId: string) {
  const i = list.findIndex((x) => x.id === rowId);
  if (i >= 0) list.splice(i, 1);
}

function setProjectRole(projectId: string, userId: string, role: Role | null) {
  const i = projectRoles.findIndex((r) => r.projectId === projectId && r.userId === userId);
  if (i >= 0) projectRoles.splice(i, 1);
  if (role) projectRoles.push({ projectId, userId, role });
}

function projectMember(p: ProjectRow, userId: string): S["ProjectMember"] {
  const u = userOf(userId);
  return (
    projectMembers(p).find((m) => m.userId === userId) ?? {
      userId,
      login: u.login,
      displayName: u.displayName,
      organizationRole: null,
      projectRole: null,
      effectiveRole: "viewer",
      explicit: false,
      allProjects: false,
    }
  );
}

function createProject(orgId: string, name: string) {
  const p: ProjectRow = {
    id: id("p"),
    orgId,
    name,
    slug: name.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
    isDefault: false,
    createdAt: new Date().toISOString(),
  };
  projects.push(p);
  return p;
}

function setMember(orgId: string, userId: string, role: Role | null) {
  const m = memberships.find((x) => x.orgId === orgId && x.userId === userId);
  if (m) m.role = role;
  else if (!users.some((u) => u.id === userId)) throw new Error(`no user ${userId}`);
  else memberships.push({ orgId, userId, role, joinedAt: new Date().toISOString() });
  return memberships.find((x) => x.orgId === orgId && x.userId === userId)!;
}

function removeMember(orgId: string, userId: string) {
  const i = memberships.findIndex((m) => m.orgId === orgId && m.userId === userId);
  if (i >= 0) memberships.splice(i, 1);
  for (const p of projectsOf(orgId)) setProjectRole(p.id, userId, null);
}

const projectById = (pid: string) => projects.find((p) => p.id === pid);

const problem = (status: number, title: string, summary: string, extra: object = {}): Result => ({
  status,
  body: { type: `urn:proreta:problem:${title}`, title: summary, status, ...extra },
});

const statusPage = {
  id: "sp-acme",
  projectId: null,
  slug: "acme",
  title: "Acme status",
  description: "",
  logoUrl: null,
  accentColor: null,
  published: true,
  passwordProtected: false,
  publicUrl: "http://localhost:5173/status/acme",
  componentCount: 0,
  createdAt: day(20),
  updatedAt: day(2),
};

function mockDomain(host: string, state: S["StatusPageDomain"]["state"]): S["StatusPageDomain"] {
  return {
    id: id("d"),
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
    verifiedAt: state === "pending" ? null : day(3),
    lastCheckedAt: state === "pending" ? null : day(0),
    lastError: state === "pending" ? "txt-missing" : null,
    expiresAt: state === "pending" ? new Date(Date.now() + 7 * 86_400_000).toISOString() : null,
    createdAt: day(3),
  };
}

const statusPageDomains: S["StatusPageDomain"][] = [
  { ...mockDomain("status.acme.test", "active"), primary: true },
  mockDomain("uptime.acme.test", "pending"),
];

const routes: Route[] = [
  ["GET", "/api/v1/public/config", () => ok({ registrationEnabled: true })],
  [
    "GET",
    "/api/v1/auth/providers",
    () =>
      ok({
        providers: [],
        passwordLoginEnabled: true,
        registrationEnabled: true,
        emailEnabled: false,
        passwordResetEnabled: false,
        emailVerificationEnabled: false,
      }),
  ],
  [
    "GET",
    "/api/v1/auth/me",
    () =>
      session.signedIn
        ? ok(sessionInfo())
        : { status: 401, body: { type: "about:blank", title: "Unauthorized", status: 401 } },
  ],
  [
    "POST",
    "/api/v1/auth/login",
    () => {
      session.signedIn = true;
      return ok(sessionInfo());
    },
  ],
  [
    "POST",
    "/api/v1/auth/logout",
    () => {
      session.signedIn = false;
      return noContent;
    },
  ],
  [
    "GET",
    "/api/v1/auth/organizations",
    () =>
      ok({
        items: memberships
          .filter((m) => m.userId === session.userId)
          .map((m) => {
            const o = orgOf(m.orgId);
            return {
              id: o.id,
              slug: o.slug,
              name: o.name,
              role: m.role,
              status: o.status,
              current: o.id === session.orgId,
            };
          }),
      }),
  ],
  [
    "POST",
    "/api/v1/auth/switch-organization",
    ({ body }) => {
      session.orgId = String(body.organizationId);
      return ok(sessionInfo());
    },
  ],
  ["GET", "/api/v1/auth/identities", () => ok({ items: [] })],
  ["GET", "/api/v1/organization/auth-domains", () => ok({ items: [] })],
  [
    "GET",
    "/api/v1/permissions",
    () => {
      const role = roleIn(session.orgId, session.userId);
      const visible = projectsOf(session.orgId).filter((p) => myRoleIn(p));
      return ok(
        permissionsBody(
          role,
          visible.map((p) => ({ id: p.id, name: p.name, role: myRoleIn(p) })),
        ),
      );
    },
  ],
  [
    "GET",
    "/api/v1/usage",
    () =>
      ok({
        plan: { key: "business", name: "Business" },
        limits: {
          monitors: 50,
          heartbeats: 20,
          channels: 10,
          members: 25,
          projects: 10,
          statusPages: 5,
          specula: 3,
          statusPageDomains: 3,
        },
        usage: {
          monitors: monitors.length,
          heartbeats: 0,
          channels: channels.length,
          members: membersOf(session.orgId).length,
          projects: projectsOf(session.orgId).length,
          statusPages: 1,
          specula: 0,
          statusPageDomains: statusPageDomains.length,
        },
      }),
  ],

  // Members and invitations of the session's organization.
  ["GET", "/api/v1/members", () => ok({ items: membersOf(session.orgId).map(member) })],
  [
    "PATCH",
    "/api/v1/members/:userId",
    ({ params, body }) =>
      ok(member(setMember(session.orgId, params.userId!, (body.role as Role | null) ?? null))),
  ],
  [
    "DELETE",
    "/api/v1/members/:userId",
    ({ params }) => {
      removeMember(session.orgId, params.userId!);
      return noContent;
    },
  ],
  [
    "GET",
    "/api/v1/invitations",
    ({ query }) =>
      ok({
        items: invitations
          .filter(
            (i) =>
              i.orgId === session.orgId &&
              (!query.get("projectId") || i.projectId === query.get("projectId")),
          )
          .map(({ orgId: _o, ...i }) => i),
      }),
  ],
  [
    "POST",
    "/api/v1/invitations/:id/revoke",
    ({ params }) => {
      const inv = invitations.find((i) => i.id === params.id);
      if (inv) {
        inv.status = "revoked";
        inv.revokedAt = new Date().toISOString();
      }
      return noContent;
    },
  ],

  // Projects.
  [
    "GET",
    "/api/v1/projects",
    () =>
      ok({
        items: projectsOf(session.orgId)
          .filter((p) => myRoleIn(p))
          .map(project),
      }),
  ],
  [
    "POST",
    "/api/v1/projects",
    ({ body }) => created(project(createProject(session.orgId, String(body.name)))),
  ],
  [
    "PATCH",
    "/api/v1/projects/:id",
    ({ params, body }) => {
      const p = projectById(params.id!);
      if (!p) return notFound();
      p.name = String(body.name);
      return ok(project(p));
    },
  ],
  [
    "DELETE",
    "/api/v1/projects/:id",
    ({ params }) => {
      dropById(projects, params.id!);
      return noContent;
    },
  ],
  [
    "GET",
    "/api/v1/projects/:id/members",
    ({ params }) => {
      const p = projectById(params.id!);
      return p ? ok({ items: projectMembers(p) }) : notFound();
    },
  ],
  [
    "PUT",
    "/api/v1/projects/:id/members/:userId",
    ({ params, body }) => {
      const p = projectById(params.id!);
      if (!p) return notFound();
      setProjectRole(p.id, params.userId!, body.role as Role);
      return ok(projectMember(p, params.userId!));
    },
  ],
  [
    "DELETE",
    "/api/v1/projects/:id/members/:userId",
    ({ params }) => {
      setProjectRole(params.id!, params.userId!, null);
      return noContent;
    },
  ],

  // Monitoring.
  [
    "GET",
    "/api/v1/monitors",
    ({ query }) => {
      const pid = query.get("projectId");
      const ids = new Set(projectsOf(session.orgId).map((p) => p.id));
      const items = monitors.filter((m) => ids.has(m.projectId) && (!pid || m.projectId === pid));
      const count = (s: string) => items.filter((m) => !m.paused && m.status === s).length;
      return ok({
        summary: {
          up: count("up"),
          down: count("down"),
          unknown: count("unknown"),
          paused: items.filter((m) => m.paused).length,
        },
        items,
      });
    },
  ],
  [
    "GET",
    "/api/v1/channels",
    ({ query }) => {
      const pid = query.get("projectId");
      return ok({
        items: channels
          .filter(
            (c) =>
              c.orgId === session.orgId && (!pid || c.projectId === null || c.projectId === pid),
          )
          .map(({ orgId: _o, ...c }) => c),
      });
    },
  ],
  [
    "DELETE",
    "/api/v1/channels/:id",
    ({ params }) => {
      dropById(channels, params.id!);
      return noContent;
    },
  ],
  ["GET", "/api/v1/specula", () => ok({ items: [] })],
  ["GET", "/api/v1/status-pages", () => ok({ items: [statusPage] })],
  ["GET", "/api/v1/status-pages/:id/components", () => ok({ items: [] })],
  [
    "GET",
    "/api/v1/status-pages/:id/preview",
    () =>
      ok({
        version: 1,
        page: {
          slug: "acme",
          title: "Acme status",
          description: "",
          logoUrl: null,
          accentColor: null,
        },
        status: "operational",
        generatedAt: new Date().toISOString(),
        components: [],
        activeIncidents: [],
        pastIncidents: [],
      }),
  ],
  // Custom domains. Hosts starting with "bad" are refused (422), "taken" ones are unavailable (409);
  // "Check now" moves a domain pending -> verified -> active, except hosts starting with "fail".
  ["GET", "/api/v1/status-pages/:id/domains", () => ok({ items: statusPageDomains })],
  [
    "POST",
    "/api/v1/status-pages/:id/domains",
    ({ body }) => {
      const host = String(body.host ?? "")
        .trim()
        .toLowerCase();
      if (host.startsWith("bad"))
        return problem(422, "domain-forbidden", "This host cannot be used", {
          fieldErrors: [
            {
              field: "host",
              message: "This is a public suffix; use a name below your own domain.",
            },
          ],
        });
      if (host.startsWith("taken"))
        return problem(409, "domain-unavailable", "This host is not available");
      if (statusPageDomains.length >= 3)
        return problem(409, "plan-limit-reached", "Domain limit of the page reached", {
          detail: "A status page can have at most 3 domains.",
          limit: "statusPageDomains",
          max: 3,
        });
      const d = mockDomain(host, "pending");
      statusPageDomains.push(d);
      return created(d);
    },
  ],
  [
    "POST",
    "/api/v1/status-pages/:id/domains/:domainId/verify",
    ({ params }) => {
      const d = statusPageDomains.find((x) => x.id === params.domainId);
      if (!d) return notFound();
      d.lastCheckedAt = new Date().toISOString();
      if (d.host.startsWith("fail")) {
        d.state = "failed";
        d.lastError = "txt-missing";
      } else if (d.state === "pending" || d.state === "failed") {
        d.state = "verified";
        d.lastError = "cname-missing";
        d.verifiedAt = d.lastCheckedAt;
        d.expiresAt = null;
      } else {
        d.state = "active";
        d.lastError = null;
      }
      return { status: 202, body: d };
    },
  ],
  [
    "PATCH",
    "/api/v1/status-pages/:id/domains/:domainId",
    ({ params }) => {
      const d = statusPageDomains.find((x) => x.id === params.domainId);
      if (!d) return notFound();
      for (const x of statusPageDomains) x.primary = x === d;
      statusPageDomains.sort((a, b) => Number(b.primary) - Number(a.primary));
      return ok(d);
    },
  ],
  [
    "DELETE",
    "/api/v1/status-pages/:id/domains/:domainId",
    ({ params }) => {
      const was = statusPageDomains.find((x) => x.id === params.domainId);
      dropById(statusPageDomains, params.domainId!);
      if (was?.primary && statusPageDomains[0]) statusPageDomains[0].primary = true;
      return noContent;
    },
  ],

  // Admin Area.
  [
    "GET",
    "/api/v1/admin/stats",
    () =>
      ok({
        organizations: {
          total: orgs.length,
          active: orgs.filter((o) => o.status === "active").length,
          suspended: orgs.filter((o) => o.status === "suspended").length,
          deleted: 0,
        },
        users: users.length,
        monitors: monitors.length,
        heartbeats: 0,
        plans: [
          { key: "business", name: "Business", organizations: 1 },
          { key: "starter", name: "Starter", organizations: 1 },
          { key: "free", name: "Free", organizations: 1 },
        ],
      }),
  ],
  [
    "GET",
    "/api/v1/admin/organizations",
    () => ok({ items: orgs.map(adminOrg), total: orgs.length, limit: 25, offset: 0 }),
  ],
  ["GET", "/api/v1/admin/organizations/:org", ({ params }) => ok(adminOrg(orgOf(params.org!)))],
  [
    "GET",
    "/api/v1/admin/organizations/:org/members",
    ({ params }) => ok({ items: membersOf(params.org!).map(adminMember) }),
  ],
  [
    "PUT",
    "/api/v1/admin/organizations/:org/members/:userId",
    ({ params, body }) =>
      ok(adminMember(setMember(params.org!, params.userId!, (body.role as Role | null) ?? null))),
  ],
  [
    "DELETE",
    "/api/v1/admin/organizations/:org/members/:userId",
    ({ params }) => {
      removeMember(params.org!, params.userId!);
      return noContent;
    },
  ],
  [
    "GET",
    "/api/v1/admin/organizations/:org/invitations",
    ({ params }) =>
      ok({
        items: invitations.filter((i) => i.orgId === params.org).map(({ orgId: _o, ...i }) => i),
      }),
  ],
  [
    "GET",
    "/api/v1/admin/organizations/:org/projects",
    ({ params }) => ok({ items: projectsOf(params.org!).map(adminProject) }),
  ],
  [
    "POST",
    "/api/v1/admin/organizations/:org/projects",
    ({ params, body }) => created(adminProject(createProject(params.org!, String(body.name)))),
  ],
  [
    "PATCH",
    "/api/v1/admin/organizations/:org/projects/:id",
    ({ params, body }) => {
      const p = projectById(params.id!);
      if (!p) return notFound();
      p.name = String(body.name);
      return ok(adminProject(p));
    },
  ],
  [
    "DELETE",
    "/api/v1/admin/organizations/:org/projects/:id",
    ({ params }) => {
      dropById(projects, params.id!);
      return noContent;
    },
  ],
  [
    "GET",
    "/api/v1/admin/organizations/:org/projects/:id/members",
    ({ params }) => {
      const p = projectById(params.id!);
      return p ? ok({ items: projectMembers(p) }) : notFound();
    },
  ],
  [
    "PUT",
    "/api/v1/admin/organizations/:org/projects/:id/members/:userId",
    ({ params, body }) => {
      const p = projectById(params.id!);
      if (!p) return notFound();
      setProjectRole(p.id, params.userId!, body.role as Role);
      return ok(projectMember(p, params.userId!));
    },
  ],
  [
    "DELETE",
    "/api/v1/admin/organizations/:org/projects/:id/members/:userId",
    ({ params }) => {
      setProjectRole(params.id!, params.userId!, null);
      return noContent;
    },
  ],
  [
    "POST",
    "/api/v1/admin/users/:userId/reset-password",
    () => ok({ temporaryPassword: "Mock-Temp-Password-1234" }),
  ],
  [
    "GET",
    "/api/v1/admin/status-page-domains",
    ({ query }) => {
      const q = (query.get("query") ?? "").toLowerCase();
      const states = (query.get("state") ?? "").split(",").filter(Boolean);
      const items = domains.filter(
        (d) =>
          (!states.length || states.includes(d.state)) &&
          (!q || d.host.includes(q) || d.organizationSlug.includes(q)),
      );
      const offset = Number(query.get("offset") ?? 0);
      return ok({
        items: items.slice(offset, offset + 25),
        total: items.length,
        limit: 25,
        offset,
      });
    },
  ],
  [
    "POST",
    "/api/v1/admin/status-page-domains/:id/:action",
    ({ params, body }) => {
      const d = domains.find((x) => x.id === params.id);
      if (!d || !String(body.reason ?? "").trim()) return notFound();
      d.disabled = params.action === "disable";
      d.disabledAt = d.disabled ? ago(0) : null;
      return ok(d);
    },
  ],
  [
    "GET",
    "/api/v1/admin/users",
    ({ query }) => {
      const q = (query.get("query") ?? "").toLowerCase();
      const items = users
        .filter((u) => !q || u.login.includes(q) || u.displayName.toLowerCase().includes(q))
        .map(adminUserSummary);
      return ok({ items, total: items.length, limit: 25, offset: 0 });
    },
  ],
  [
    "GET",
    "/api/v1/admin/users/:userId",
    ({ params }) =>
      ok({
        user: adminUserSummary(userOf(params.userId!)),
        memberships: memberships
          .filter((m) => m.userId === params.userId)
          .map((m) => {
            const o = orgOf(m.orgId);
            return {
              organizationId: o.id,
              organizationName: o.name,
              organizationSlug: o.slug,
              organizationStatus: o.status,
              role: m.role,
              joinedAt: m.joinedAt,
            };
          }),
        openSessions: 2,
      }),
  ],
  [
    "GET",
    "/api/v1/admin/registration",
    () => ok({ effective: true, source: "database", environmentValue: false, updatedAt: day(3) }),
  ],
  [
    "GET",
    "/api/v1/admin/retention",
    () =>
      ok({
        deliveryDays: 30,
        rollupMonths: 13,
        deliveryDefault: true,
        rollupDefault: true,
        updatedAt: null,
      }),
  ],
];

const compiled = routes.map(([method, pattern, handle]) => {
  const names: string[] = [];
  const re = new RegExp(
    `^${pattern.replace(/:(\w+)/g, (_, n: string) => (names.push(n), "([^/]+)"))}$`,
  );
  return { method, re, names, handle };
});

async function readBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  let raw = "";
  for await (const chunk of req) raw += String(chunk);
  try {
    return raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

async function handle(req: IncomingMessage, res: ServerResponse) {
  const url = new URL(req.url ?? "/", "http://mock");
  for (const r of compiled) {
    const match = r.method === req.method ? r.re.exec(url.pathname) : null;
    if (!match) continue;
    const params = Object.fromEntries(
      r.names.map((n, i) => [n, decodeURIComponent(match[i + 1]!)]),
    );
    const result =
      r.handle({ params, query: url.searchParams, body: await readBody(req) }) ?? noContent;
    res.statusCode = result!.status ?? 200;
    if (result!.body === undefined) return res.end();
    res.setHeader(
      "Content-Type",
      res.statusCode >= 400 ? "application/problem+json" : "application/json",
    );
    return res.end(JSON.stringify(result!.body));
  }
  res.statusCode = 501;
  res.setHeader("Content-Type", "application/problem+json");
  res.end(
    JSON.stringify({ type: "about:blank", title: "Not implemented in the mock API", status: 501 }),
  );
}

/** Serves /api from memory instead of proxying it (see the top of this file). */
export function mockApi(): Plugin {
  return {
    name: "proreta-mock-api",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use("/api", (req, res) => {
        // connect strips the mount path; the routes above keep the full one.
        req.url = `/api${req.url ?? ""}`;
        // A throw (an unknown id, a bad escape) answers 404 instead of crashing the dev server.
        handle(req, res).catch((err: unknown) => {
          res.statusCode = 404;
          res.setHeader("Content-Type", "application/problem+json");
          res.end(JSON.stringify({ type: "about:blank", title: String(err), status: 404 }));
        });
      });
    },
  };
}
