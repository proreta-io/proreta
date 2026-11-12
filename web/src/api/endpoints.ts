import { ApiError, client, setCsrfToken, unwrap, type Schemas } from "./client";

export type SessionInfo = Schemas["SessionInfo"];
export type MonitorList = Schemas["MonitorList"];
export type MonitorListItem = Schemas["MonitorListItem"];
export type MonitorDetail = Schemas["MonitorDetail"];
export type MonitorCreate = Schemas["MonitorCreate"];
export type MonitorUpdate = Schemas["MonitorUpdate"];
export type HttpMonitor = Schemas["HttpMonitor"];
export type HeartbeatMonitor = Schemas["HeartbeatMonitor"];
export type ErrorKind = Schemas["ErrorKind"];
export type MonitorStats = Schemas["MonitorStats"];
export type StatsRange = Schemas["StatsRange"];
export type LatencyPoint = Schemas["LatencyPoint"];
export type Check = Schemas["Check"];
export type Incident = Schemas["Incident"];
export type Channel = Schemas["Channel"];
export type ChannelCreate = Schemas["ChannelCreate"];
export type ChannelUpdate = Schemas["ChannelUpdate"];
export type ChannelEvent = Schemas["ChannelEvent"];
export type ChannelTemplatePreviewRequest = Schemas["ChannelTemplatePreviewRequest"];
export type Usage = Schemas["Usage"];
export type PermissionsInfo = Schemas["PermissionsInfo"];
export type ProjectPermissions = Schemas["ProjectPermissions"];
export type PermissionDefinition = Schemas["PermissionDefinition"];
export type PublicConfig = Schemas["PublicConfig"];
export type AdminStats = Schemas["AdminStats"];
export type AdminOrganization = Schemas["AdminOrganization"];
export type AdminOrganizationList = Schemas["AdminOrganizationList"];
export type AdminOrganizationUpdate = Schemas["AdminOrganizationUpdate"];
export type AdminEmailSettings = Schemas["AdminEmailSettings"];
export type AdminEmailConnectionUpdate = Schemas["AdminEmailConnectionUpdate"];
export type AdminEmailFeaturesUpdate = Schemas["AdminEmailFeaturesUpdate"];
export type AdminEmailTestResult = Schemas["AdminEmailTestResult"];
export type AdminRegistrationSettings = Schemas["AdminRegistrationSettings"];
export type AdminUser = Schemas["AdminUser"];
export type AdminProject = Schemas["AdminProject"];
export type AdminUserList = Schemas["AdminUserList"];
export type AdminUserSummary = Schemas["AdminUserSummary"];
export type AdminUserSummaryList = Schemas["AdminUserSummaryList"];
export type AdminUserDetail = Schemas["AdminUserDetail"];
export type AdminUserMembership = Schemas["AdminUserMembership"];
export type AdminMember = Schemas["AdminMember"];
export type MembershipRole = Schemas["MembershipRole"];
export type Organization = Schemas["Organization"];
export type Member = Schemas["Member"];
export type Invitation = Schemas["Invitation"];
export type InvitationRole = Schemas["InvitationRole"];
export type InvitationCreated = Schemas["InvitationCreated"];
export type InvitationPreview = Schemas["InvitationPreview"];
export type Project = Schemas["Project"];
export type ProjectMember = Schemas["ProjectMember"];
export type ProjectRole = Schemas["ProjectRole"];
export type StatusPage = Schemas["StatusPage"];
export type StatusPageCreate = Schemas["StatusPageCreate"];
export type StatusPageUpdate = Schemas["StatusPageUpdate"];
export type StatusPageComponent = Schemas["StatusPageComponent"];
export type StatusPageComponentInput = Schemas["StatusPageComponentInput"];
export type StatusPageSnapshot = Schemas["StatusPageSnapshot"];
export type OAuthProvider = Schemas["OAuthProvider"];
export type AuthProviders = Schemas["AuthProviders"];
export type Identity = Schemas["Identity"];
export type IdentityList = Schemas["IdentityList"];
export type AuthDomain = Schemas["AuthDomain"];
export type AuthDomainRole = Schemas["AuthDomainRole"];
export type AuthDomainUpdate = Schemas["AuthDomainUpdate"];

/** Page size of the admin lists (the API default; at most 100). */
export const ADMIN_PAGE_SIZE = 25;

/** Dashboard refresh interval. TanStack Query pauses it while the tab is hidden. */
export const POLL_MS = 15_000;

export const queryKeys = {
  me: ["me"] as const,
  monitors: ["monitors"] as const,
  monitorList: (projectId: string | undefined) => ["monitors", "list", projectId ?? "all"] as const,
  monitor: (id: string) => ["monitors", id] as const,
  stats: (id: string, range: StatsRange) => ["monitors", id, "stats", range] as const,
  checks: (id: string) => ["monitors", id, "checks"] as const,
  incidents: (id: string) => ["monitors", id, "incidents"] as const,
  channels: ["channels"] as const,
  channelsFor: (projectId: string | undefined) => ["channels", "for", projectId ?? "all"] as const,
  projects: ["projects"] as const,
  projectMembers: (id: string) => ["projects", id, "members"] as const,
  projectInvitations: (id: string) => ["projects", id, "invitations"] as const,
  usage: ["usage"] as const,
  permissions: ["permissions"] as const,
  statusPages: ["status-pages"] as const,
  statusPageComponents: (id: string) => ["status-pages", id, "components"] as const,
  statusPagePreview: (id: string) => ["status-pages", id, "preview"] as const,
  publicConfig: ["public-config"] as const,
  authProviders: ["auth-providers"] as const,
  identities: ["identities"] as const,
  authDomains: ["auth-domains"] as const,
  adminStats: ["admin", "stats"] as const,
  adminEmail: ["admin", "email"] as const,
  adminRegistration: ["admin", "registration"] as const,
  adminOrganizations: (query: string, offset: number, includeDeleted: boolean) =>
    ["admin", "organizations", { query, offset, includeDeleted }] as const,
  adminOrganization: (id: string) => ["admin", "organization", id] as const,
  adminProjects: (id: string) => ["admin", "organization", id, "projects"] as const,
  adminProjectMembers: (id: string, projectId: string) =>
    ["admin", "organization", id, "projects", projectId, "members"] as const,
  adminUsers: (id: string) => ["admin", "organization", id, "users"] as const,
  adminAllUsers: (query: string, status: string, offset: number) =>
    ["admin", "all-users", { query, status, offset }] as const,
  adminUser: (id: string) => ["admin", "user", id] as const,
  adminMembers: (id: string) => ["admin", "organization", id, "members"] as const,
  adminInvitations: (id: string) => ["admin", "organization", id, "invitations"] as const,
  organizations: ["organizations"] as const,
  members: ["members"] as const,
  invitations: ["invitations"] as const,
  invitationPreview: (token: string) => ["invitation-preview", token] as const,
};

export async function login(login: string, password: string): Promise<SessionInfo> {
  const session = await unwrap(client.POST("/api/v1/auth/login", { body: { login, password } }));
  setCsrfToken(session.csrfToken);
  return session;
}

/**
 * Registers a new account and organization; the answer is a session, exactly like login. With an
 * invitation token the account joins the inviting organization instead.
 */
export async function register(
  email: string,
  password: string,
  inviteToken?: string,
  displayName?: string,
): Promise<SessionInfo> {
  const name = displayName?.trim();
  const body = {
    email,
    password,
    ...(inviteToken ? { inviteToken } : {}),
    ...(name ? { displayName: name } : {}),
  };
  const session = await unwrap(client.POST("/api/v1/auth/register", { body }));
  setCsrfToken(session.csrfToken);
  return session;
}

/**
 * Changes the signed-in user's password. The server ends every session and answers with a new one
 * in the same organization (new cookie, new CSRF token).
 */
export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<SessionInfo> {
  const session = await unwrap(
    client.POST("/api/v1/auth/password", { body: { currentPassword, newPassword } }),
  );
  setCsrfToken(session.csrfToken);
  return session;
}

/** Settings the app needs before login (no session). */
export const getPublicConfig = () => unwrap(client.GET("/api/v1/public/config"));

/** The current session, or null when not logged in. */
export async function getMe(): Promise<SessionInfo | null> {
  try {
    const session = await unwrap(client.GET("/api/v1/auth/me"));
    setCsrfToken(session.csrfToken);
    return session;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      setCsrfToken(null);
      return null;
    }
    throw err;
  }
}

export async function logout(): Promise<void> {
  try {
    await unwrap(client.POST("/api/v1/auth/logout"));
  } finally {
    setCsrfToken(null);
  }
}

/** The monitors the user sees; with a project id only that project's. */
export const listMonitors = (projectId?: string) =>
  unwrap(client.GET("/api/v1/monitors", { params: { query: projectId ? { projectId } : {} } }));

const monitorPath = (monitorId: string) => ({ params: { path: { monitorId } } });

export const getMonitor = (monitorId: string) =>
  unwrap(client.GET("/api/v1/monitors/{monitorId}", monitorPath(monitorId)));

export const createMonitor = (body: MonitorCreate) =>
  unwrap(client.POST("/api/v1/monitors", { body }));

export const updateMonitor = (monitorId: string, body: MonitorUpdate) =>
  unwrap(client.PATCH("/api/v1/monitors/{monitorId}", { ...monitorPath(monitorId), body }));

export const deleteMonitor = (monitorId: string) =>
  unwrap(client.DELETE("/api/v1/monitors/{monitorId}", monitorPath(monitorId)));

export const pauseMonitor = (monitorId: string) =>
  unwrap(client.POST("/api/v1/monitors/{monitorId}/pause", monitorPath(monitorId)));

export const resumeMonitor = (monitorId: string) =>
  unwrap(client.POST("/api/v1/monitors/{monitorId}/resume", monitorPath(monitorId)));

/** A new secret URL for a heartbeat; the old one stops working. Shown once. */
export const regenerateHeartbeatToken = (monitorId: string) =>
  unwrap(client.POST("/api/v1/monitors/{monitorId}/heartbeat-token", monitorPath(monitorId)));

export const getMonitorStats = (monitorId: string, range: StatsRange) =>
  unwrap(
    client.GET("/api/v1/monitors/{monitorId}/stats", {
      params: { path: { monitorId }, query: { range } },
    }),
  );

export const listChecks = (monitorId: string, limit = 20) =>
  unwrap(
    client.GET("/api/v1/monitors/{monitorId}/checks", {
      params: { path: { monitorId }, query: { limit } },
    }),
  );

export const listIncidents = (monitorId: string) =>
  unwrap(client.GET("/api/v1/monitors/{monitorId}/incidents", monitorPath(monitorId)));

/** The channels the user sees; with a project id, those a monitor of that project may select. */
export const listChannels = (projectId?: string) =>
  unwrap(client.GET("/api/v1/channels", { params: { query: projectId ? { projectId } : {} } }));

const channelPath = (channelId: string) => ({ params: { path: { channelId } } });

export const createChannel = (body: ChannelCreate) =>
  unwrap(client.POST("/api/v1/channels", { body }));

export const updateChannel = (channelId: string, body: ChannelUpdate) =>
  unwrap(client.PATCH("/api/v1/channels/{channelId}", { ...channelPath(channelId), body }));

export const deleteChannel = (channelId: string) =>
  unwrap(client.DELETE("/api/v1/channels/{channelId}", channelPath(channelId)));

export const previewChannelTemplate = (body: ChannelTemplatePreviewRequest) =>
  unwrap(client.POST("/api/v1/channels/preview-template", { body }));

export const testChannel = (channelId: string) =>
  unwrap(client.POST("/api/v1/channels/{channelId}/test", channelPath(channelId)));

export const getUsage = () => unwrap(client.GET("/api/v1/usage"));

export const getPermissions = () => unwrap(client.GET("/api/v1/permissions"));

// Platform admin panel (experimental API). Every call answers 403 to anybody but the platform
// admin.

export const getAdminStats = () => unwrap(client.GET("/api/v1/admin/stats"));

export const listAdminOrganizations = (query: string, offset: number, includeDeleted: boolean) =>
  unwrap(
    client.GET("/api/v1/admin/organizations", {
      params: { query: { query, offset, limit: ADMIN_PAGE_SIZE, includeDeleted } },
    }),
  );

const organizationPath = (organizationId: string) => ({ params: { path: { organizationId } } });

export const getAdminOrganization = (organizationId: string) =>
  unwrap(
    client.GET("/api/v1/admin/organizations/{organizationId}", organizationPath(organizationId)),
  );

export const updateAdminOrganization = (organizationId: string, body: AdminOrganizationUpdate) =>
  unwrap(
    client.PATCH("/api/v1/admin/organizations/{organizationId}", {
      ...organizationPath(organizationId),
      body,
    }),
  );

export const deleteAdminOrganization = (organizationId: string) =>
  unwrap(
    client.DELETE("/api/v1/admin/organizations/{organizationId}", organizationPath(organizationId)),
  );

/** The members of an organization (the first page is enough for the panel). */
export const listAdminOrganizationUsers = (organizationId: string) =>
  unwrap(
    client.GET("/api/v1/admin/organizations/{organizationId}/users", {
      params: { path: { organizationId }, query: { limit: 100 } },
    }),
  );

export const listAdminMembers = (organizationId: string) =>
  unwrap(
    client.GET("/api/v1/admin/organizations/{organizationId}/members", {
      params: { path: { organizationId } },
    }),
  );

/** Adds an existing user to the organization or changes their role (any of the four roles). */
export const setAdminMember = (
  organizationId: string,
  userId: string,
  role: MembershipRole | null,
) =>
  unwrap(
    client.PUT("/api/v1/admin/organizations/{organizationId}/members/{userId}", {
      params: { path: { organizationId, userId } },
      body: { role },
    }),
  );

export const removeAdminMember = (organizationId: string, userId: string) =>
  unwrap(
    client.DELETE("/api/v1/admin/organizations/{organizationId}/members/{userId}", {
      params: { path: { organizationId, userId } },
    }),
  );

export const listAdminInvitations = (organizationId: string) =>
  unwrap(
    client.GET("/api/v1/admin/organizations/{organizationId}/invitations", {
      params: { path: { organizationId } },
    }),
  );

/** A new invitation; its link is in this answer only. */
export const createAdminInvitation = (
  organizationId: string,
  role: InvitationRole,
  label: string,
  email = "",
) =>
  unwrap(
    client.POST("/api/v1/admin/organizations/{organizationId}/invitations", {
      params: { path: { organizationId } },
      body: {
        role,
        ...(label ? { label } : {}),
        ...(email ? { email, sendEmail: true } : {}),
      },
    }),
  );

export const revokeAdminInvitation = (organizationId: string, invitationId: string) =>
  unwrap(
    client.POST("/api/v1/admin/organizations/{organizationId}/invitations/{invitationId}/revoke", {
      params: { path: { organizationId, invitationId } },
    }),
  );

/** A new temporary password for the user, returned once; all of the user's sessions end. */
export const resetAdminUserPassword = (userId: string) =>
  unwrap(
    client.POST("/api/v1/admin/users/{userId}/reset-password", {
      params: { path: { userId } },
    }),
  );

export type AdminUserStatusFilter = "active" | "disabled";

export const listAdminAllUsers = (
  query: string,
  status: AdminUserStatusFilter | "",
  offset: number,
) =>
  unwrap(
    client.GET("/api/v1/admin/users", {
      params: {
        query: { query, offset, limit: ADMIN_PAGE_SIZE, ...(status ? { status } : {}) },
      },
    }),
  );

const userPath = (userId: string) => ({ params: { path: { userId } } });

/** One user with every membership and the number of open sessions. */
export const getAdminUser = (userId: string) =>
  unwrap(client.GET("/api/v1/admin/users/{userId}", userPath(userId)));

/** Refuses sign-in and deletes all of the user's sessions. */
export const disableAdminUser = (userId: string) =>
  unwrap(client.POST("/api/v1/admin/users/{userId}/disable", userPath(userId)));

export const enableAdminUser = (userId: string) =>
  unwrap(client.POST("/api/v1/admin/users/{userId}/enable", userPath(userId)));

/** Grants or revokes the platform-admin flag; answers the user afterwards. */
export const setAdminPlatformAdmin = (userId: string, enabled: boolean) =>
  unwrap(
    client.PUT("/api/v1/admin/users/{userId}/platform-admin", {
      params: { path: { userId } },
      body: { enabled },
    }),
  );

/** Deletes all of the user's sessions; answers how many were open. */
export const revokeAdminUserSessions = (userId: string) =>
  unwrap(client.POST("/api/v1/admin/users/{userId}/sessions/revoke", userPath(userId)));

export const getAdminEmailSettings = () => unwrap(client.GET("/api/v1/admin/email"));

/** Saves the SMTP connection. The password is write-only: leave it out to keep the stored one. */
export const updateAdminEmailSettings = (body: AdminEmailConnectionUpdate) =>
  unwrap(client.PUT("/api/v1/admin/email", { body }));

export const deleteAdminEmailSettings = () => unwrap(client.DELETE("/api/v1/admin/email"));

export const updateAdminEmailFeatures = (body: AdminEmailFeaturesUpdate) =>
  unwrap(client.PUT("/api/v1/admin/email/features", { body }));

export const getAdminRegistration = () => unwrap(client.GET("/api/v1/admin/registration"));

/** `true` or `false` is the platform admin's choice; `null` follows the installation setting again. */
export const updateAdminRegistration = (enabled: boolean | null) =>
  unwrap(client.PUT("/api/v1/admin/registration", { body: { enabled } }));

/** Queues a real test message; poll getAdminEmailSettings for the result. */
export const sendAdminTestEmail = (to: string) =>
  unwrap(client.POST("/api/v1/admin/email/test", { body: to ? { to } : {} }));

// Organizations, members and invitations.

export const listOrganizations = () => unwrap(client.GET("/api/v1/auth/organizations"));

/** Replaces the session with one in another organization (new cookie and CSRF token). */
export async function switchOrganization(organizationId: string): Promise<SessionInfo> {
  const session = await unwrap(
    client.POST("/api/v1/auth/switch-organization", { body: { organizationId } }),
  );
  setCsrfToken(session.csrfToken);
  return session;
}

export const listMembers = () => unwrap(client.GET("/api/v1/members"));

const memberPath = (userId: string) => ({ params: { path: { userId } } });

/** Gives a member another organization role; null leaves them with their projects only. */
export const updateMember = (userId: string, role: MembershipRole | null) =>
  unwrap(client.PATCH("/api/v1/members/{userId}", { ...memberPath(userId), body: { role } }));

export const removeMember = (userId: string) =>
  unwrap(client.DELETE("/api/v1/members/{userId}", memberPath(userId)));

/** The invitations the caller may see; with a project id only those into that project. */
export const listInvitations = (projectId?: string) =>
  unwrap(
    client.GET("/api/v1/invitations", {
      ...(projectId ? { params: { query: { projectId } } } : {}),
    }),
  );

/**
 * A new invitation; its link is in this answer only. With a project id it invites into that
 * project and the role is the role there.
 */
export const createInvitation = (
  role: InvitationRole,
  label: string,
  email = "",
  projectId?: string,
) =>
  unwrap(
    client.POST("/api/v1/invitations", {
      // An address is only sent when the box "Send by email" is checked, so it always asks for
      // the email explicitly.
      body: {
        role,
        ...(projectId ? { projectId } : {}),
        ...(label ? { label } : {}),
        ...(email ? { email, sendEmail: true } : {}),
      },
    }),
  );

export const revokeInvitation = (invitationId: string) =>
  unwrap(
    client.POST("/api/v1/invitations/{invitationId}/revoke", {
      params: { path: { invitationId } },
    }),
  );

/** Confirms an address with the token of the emailed link; works without a session. */
export const verifyEmail = (token: string) =>
  unwrap(client.POST("/api/v1/auth/verify-email", { body: { token } }));

/** Emails a reset link if the address has an account; the answer is the same either way. */
export const requestPasswordReset = (email: string) =>
  unwrap(client.POST("/api/v1/auth/forgot-password", { body: { email } }));

/** Sets a new password with the token of the emailed link; ends every session of the account. */
export const resetPassword = (token: string, password: string) =>
  unwrap(client.POST("/api/v1/auth/reset-password", { body: { token, password } }));

/** Asks for a new verification email for the signed-in user. */
export const resendEmailVerification = () =>
  unwrap(client.POST("/api/v1/auth/resend-verification"));

/** What an invitation link offers; works without a session. */
export const lookupInvitation = (token: string) =>
  unwrap(client.POST("/api/v1/invitations/lookup", { body: { token } }));

/** Joins the inviting organization as the signed-in user; the session moves there. */
export async function acceptInvitation(token: string): Promise<SessionInfo> {
  const session = await unwrap(client.POST("/api/v1/invitations/accept", { body: { token } }));
  setCsrfToken(session.csrfToken);
  return session;
}

export async function leaveOrganization(): Promise<void> {
  try {
    await unwrap(client.POST("/api/v1/organization/leave"));
  } finally {
    setCsrfToken(null);
  }
}

export const deleteOrganization = () => unwrap(client.DELETE("/api/v1/organization"));

// Projects.

export const listProjects = () => unwrap(client.GET("/api/v1/projects"));

const projectPath = (projectId: string) => ({ params: { path: { projectId } } });

export const createProject = (name: string) =>
  unwrap(client.POST("/api/v1/projects", { body: { name } }));

export const renameProject = (projectId: string, name: string) =>
  unwrap(
    client.PATCH("/api/v1/projects/{projectId}", { ...projectPath(projectId), body: { name } }),
  );

export const deleteProject = (projectId: string) =>
  unwrap(client.DELETE("/api/v1/projects/{projectId}", projectPath(projectId)));

export const listProjectMembers = (projectId: string) =>
  unwrap(client.GET("/api/v1/projects/{projectId}/members", projectPath(projectId)));

/** Gives a member of the organization a direct role in a project (never lowers its inherited role). */
export const putProjectMember = (projectId: string, userId: string, role: ProjectRole) =>
  unwrap(
    client.PUT("/api/v1/projects/{projectId}/members/{userId}", {
      params: { path: { projectId, userId } },
      body: { role },
    }),
  );

export const removeProjectMember = (projectId: string, userId: string) =>
  unwrap(
    client.DELETE("/api/v1/projects/{projectId}/members/{userId}", {
      params: { path: { projectId, userId } },
    }),
  );

// Status page. Every role reads the settings; maintainers and owners change them.

export const listStatusPages = () => unwrap(client.GET("/api/v1/status-pages"));

const statusPagePath = (statusPageId: string) => ({ params: { path: { statusPageId } } });

export const createStatusPage = (body: StatusPageCreate) =>
  unwrap(client.POST("/api/v1/status-pages", { body }));

export const updateStatusPage = (statusPageId: string, body: StatusPageUpdate) =>
  unwrap(
    client.PATCH("/api/v1/status-pages/{statusPageId}", { ...statusPagePath(statusPageId), body }),
  );

export const deleteStatusPage = (statusPageId: string) =>
  unwrap(client.DELETE("/api/v1/status-pages/{statusPageId}", statusPagePath(statusPageId)));

/** Write-only: the password is never returned. */
export const setStatusPagePassword = (statusPageId: string, password: string) =>
  unwrap(
    client.PUT("/api/v1/status-pages/{statusPageId}/password", {
      ...statusPagePath(statusPageId),
      body: { password },
    }),
  );

export const clearStatusPagePassword = (statusPageId: string) =>
  unwrap(
    client.DELETE("/api/v1/status-pages/{statusPageId}/password", statusPagePath(statusPageId)),
  );

export const listStatusPageComponents = (statusPageId: string) =>
  unwrap(
    client.GET("/api/v1/status-pages/{statusPageId}/components", statusPagePath(statusPageId)),
  );

/** Replaces the components: the list is the new content and order of the page. */
export const putStatusPageComponents = (statusPageId: string, items: StatusPageComponentInput[]) =>
  unwrap(
    client.PUT("/api/v1/status-pages/{statusPageId}/components", {
      ...statusPagePath(statusPageId),
      body: { items },
    }),
  );

/** The public view as /status/<slug>.json would serve it now, also while unpublished. */
export const previewStatusPage = (statusPageId: string) =>
  unwrap(client.GET("/api/v1/status-pages/{statusPageId}/preview", statusPagePath(statusPageId)));

// OAuth sign-in.

/** The providers the login page offers and whether the password form is on (no session). */
export const getAuthProviders = () => unwrap(client.GET("/api/v1/auth/providers"));

/**
 * Starts a sign-in or link at a provider and returns the URL to navigate the window to. An
 * invitation token goes in this body only, never in a URL; a link sends the CSRF token (apiFetch).
 */
export const startOAuth = (
  provider: OAuthProvider,
  body: { intent?: "login" | "link"; inviteToken?: string },
) =>
  unwrap(
    client.POST("/api/v1/auth/oauth/{provider}/start", { params: { path: { provider } }, body }),
  );

export const listIdentities = () => unwrap(client.GET("/api/v1/auth/identities"));

export const unlinkIdentity = (provider: OAuthProvider) =>
  unwrap(client.DELETE("/api/v1/auth/identities/{provider}", { params: { path: { provider } } }));

export const listAuthDomains = () => unwrap(client.GET("/api/v1/organization/auth-domains"));

/** Claims a Workspace domain; it always starts with the viewer role. */
export const createAuthDomain = (domain: string) =>
  unwrap(client.POST("/api/v1/organization/auth-domains", { body: { domain } }));

export const updateAuthDomain = (domainId: string, body: AuthDomainUpdate) =>
  unwrap(
    client.PATCH("/api/v1/organization/auth-domains/{domainId}", {
      params: { path: { domainId } },
      body,
    }),
  );

export const deleteAuthDomain = (domainId: string) =>
  unwrap(
    client.DELETE("/api/v1/organization/auth-domains/{domainId}", {
      params: { path: { domainId } },
    }),
  );

const adminProjectPath = (organizationId: string, projectId: string) => ({
  params: { path: { organizationId, projectId } },
});

export const listAdminProjects = (organizationId: string) =>
  unwrap(
    client.GET("/api/v1/admin/organizations/{organizationId}/projects", {
      params: { path: { organizationId } },
    }),
  );

export const createAdminProject = (organizationId: string, name: string) =>
  unwrap(
    client.POST("/api/v1/admin/organizations/{organizationId}/projects", {
      params: { path: { organizationId } },
      body: { name },
    }),
  );

export const renameAdminProject = (organizationId: string, projectId: string, name: string) =>
  unwrap(
    client.PATCH("/api/v1/admin/organizations/{organizationId}/projects/{projectId}", {
      ...adminProjectPath(organizationId, projectId),
      body: { name },
    }),
  );

export const deleteAdminProject = (organizationId: string, projectId: string) =>
  unwrap(
    client.DELETE(
      "/api/v1/admin/organizations/{organizationId}/projects/{projectId}",
      adminProjectPath(organizationId, projectId),
    ),
  );

export const listAdminProjectMembers = (organizationId: string, projectId: string) =>
  unwrap(
    client.GET(
      "/api/v1/admin/organizations/{organizationId}/projects/{projectId}/members",
      adminProjectPath(organizationId, projectId),
    ),
  );

/** Gives a member of the organization a direct role in a project. */
export const putAdminProjectMember = (
  organizationId: string,
  projectId: string,
  userId: string,
  role: ProjectRole,
) =>
  unwrap(
    client.PUT(
      "/api/v1/admin/organizations/{organizationId}/projects/{projectId}/members/{userId}",
      { params: { path: { organizationId, projectId, userId } }, body: { role } },
    ),
  );

export const removeAdminProjectMember = (
  organizationId: string,
  projectId: string,
  userId: string,
) =>
  unwrap(
    client.DELETE(
      "/api/v1/admin/organizations/{organizationId}/projects/{projectId}/members/{userId}",
      { params: { path: { organizationId, projectId, userId } } },
    ),
  );
