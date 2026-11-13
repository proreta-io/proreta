import type { MembershipRole } from "../api/endpoints";

/**
 * Roles exist on the organization and on each project, the same four at both levels; the role in a
 * project is the higher of the two. A person may have no organization role (null) and belong to
 * the organization only through projects. The server decides (internal/authz); what a person may
 * do comes from lib/permissions.ts. This file keeps the labels and the rules that compare roles
 * (nobody grants or touches a role above their own).
 */
export type OptionalRole = MembershipRole | null | undefined;

export const RANK: Record<MembershipRole, number> = {
  owner: 4,
  maintainer: 3,
  member: 2,
  viewer: 1,
};

/** Every role, highest first. */
export const ROLES: MembershipRole[] = ["owner", "maintainer", "member", "viewer"];

/** The select value of "no organization role" (PROJECTS_ONLY_LABEL). */
export const NO_ROLE = "none";

export const ROLE_LABELS: Record<MembershipRole, string> = {
  owner: "Owner",
  maintainer: "Maintainer",
  member: "Member",
  viewer: "Viewer",
};

/** A person without an organization role, in lists and as the option that takes the role away. */
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

/** The roles an actor may give someone (nobody grants above their own; owner only by an owner). */
export function assignableRoles(actor: OptionalRole): MembershipRole[] {
  if (!roleAtLeast(actor, "maintainer")) return [];
  return ROLES.filter((r) => RANK[r] <= RANK[actor as MembershipRole]);
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
