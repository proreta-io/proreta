import type { MembershipRole, Project, SessionInfo } from "../api/endpoints";

/**
 * Roles exist on the organization and on each project, the same four at both levels; the role in a
 * project is the higher of the two. A person may have no organization role (null) and belong to
 * the organization only through projects. The server decides (internal/authz); the UI only hides
 * or disables what the server would refuse, and says why.
 */
export type OptionalRole = MembershipRole | null | undefined;

const RANK: Record<MembershipRole, number> = { owner: 4, maintainer: 3, member: 2, viewer: 1 };

export const ROLE_LABELS: Record<MembershipRole, string> = {
  owner: "Owner",
  maintainer: "Maintainer",
  member: "Member",
  viewer: "Viewer",
};

/** The option that takes the organization role away. */
export const NO_ROLE_LABEL = "No organization role (projects only)";

/** What a list shows for a person without an organization role. */
export const PROJECTS_ONLY_LABEL = "Projects only";

/** The label of a role that may be missing. */
export function roleLabel(role: OptionalRole): string {
  return role ? ROLE_LABELS[role] : PROJECTS_ONLY_LABEL;
}

/** What each role may do on the organization; a project role gives the project parts only. */
export const ROLE_DESCRIPTIONS: Record<MembershipRole, string> = {
  owner:
    "Everything, including deleting projects and the organization and granting owner. In a project: deleting it and granting owner.",
  maintainer:
    "Members, invitations, projects, notification channels, the status page and every monitor. In a project: its people, channels and settings.",
  member: "Creates, edits, pauses and deletes monitors.",
  viewer: "Reads monitors, incidents and channels only.",
};

export function roleAtLeast(role: OptionalRole, min: MembershipRole): boolean {
  return role !== undefined && role !== null && RANK[role] >= RANK[min];
}

/** What a session may do in the UI; mirrors the permissions of the API. */
export interface Abilities {
  /** The organization role; null for a person who belongs only through projects. */
  role: OptionalRole;
  /** Whether the person has an organization role at all. */
  hasOrganizationRole: boolean;
  /** Creates or edits monitors in the organization or in at least one project. */
  editMonitors: boolean;
  /** Manages channels in the organization or in at least one project. */
  manageChannels: boolean;
  /** Organization members and invitations. */
  manageMembers: boolean;
  deleteOrganization: boolean;
  /** Manages status pages of the organization or of at least one project. */
  manageStatusPage: boolean;
  /** Organization-wide plan usage. */
  seeUsage: boolean;
}

/**
 * The abilities of a session. With the projects of the user (their role there is the effective
 * role) the project-level abilities also hold when a project role grants them; the server checks
 * the concrete project again.
 */
export function abilities(
  session: SessionInfo | null | undefined,
  projects?: Pick<Project, "role">[],
): Abilities {
  const role = session?.role;
  const inAProject = (min: MembershipRole) =>
    (projects ?? []).some((p) => roleAtLeast(p.role, min));
  return {
    role,
    hasOrganizationRole: role !== undefined && role !== null,
    editMonitors: roleAtLeast(role, "member") || inAProject("member"),
    manageChannels: roleAtLeast(role, "maintainer") || inAProject("maintainer"),
    manageMembers: roleAtLeast(role, "maintainer"),
    deleteOrganization: role === "owner",
    manageStatusPage: roleAtLeast(role, "maintainer") || inAProject("maintainer"),
    seeUsage: roleAtLeast(role, "maintainer"),
  };
}

/**
 * Whether the session may change channels and status pages owned by a project, or by the
 * organization when projectId is empty: the organization role decides for the latter, the role in
 * the project (the higher of the two) for the former.
 */
export function canManageOwned(
  session: SessionInfo | null | undefined,
  projects: Pick<Project, "id" | "role">[] | undefined,
  projectId: string | null | undefined,
): boolean {
  if (roleAtLeast(session?.role, "maintainer")) return true;
  if (!projectId) return false;
  return roleAtLeast(projects?.find((p) => p.id === projectId)?.role, "maintainer");
}

/** The roles an actor may give someone (nobody grants above their own; owner only by an owner). */
export function assignableRoles(actor: OptionalRole): MembershipRole[] {
  if (!roleAtLeast(actor, "maintainer")) return [];
  return (["owner", "maintainer", "member", "viewer"] as const).filter(
    (r) => RANK[r] <= RANK[actor as MembershipRole],
  );
}

/**
 * Whether the actor may change or remove a person whose role is target (an organization role, or
 * the effective role in a project; null for none): maintainers never touch owners.
 */
export function canManageMember(actor: OptionalRole, target: OptionalRole): boolean {
  return (
    roleAtLeast(actor, "maintainer") &&
    (target === null || target === undefined || RANK[target] <= RANK[actor as MembershipRole])
  );
}

/** The friendly reason shown next to a control the role does not allow. */
export const ROLE_HINTS = {
  editMonitors: "Viewers can look but not change monitors. Ask a maintainer for the member role.",
  manageChannels: "Only maintainers and owners can change notification channels.",
  manageMembers: "Only maintainers and owners can invite people or change roles.",
  manageStatusPage:
    "Only maintainers and owners of the organization or of the page's project can change or publish a status page.",
  organizationLevel:
    "You belong to this organization through projects only. Organization settings are for people with an organization role.",
} as const;
