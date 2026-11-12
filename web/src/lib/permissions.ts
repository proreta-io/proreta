import { useQuery } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";
import {
  getPermissions,
  queryKeys,
  type MembershipRole,
  type PermissionDefinition,
  type PermissionsInfo,
  type ProjectPermissions,
} from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";

/**
 * What the signed-in person may do, as the server worked it out (internal/authz): the UI only
 * hides or disables what the server would refuse. A permission held through the organization role
 * is held in every project; a project role holds project permissions in that project only.
 */
export interface Permissions {
  /** False until the server has answered; nothing is granted before that. */
  ready: boolean;
  /** The organization role; null for a person who belongs through projects only. */
  organizationRole: MembershipRole | null;
  /** Held through the organization role (account permissions are held by everybody). */
  org: (permission: string) => boolean;
  /** Held in one project (an organization role holds its project permissions in every project). */
  inProject: (projectId: string, permission: string) => boolean;
  /** Held through the organization role or in at least one project. */
  anywhere: (permission: string) => boolean;
  /** The projects in which the permission is held. */
  projectsWith: (permission: string) => ProjectPermissions[];
  /** Every project the person sees, with the role and permissions there. */
  projects: ProjectPermissions[];
  /** Every permission with its level and the roles that hold it (the help page). */
  catalogue: PermissionDefinition[];
}

export function permissionsFrom(info: PermissionsInfo | undefined): Permissions {
  const org = new Set(info?.organization ?? []);
  const projects = info?.projects ?? [];
  const byId = new Map(projects.map((p) => [p.projectId, new Set(p.permissions)]));
  const projectsWith = (permission: string) =>
    projects.filter((p) => p.permissions.includes(permission));
  return {
    ready: info !== undefined,
    organizationRole: info?.organizationRole ?? null,
    org: (permission) => org.has(permission),
    inProject: (projectId, permission) =>
      org.has(permission) || (byId.get(projectId)?.has(permission) ?? false),
    anywhere: (permission) => org.has(permission) || projectsWith(permission).length > 0,
    projectsWith,
    projects,
    catalogue: info?.catalogue ?? [],
  };
}

/** The permissions of the session; refetched when the organization changes (the app resets its queries) and when roles are edited. */
export function usePermissions(): Permissions {
  const session = useAuth().session;
  const query = useQuery({
    queryKey: queryKeys.permissions,
    queryFn: getPermissions,
    enabled: session !== null && session !== undefined,
    staleTime: 30_000,
  });
  return useMemo(() => permissionsFrom(query.data), [query.data]);
}

/**
 * Renders the children only when the permission is held (nothing at all until the server has
 * answered, so a fallback does not flash): in the project when `project` is given,
 * through the organization role when `organization` is set, otherwise anywhere (organization or
 * at least one project).
 */
export function Can({
  permission,
  project,
  organization,
  fallback = null,
  children,
}: {
  permission: string;
  project?: string | null;
  organization?: boolean;
  fallback?: ReactNode;
  children: ReactNode;
}) {
  const can = usePermissions();
  if (!can.ready) return null;
  const held = organization
    ? can.org(permission)
    : project
      ? can.inProject(project, permission)
      : can.anywhere(permission);
  return held ? children : fallback;
}

type Held = Pick<Permissions, "org" | "inProject">;

/** Whether the person manages a channel of a project, or an organization-wide one when projectId is empty. */
export function canManageChannelOf(can: Held, projectId: string | null | undefined): boolean {
  return projectId ? can.inProject(projectId, "channel.manage") : can.org("channel.manage");
}

/** Whether the person manages a status page of a project, or an organization-level one when projectId is empty. */
export function canManageStatusPageOf(can: Held, projectId: string | null | undefined): boolean {
  return projectId
    ? can.inProject(projectId, "statuspage.manage")
    : can.org("organization.statuspages.manage");
}
