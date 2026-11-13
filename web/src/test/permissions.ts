import type { MembershipRole, PermissionsInfo } from "../api/endpoints";

/*
 * The role table of the server (internal/authz), written out for the tests and the dev mock API
 * (mock/api.ts). Keep it in step when the server's table changes.
 */
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
