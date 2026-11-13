import { useQueryClient, type InvalidateQueryFilters, type QueryKey } from "@tanstack/react-query";
import {
  createAdminProject,
  createProject,
  deleteAdminProject,
  deleteProject,
  listAdminMembers,
  listAdminProjectMembers,
  listAdminProjects,
  listMembers,
  listProjectMembers,
  listProjects,
  putAdminProjectMember,
  putProjectMember,
  queryKeys,
  removeAdminMember,
  removeAdminProjectMember,
  removeMember,
  removeProjectMember,
  renameAdminProject,
  renameProject,
  setAdminMember,
  updateMember,
  type AdminMember,
  type AdminOrganization,
  type AdminProject,
  type Member,
  type MembershipRole,
  type Project,
  type ProjectMember,
  type ProjectRole,
} from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import { usePermissions } from "./permissions";
import { assignableRoles, canManageMember, ROLES } from "./roles";

export type Person = Member | AdminMember;
export type PeopleProject = Project | AdminProject;

/**
 * The people and projects of one organization, through the organization's own API (the members
 * and projects pages) or the platform admin's API (an organization in the Admin Area). The tables
 * in components/PeopleTable.tsx and components/ProjectsPanel.tsx take one of these; the server decides
 * again on every change.
 */
export interface PeopleApi {
  admin: boolean;
  keys: { members: QueryKey; projects: QueryKey; projectMembers: (projectId: string) => QueryKey };
  /** Refetched after any change, besides the keys above. */
  related: InvalidateQueryFilters[];
  listMembers: () => Promise<{ items: Person[] }>;
  setRole: (userId: string, role: MembershipRole | null) => Promise<Person>;
  removeMember: (userId: string) => Promise<unknown>;
  listProjects: () => Promise<{ items: PeopleProject[] }>;
  createProject: (name: string) => Promise<PeopleProject>;
  renameProject: (projectId: string, name: string) => Promise<PeopleProject>;
  deleteProject: (projectId: string) => Promise<unknown>;
  listProjectMembers: (projectId: string) => Promise<{ items: ProjectMember[] }>;
  putProjectMember: (
    projectId: string,
    userId: string,
    role: ProjectRole,
  ) => Promise<ProjectMember>;
  removeProjectMember: (projectId: string, userId: string) => Promise<unknown>;
  /** The organization roles the actor may give this person; empty when they may not change it. */
  orgRolesFor: (m: Person) => MembershipRole[];
  /** Why the organization role of this person is not editable here, when it is worth saying. */
  lockedNote: (m: Person) => string | undefined;
  canRemove: (m: Person) => boolean;
  /** Whether the organization member list is readable (to add someone to a project). */
  canReadMembers: boolean;
  canCreateProject: boolean;
  canRename: (p: PeopleProject) => boolean;
  canDelete: (p: PeopleProject) => boolean;
  /** The project roles the actor may give someone whose effective role there is `target`. */
  projectRolesFor: (p: PeopleProject, target: MembershipRole | null) => ProjectRole[];
}

/** The organization of the session, with what the signed-in person may do in it. */
export function useOrganizationPeople(): PeopleApi {
  const auth = useAuth();
  const can = usePermissions();
  const me = auth.session?.user.id;
  const actor = auth.session?.role;
  const orgRolesFor = (m: Person) =>
    canManageMember(actor, m.role) && !m.isSystemAdmin ? assignableRoles(actor) : [];
  const projectRole = (p: PeopleProject) => ("role" in p ? p.role : undefined);
  return {
    admin: false,
    keys: {
      members: queryKeys.members,
      projects: queryKeys.projects,
      projectMembers: queryKeys.projectMembers,
    },
    related: [{ queryKey: queryKeys.permissions }, { queryKey: queryKeys.usage }],
    listMembers,
    setRole: updateMember,
    removeMember,
    listProjects,
    createProject,
    renameProject,
    deleteProject,
    listProjectMembers,
    putProjectMember,
    removeProjectMember,
    orgRolesFor,
    lockedNote: (m) =>
      m.isSystemAdmin
        ? "The system administrator; set by the deployment."
        : canManageMember(actor, "viewer") && !canManageMember(actor, m.role)
          ? "Only an owner can change an owner."
          : undefined,
    canRemove: (m) => orgRolesFor(m).length > 0 && m.userId !== me,
    canReadMembers: can.org("organization.members.read"),
    canCreateProject: can.org("project.create"),
    canRename: (p) => can.inProject(p.id, "project.update"),
    canDelete: (p) => can.inProject(p.id, "project.delete"),
    // Nobody grants above their own role in the project, and maintainers never touch owners there.
    projectRolesFor: (p, target) =>
      can.inProject(p.id, "project.members.manage") && canManageMember(projectRole(p), target)
        ? assignableRoles(projectRole(p))
        : [],
  };
}

/** One organization in the Admin Area: the platform admin acts as its owner. */
export function adminPeople(organization: AdminOrganization): PeopleApi {
  const id = organization.id;
  const readOnly = organization.deletedAt !== null;
  const roles = readOnly ? [] : ROLES;
  return {
    admin: true,
    keys: {
      members: queryKeys.adminMembers(id),
      projects: queryKeys.adminProjects(id),
      projectMembers: (projectId) => queryKeys.adminProjectMembers(id, projectId),
    },
    // Exact: the organization key is also the prefix of its members and projects.
    related: [
      { queryKey: queryKeys.adminOrganization(id), exact: true },
      { queryKey: ["admin", "organizations"] },
    ],
    listMembers: () => listAdminMembers(id),
    setRole: (userId, role) => setAdminMember(id, userId, role),
    removeMember: (userId) => removeAdminMember(id, userId),
    listProjects: () => listAdminProjects(id),
    createProject: (name) => createAdminProject(id, name),
    renameProject: (projectId, name) => renameAdminProject(id, projectId, name),
    deleteProject: (projectId) => deleteAdminProject(id, projectId),
    listProjectMembers: (projectId) => listAdminProjectMembers(id, projectId),
    putProjectMember: (projectId, userId, role) =>
      putAdminProjectMember(id, projectId, userId, role),
    removeProjectMember: (projectId, userId) => removeAdminProjectMember(id, projectId, userId),
    orgRolesFor: (m) => (m.isSystemAdmin ? [] : roles),
    lockedNote: (m) =>
      m.isSystemAdmin ? "Managed by the proreta-admin Secret, not here." : undefined,
    canRemove: (m) => !readOnly && !m.isSystemAdmin,
    canReadMembers: true,
    canCreateProject: !readOnly,
    canRename: () => !readOnly,
    canDelete: () => !readOnly,
    projectRolesFor: () => roles,
  };
}

/**
 * Refetches what a change touched; only the queries on screen go to the server, and the promise
 * ends when they are back. Without a project: the people and every project with its people (the
 * projects key is their prefix), after an organization role changed. With one: the project list
 * and that project's people only.
 */
export function useRefreshPeople(api: PeopleApi): (projectId?: string) => Promise<unknown> {
  const queryClient = useQueryClient();
  return (projectId) => {
    const changed: InvalidateQueryFilters[] = projectId
      ? [
          { queryKey: api.keys.projects, exact: true },
          { queryKey: api.keys.projectMembers(projectId) },
        ]
      : [{ queryKey: api.keys.members }, { queryKey: api.keys.projects }];
    return Promise.all(
      [...changed, ...api.related].map((filters) => queryClient.invalidateQueries(filters)),
    );
  };
}
