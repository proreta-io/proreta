import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { errorMessage, errorMeta } from "../api/client";
import {
  queryKeys,
  type MembershipRole,
  type ProjectMember,
  type ProjectRole,
} from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import { formatDate } from "../lib/format";
import { memberLabel } from "../lib/members";
import { useRefreshPeople, type PeopleApi, type PeopleProject, type Person } from "../lib/people";
import { NO_ROLE, PROJECTS_ONLY_LABEL, ROLE_LABELS, roleAtLeast, roleLabel } from "../lib/roles";
import { useProjectRole } from "./ProjectsPanel";
import { useToast } from "./Toasts";
import {
  Badge,
  Button,
  ConfirmDialog,
  EmptyNote,
  ErrorState,
  Loading,
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
  cx,
} from "./ui";

const CARD =
  "max-sm:flex max-sm:flex-wrap max-sm:items-center max-sm:gap-x-3 max-sm:border-b max-sm:py-2 max-sm:last:border-b-0 max-sm:[&>td]:border-b-0 max-sm:[&>td]:py-1";

type Pending =
  | { kind: "role"; member: Person; role: MembershipRole | null }
  | { kind: "remove"; member: Person };

/**
 * The people of an organization with their organization role and, under each person, the role in
 * every project: one place to change all of somebody's access.
 */
export default function PeopleTable({
  api,
  organizationName,
  userLink,
  actions,
}: {
  api: PeopleApi;
  organizationName: string;
  /** Where the login links to (the user in the Admin Area). */
  userLink?: (m: Person) => string;
  /**
   * More buttons in a row, before Remove; `describedBy` is the id of the note that says why the
   * person's role is locked, when there is one.
   */
  actions?: (m: Person, describedBy: string | undefined) => ReactNode;
}) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const me = useAuth().session?.user.id;
  const members = useQuery({ queryKey: api.keys.members, queryFn: api.listMembers });
  const projects = useQuery({ queryKey: api.keys.projects, queryFn: api.listProjects });
  const projectItems = projects.data?.items ?? [];
  // shortcut: one request per project; ask the backend for a per-user list if organizations grow
  // beyond a few dozen projects.
  const access = useQueries({
    queries: projectItems.map((p) => ({
      queryKey: api.keys.projectMembers(p.id),
      queryFn: () => api.listProjectMembers(p.id),
      refetchOnWindowFocus: false,
    })),
  });
  const [open, setOpen] = useState<string | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);

  // The projects too: the actor's own role there changes with their organization role.
  const refresh = useRefreshPeople(api);

  const change = useMutation({
    mutationFn: ({ member, role }: { member: Person; role: MembershipRole | null }) =>
      api.setRole(member.userId, role),
    onSuccess: (updated) => {
      setPending(null);
      // The session carries the role in the current organization, also when changed from the
      // Admin Area.
      if (updated.userId === me) void queryClient.invalidateQueries({ queryKey: queryKeys.me });
      toast(
        updated.role
          ? `${memberLabel(updated)} is now ${ROLE_LABELS[updated.role].toLowerCase()}`
          : `${memberLabel(updated)} now belongs through projects only`,
      );
      // Pending until the new roles are back, so the selects cannot offer the old ones.
      return refresh();
    },
    onError: (err) => {
      setPending(null);
      toast(errorMessage(err), "error");
    },
  });

  const remove = useMutation({
    mutationFn: (member: Person) => api.removeMember(member.userId),
    onSuccess: (_, member) => {
      setPending(null);
      toast(`${memberLabel(member)} was removed`);
      return refresh();
    },
    onError: (err) => {
      setPending(null);
      toast(errorMessage(err), "error");
    },
  });

  function onRoleChange(member: Person, role: MembershipRole | null) {
    // Owner changes are confirmed; the others apply at once.
    if (role === "owner" || member.role === "owner") setPending({ kind: "role", member, role });
    else change.mutate({ member, role });
  }

  if (members.isPending) {
    return (
      <Loading label="Loading members…">
        <Skeleton className="w-full" />
        <Skeleton className="w-2/3" />
      </Loading>
    );
  }
  if (members.isError) {
    return (
      <ErrorState
        variant="section"
        title="Cannot load the members"
        detail={errorMessage(members.error)}
        meta={errorMeta(members.error)}
        onRetry={() => void members.refetch()}
      />
    );
  }
  if (members.data.items.length === 0) {
    return <EmptyNote>This organization has no members.</EmptyNote>;
  }

  // One lookup per project, built once per render: members × projects stays cheap.
  const byUser = access.map((q) => new Map(q.data?.items.map((m) => [m.userId, m])));
  /** The person's entry in each project the actor sees (undefined while loading). */
  const entriesOf = (userId: string) =>
    projectItems.map((p, i) => ({
      project: p,
      loaded: access[i]?.data !== undefined,
      error: access[i]?.isError === true,
      entry: byUser[i]?.get(userId),
    }));
  const admin = api.admin;
  const columns = admin ? 5 : 4;

  return (
    <>
      {projects.isError && (
        <ErrorState
          variant="section"
          title="Cannot load the projects: project roles are not shown"
          detail={errorMessage(projects.error)}
          meta={errorMeta(projects.error)}
          onRetry={() => void projects.refetch()}
        />
      )}
      {/* On a phone each person is a card: the login on its own line, the rest under it. */}
      <Table cards>
        <thead>
          <tr>
            <Th>Login</Th>
            <Th>Role</Th>
            {admin && <Th>Status</Th>}
            <Th numeric>Joined</Th>
            <Th numeric>
              <span className="sr-only">Actions</span>
            </Th>
          </tr>
        </thead>
        {/* eslint-disable-next-line jsx-a11y/no-redundant-roles -- kept when a phone shows cards */}
        <tbody role="rowgroup">
          {members.data.items.map((m) => {
            const label = memberLabel(m);
            const roles = api.orgRolesFor(m);
            const note = roles.length === 0 ? api.lockedNote(m) : undefined;
            const noteId = `member-note-${m.userId}`;
            const entries = entriesOf(m.userId);
            const expanded = open === m.userId;
            const direct = entries.flatMap((e) =>
              e.entry && raisedRole(m, e.entry)
                ? [`${e.project.name}: ${ROLE_LABELS[e.entry.effectiveRole]}`]
                : [],
            );
            return (
              <Fragment key={m.userId}>
                <Tr role="row" className={cx(CARD, expanded && "[&>td]:border-b-border-faint")}>
                  <Td role="cell" className="max-sm:w-full">
                    <span className="flex flex-wrap items-center gap-2">
                      {userLink ? (
                        <Link
                          to={userLink(m)}
                          className="font-mono text-[13px] wrap-anywhere hover:underline"
                        >
                          {label}
                        </Link>
                      ) : (
                        <span
                          className={
                            m.login ? "font-mono text-[13px] wrap-anywhere" : "wrap-anywhere"
                          }
                        >
                          {label}
                        </span>
                      )}
                      {m.userId === me && <Badge>You</Badge>}
                      {m.isSystemAdmin && <Badge tone="inverse">System admin</Badge>}
                      {admin && m.isPlatformAdmin && !m.isSystemAdmin && (
                        <Badge tone="inverse">Platform admin</Badge>
                      )}
                    </span>
                    {projectItems.length > 0 && (
                      // Under the person: the project roles at a glance, and the switch to change them.
                      <span className="mt-1 flex items-start gap-2 text-caption text-muted-foreground">
                        <Button
                          variant="ghost"
                          size="small"
                          className="-ml-2.5 shrink-0 sm:h-6"
                          aria-expanded={expanded}
                          aria-controls={`projects-of-${m.userId}`}
                          aria-label={`Projects of ${label}`}
                          onClick={() => setOpen(expanded ? null : m.userId)}
                        >
                          {expanded ? "▾" : "▸"} Projects
                        </Button>
                        {direct.length > 0 && <span className="pt-1">{direct.join(" · ")}</span>}
                      </span>
                    )}
                  </Td>
                  <Td role="cell">
                    {roles.length > 0 ? (
                      <Select
                        aria-label={`Role of ${label}`}
                        className="w-[220px] max-w-full"
                        value={m.role ?? NO_ROLE}
                        disabled={change.isPending}
                        onChange={(e) =>
                          onRoleChange(
                            m,
                            e.target.value === NO_ROLE ? null : (e.target.value as MembershipRole),
                          )
                        }
                      >
                        <option value={NO_ROLE}>{PROJECTS_ONLY_LABEL}</option>
                        {roles.map((r) => (
                          <option key={r} value={r}>
                            {ROLE_LABELS[r]}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <span aria-describedby={note ? noteId : undefined}>{roleLabel(m.role)}</span>
                    )}
                    {note && (
                      <span id={noteId} className="mt-1 block text-caption text-muted-foreground">
                        {note}
                      </span>
                    )}
                  </Td>
                  {admin && "status" in m && (
                    <Td
                      role="cell"
                      className={m.status === "disabled" ? "text-subtle-foreground" : undefined}
                    >
                      {/* The column headers are hidden on a phone. */}
                      <span className="sr-only sm:hidden">Status: </span>
                      {m.status}
                    </Td>
                  )}
                  <Td role="cell" numeric>
                    <span className="sr-only sm:hidden">Joined </span>
                    {formatDate(m.joinedAt)}
                  </Td>
                  <Td role="cell" numeric>
                    <span className="inline-flex items-center justify-end gap-2 whitespace-nowrap">
                      {actions?.(m, note ? noteId : undefined)}
                      {api.canRemove(m) && (
                        <Button
                          variant="ghost-destructive"
                          size="small"
                          aria-label={`Remove ${label}`}
                          onClick={() => setPending({ kind: "remove", member: m })}
                        >
                          Remove
                        </Button>
                      )}
                    </span>
                  </Td>
                </Tr>
                {expanded && (
                  <Tr role="row" className={CARD}>
                    <Td
                      role="cell"
                      colSpan={columns}
                      id={`projects-of-${m.userId}`}
                      className="bg-muted max-sm:w-full"
                    >
                      <ProjectAccess api={api} person={m} entries={entries} />
                    </Td>
                  </Tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </Table>

      {pending?.kind === "role" && (
        <ConfirmDialog
          title={`Make ${memberLabel(pending.member)} ${pending.role ? ROLE_LABELS[pending.role].toLowerCase() : "project-only"}?`}
          confirmLabel="Change role"
          confirmVariant="primary"
          busy={change.isPending}
          onCancel={() => setPending(null)}
          onConfirm={() => change.mutate({ member: pending.member, role: pending.role })}
        >
          <p>
            {pending.role === "owner"
              ? "An owner can do everything in this organization, including deleting it and managing other owners."
              : "They lose the owner rights in this organization on their next request."}{" "}
            The change is recorded in the organization&apos;s audit log.
          </p>
        </ConfirmDialog>
      )}
      {pending?.kind === "remove" && (
        <ConfirmDialog
          title={`Remove ${memberLabel(pending.member)}?`}
          confirmLabel="Remove member"
          busy={remove.isPending}
          onCancel={() => setPending(null)}
          onConfirm={() => remove.mutate(pending.member)}
        >
          <p>
            They lose access to {organizationName} at once and are signed out of it. Their account
            and other organizations stay.{" "}
            {api.admin
              ? "The last owner cannot be removed."
              : "Invite them again to bring them back."}
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

/** The direct project role when it raises the organization role; a lower one changes nothing. */
function raisedRole(person: Person, entry: ProjectMember | undefined): ProjectRole | undefined {
  const role = entry?.projectRole;
  return role && !roleAtLeast(person.role, role) ? role : undefined;
}

interface AccessEntry {
  project: PeopleProject;
  loaded: boolean;
  error: boolean;
  entry: ProjectMember | undefined;
}

function ProjectAccess({
  api,
  person,
  entries,
}: {
  api: PeopleApi;
  person: Person;
  entries: AccessEntry[];
}) {
  return (
    <div className="flex flex-col gap-2">
      <p className="text-caption text-muted-foreground">
        {person.role
          ? `The organization role (${ROLE_LABELS[person.role].toLowerCase()}) applies in every project; a project role can only raise it there.`
          : "No organization role: access only to the projects with a role below."}
      </p>
      <ul className="flex flex-col divide-y divide-border-faint">
        {entries.map((e) => (
          <ProjectAccessRow key={e.project.id} api={api} person={person} {...e} />
        ))}
      </ul>
    </div>
  );
}

function ProjectAccessRow({
  api,
  person,
  project,
  loaded,
  error,
  entry,
}: { api: PeopleApi; person: Person } & AccessEntry) {
  const { put, remove } = useProjectRole(api, project);
  const label = memberLabel(person);
  const baseline = person.role ? `Inherited: ${ROLE_LABELS[person.role]}` : "No access";
  const current = raisedRole(person, entry);
  // Only roles above the organization role mean anything in a project.
  const choices = api
    .projectRolesFor(project, entry?.effectiveRole ?? null)
    .filter((r) => !roleAtLeast(person.role, r));

  let control: ReactNode;
  if (error) control = <span className="text-caption text-muted-foreground">Cannot load</span>;
  else if (!loaded) control = <Skeleton className="h-9 w-[220px]" />;
  else if (choices.length === 0)
    control = (
      <span className="text-caption">
        {entry ? ROLE_LABELS[entry.effectiveRole] : "No access"}
        {entry && !entry.explicit && (
          <span className="text-muted-foreground"> (organization role)</span>
        )}
      </span>
    );
  else
    control = (
      <Select
        aria-label={`${label}: role in ${project.name}`}
        className="w-[220px] max-w-full max-sm:w-full"
        value={current ?? ""}
        disabled={put.isPending || remove.isPending}
        onChange={(e) => {
          const value = e.target.value;
          if (value) put.mutate({ userId: person.userId, role: value as ProjectRole });
          else if (entry?.explicit) remove.mutate(entry);
        }}
      >
        <option value="">{baseline}</option>
        {choices.map((r) => (
          <option key={r} value={r}>
            {ROLE_LABELS[r]}
          </option>
        ))}
      </Select>
    );

  return (
    <li className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-1.5 max-sm:flex-col max-sm:items-stretch">
      <span className="flex items-center gap-2">
        <span className="font-medium break-words">{project.name}</span>
        {project.isDefault && <Badge>Default</Badge>}
      </span>
      {control}
    </li>
  );
}
