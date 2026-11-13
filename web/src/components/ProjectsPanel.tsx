import { useMutation, useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { ApiError, errorMessage, errorMeta, planLimitMessage } from "../api/client";
import type { ProjectMember, ProjectRole } from "../api/endpoints";
import { memberLabel } from "../lib/members";
import { useRefreshPeople, type PeopleApi, type PeopleProject } from "../lib/people";
import { ROLE_LABELS, roleLabel } from "../lib/roles";
import { countOf } from "../lib/usage";
import { useToast } from "./Toasts";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyNote,
  ErrorState,
  Field,
  Input,
  Loading,
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
} from "./ui";

const NAME_MAX = 60;

/**
 * The projects of an organization: create, rename, delete, and the people of one project.
 * `extra` adds a section under the open project (the invitations into it).
 */
export default function ProjectsPanel({
  api,
  initialOpen = null,
  extra,
  heading = "h3",
}: {
  api: PeopleApi;
  initialOpen?: string | null;
  extra?: (project: PeopleProject) => ReactNode;
  /** The level of the open project's heading: under the page title or under a section. */
  heading?: "h2" | "h3";
}) {
  const projects = useQuery({ queryKey: api.keys.projects, queryFn: api.listProjects });
  const [open, setOpen] = useState<string | null>(initialOpen);
  const items = projects.data?.items ?? [];
  const shown = items.find((p) => p.id === open);

  return (
    <>
      {api.canCreateProject && <NewProject api={api} />}
      {projects.isPending ? (
        <Loading label="Loading projects…">
          <Skeleton className="w-2/3" />
          <Skeleton className="w-1/2" />
        </Loading>
      ) : projects.isError ? (
        <ErrorState
          variant="section"
          title="Cannot load the projects"
          detail={errorMessage(projects.error)}
          meta={errorMeta(projects.error)}
          onRetry={() => void projects.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyNote>No projects to show.</EmptyNote>
      ) : (
        <ProjectTable
          api={api}
          items={items}
          open={open}
          onOpen={(id) => setOpen(open === id ? null : id)}
        />
      )}
      {shown && <ProjectMembers key={shown.id} api={api} project={shown} heading={heading} />}
      {shown && extra?.(shown)}
    </>
  );
}

function NewProject({ api }: { api: PeopleApi }) {
  const refresh = useRefreshPeople(api);
  const toast = useToast();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | undefined>();
  const create = useMutation({
    mutationFn: (n: string) => api.createProject(n),
    onSuccess: (p) => {
      setName("");
      void refresh(p.id);
      toast(`Project ${p.name} created`);
    },
    onError: (err) => setError(planLimitMessage(err) ?? errorMessage(err)),
  });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const trimmed = name.trim();
        if (!trimmed) return setError("Enter a name.");
        if ([...trimmed].length > NAME_MAX) return setError(`Use ${NAME_MAX} characters or fewer.`);
        setError(undefined);
        create.mutate(trimmed);
      }}
      noValidate
      aria-label="Create a project"
      className="flex flex-col gap-2 sm:flex-row sm:items-end"
    >
      <Field label="New project" error={error} className="sm:w-80">
        {(c) => (
          <Input
            {...c}
            name="projectName"
            autoComplete="off"
            maxLength={NAME_MAX * 2}
            placeholder="Payments"
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setError(undefined);
            }}
          />
        )}
      </Field>
      <Button type="submit" busy={create.isPending} className={error ? "sm:mb-7" : undefined}>
        Create project
      </Button>
    </form>
  );
}

function ProjectTable({
  api,
  items,
  open,
  onOpen,
}: {
  api: PeopleApi;
  items: PeopleProject[];
  open: string | null;
  onOpen: (id: string) => void;
}) {
  const refresh = useRefreshPeople(api);
  const toast = useToast();
  const [renaming, setRenaming] = useState<PeopleProject | null>(null);
  const [deleting, setDeleting] = useState<PeopleProject | null>(null);

  const remove = useMutation({
    mutationFn: (p: PeopleProject) => api.deleteProject(p.id),
    onSuccess: (_, p) => {
      setDeleting(null);
      void refresh(p.id);
      toast(`Project ${p.name} deleted`);
    },
    onError: (err) => {
      setDeleting(null);
      toast(`Delete failed. ${errorMessage(err)}`, "error");
    },
  });

  return (
    <>
      <Table>
        <thead>
          <tr>
            <Th>Name</Th>
            {api.admin && <Th className="max-sm:hidden">Slug</Th>}
            {api.admin && <Th>People</Th>}
            <Th>Monitors</Th>
            <Th className="max-sm:hidden">Channels</Th>
            {!api.admin && <Th className="max-sm:hidden">Your role</Th>}
            <Th>
              <span className="sr-only">Actions</span>
            </Th>
          </tr>
        </thead>
        <tbody>
          {items.map((p) => (
            <Tr key={p.id}>
              <Td>
                {renaming?.id === p.id ? (
                  <RenameForm api={api} project={p} onDone={() => setRenaming(null)} />
                ) : (
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="font-medium break-words">{p.name}</span>
                    {p.isDefault && <Badge>Default</Badge>}
                  </span>
                )}
              </Td>
              {api.admin && <Td className="font-mono text-caption max-sm:hidden">{p.slug}</Td>}
              {api.admin && (
                <Td className="font-mono text-caption">{"memberCount" in p && p.memberCount}</Td>
              )}
              <Td className="font-mono text-caption">{p.monitorCount}</Td>
              <Td className="font-mono text-caption max-sm:hidden">{p.channelCount}</Td>
              {!api.admin && (
                <Td className="text-caption max-sm:hidden">{"role" in p && roleLabel(p.role)}</Td>
              )}
              <Td>
                <div className="flex justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="small"
                    aria-expanded={open === p.id}
                    aria-label={`People in ${p.name}`}
                    onClick={() => onOpen(p.id)}
                  >
                    People
                  </Button>
                  {api.canRename(p) && (
                    <Button
                      variant="ghost"
                      size="small"
                      aria-label={`Rename ${p.name}`}
                      onClick={() => setRenaming(p)}
                    >
                      Rename
                    </Button>
                  )}
                  {api.canDelete(p) && (
                    <Button
                      variant="ghost-destructive"
                      size="small"
                      aria-label={`Delete ${p.name}`}
                      disabled={p.isDefault}
                      onClick={() => setDeleting(p)}
                    >
                      Delete
                    </Button>
                  )}
                </div>
              </Td>
            </Tr>
          ))}
        </tbody>
      </Table>
      {deleting &&
        (deleting.monitorCount > 0 || deleting.channelCount > 0 ? (
          <ConfirmDialog
            title={`${deleting.name} is not empty`}
            confirmLabel="OK"
            confirmVariant="secondary"
            cancelLabel="Close"
            onConfirm={() => setDeleting(null)}
            onCancel={() => setDeleting(null)}
          >
            It still has {countOf(deleting.monitorCount, "monitor", "monitors")} and{" "}
            {countOf(deleting.channelCount, "channel", "channels")}.{" "}
            {api.admin
              ? "The organization has to move or delete them first."
              : "Move them to another project or delete them first."}
          </ConfirmDialog>
        ) : (
          <ConfirmDialog
            title={`Delete ${deleting.name}?`}
            confirmLabel="Delete project"
            busy={remove.isPending}
            onConfirm={() => remove.mutate(deleting)}
            onCancel={() => setDeleting(null)}
          >
            The project and its list of people are removed. It has no monitors or channels.
          </ConfirmDialog>
        ))}
    </>
  );
}

function RenameForm({
  api,
  project,
  onDone,
}: {
  api: PeopleApi;
  project: PeopleProject;
  onDone: () => void;
}) {
  const refresh = useRefreshPeople(api);
  const toast = useToast();
  const [name, setName] = useState(project.name);
  const [error, setError] = useState<string | undefined>();
  const save = useMutation({
    mutationFn: (n: string) => api.renameProject(project.id, n),
    onSuccess: (p) => {
      void refresh(p.id);
      toast(`Renamed to ${p.name}`);
      onDone();
    },
    onError: (err) => setError(errorMessage(err)),
  });
  return (
    <form
      noValidate
      aria-label={`Rename ${project.name}`}
      className="flex flex-wrap items-start gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        const trimmed = name.trim();
        if (!trimmed || [...trimmed].length > NAME_MAX) {
          setError(`Use 1 to ${NAME_MAX} characters.`);
          return;
        }
        save.mutate(trimmed);
      }}
    >
      <Field label="Project name" error={error} className="min-w-40 flex-1">
        {(c) => (
          <Input
            {...c}
            // eslint-disable-next-line jsx-a11y/no-autofocus -- the user just asked to rename this row
            autoFocus
            value={name}
            maxLength={NAME_MAX * 2}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") onDone();
            }}
          />
        )}
      </Field>
      <span className="flex gap-1 pt-6">
        <Button type="submit" size="small" busy={save.isPending}>
          Save
        </Button>
        <Button variant="ghost" size="small" onClick={onDone}>
          Cancel
        </Button>
      </span>
    </form>
  );
}

/** Where a person's role in the project comes from. */
function accessLabel(m: ProjectMember): string {
  if (m.organizationRole && m.effectiveRole === m.organizationRole) return "Organization role";
  return "Project role";
}

/** Changes and removes the direct role of a person in a project; shared with the members table. */
export function useProjectRole(api: PeopleApi, project: PeopleProject) {
  const refresh = useRefreshPeople(api);
  const toast = useToast();
  const put = useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: ProjectRole }) =>
      api.putProjectMember(project.id, userId, role),
    // Pending until the new role is back from the server, so the select cannot offer the old one.
    onSuccess: (m) => {
      toast(`${memberLabel(m)} in ${project.name}: ${ROLE_LABELS[m.effectiveRole].toLowerCase()}`);
      return refresh(project.id);
    },
    onError: (err) => toast(errorMessage(err), "error"),
  });
  const remove = useMutation({
    mutationFn: (m: ProjectMember) => api.removeProjectMember(project.id, m.userId),
    onSuccess: (_, m) => {
      toast(
        m.organizationRole
          ? `${memberLabel(m)} keeps the organization role in ${project.name}`
          : `${memberLabel(m)} removed from ${project.name}`,
      );
      return refresh(project.id);
    },
    onError: (err) => toast(errorMessage(err), "error"),
  });
  return { put, remove };
}

function ProjectMembers({
  api,
  project,
  heading: Heading,
}: {
  api: PeopleApi;
  project: PeopleProject;
  heading: "h2" | "h3";
}) {
  const members = useQuery({
    queryKey: api.keys.projectMembers(project.id),
    queryFn: () => api.listProjectMembers(project.id),
  });
  // Anyone the actor may add: they may give at least one role to someone without one here.
  const manage = api.projectRolesFor(project, null).length > 0;
  const org = useQuery({
    queryKey: api.keys.members,
    queryFn: api.listMembers,
    enabled: manage && api.canReadMembers,
  });
  const { put, remove } = useProjectRole(api, project);
  const [adding, setAdding] = useState("");
  const [newRole, setNewRole] = useState<ProjectRole>("member");
  const roles = api.projectRolesFor(project, null);

  const direct = new Set(
    (members.data?.items ?? []).filter((m) => m.explicit).map((m) => m.userId),
  );
  const candidates = (org.data?.items ?? []).filter((m) => !direct.has(m.userId));

  return (
    <section
      aria-labelledby="project-people-heading"
      className="flex flex-col gap-4 border-t border-border pt-6"
    >
      <div className="flex flex-col gap-1.5">
        <Heading id="project-people-heading" className="text-lead font-semibold">
          People in {project.name}
        </Heading>
        <p className="text-[13px] text-muted-foreground">
          Everyone with an organization role is here with that role. A role given here applies to
          this project only and can raise the organization role, never lower it.
        </p>
      </div>
      {members.isPending ? (
        <Loading label="Loading people…">
          <Skeleton className="w-1/2" />
        </Loading>
      ) : members.isError ? (
        <ErrorState
          variant="section"
          title="Cannot load the people of this project"
          detail={errorMessage(members.error)}
          meta={errorMeta(members.error)}
          onRetry={() => void members.refetch()}
        />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Login</Th>
              <Th>Organization role</Th>
              <Th>Access</Th>
              <Th>Role here</Th>
              {manage && (
                <Th>
                  <span className="sr-only">Actions</span>
                </Th>
              )}
            </tr>
          </thead>
          <tbody>
            {members.data.items.map((m) => {
              const choices = api.projectRolesFor(project, m.effectiveRole);
              return (
                <Tr key={m.userId}>
                  <Td>
                    <span
                      className={
                        m.login ? "font-mono text-caption break-words" : "text-caption break-words"
                      }
                    >
                      {memberLabel(m)}
                    </span>
                  </Td>
                  <Td className="text-caption">
                    {m.organizationRole ? roleLabel(m.organizationRole) : "None"}
                  </Td>
                  <Td className="text-caption">{accessLabel(m)}</Td>
                  <Td className="text-caption">
                    {choices.length > 0 ? (
                      <Select
                        aria-label={`Role of ${memberLabel(m)} in ${project.name}`}
                        value={m.projectRole ?? ""}
                        disabled={put.isPending}
                        onChange={(e) =>
                          put.mutate({ userId: m.userId, role: e.target.value as ProjectRole })
                        }
                      >
                        {!m.projectRole && (
                          <option value="" disabled>
                            Inherited: {ROLE_LABELS[m.effectiveRole]}
                          </option>
                        )}
                        {choices.map((r) => (
                          <option key={r} value={r}>
                            {ROLE_LABELS[r]}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      ROLE_LABELS[m.effectiveRole]
                    )}
                  </Td>
                  {manage && (
                    <Td>
                      {m.explicit && choices.length > 0 && (
                        <div className="flex justify-end">
                          <Button
                            variant="ghost-destructive"
                            size="small"
                            aria-label={`Remove ${memberLabel(m)} from ${project.name}`}
                            busy={remove.isPending && remove.variables.userId === m.userId}
                            onClick={() => remove.mutate(m)}
                          >
                            {m.organizationRole ? "Reset" : "Remove"}
                          </Button>
                        </div>
                      )}
                    </Td>
                  )}
                </Tr>
              );
            })}
          </tbody>
        </Table>
      )}
      {manage && api.canReadMembers && (
        <form
          aria-label={`Add someone to ${project.name}`}
          className="flex flex-col gap-2 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            if (adding)
              put.mutate({ userId: adding, role: newRole }, { onSuccess: () => setAdding("") });
          }}
        >
          <Field label="Add someone" className="sm:w-80">
            {(c) => (
              <Select
                {...c}
                value={adding}
                disabled={candidates.length === 0}
                onChange={(e) => setAdding(e.target.value)}
              >
                <option value="">
                  {candidates.length === 0 ? "Everyone is already here" : "Choose a member"}
                </option>
                {candidates.map((m) => (
                  <option key={m.userId} value={m.userId}>
                    {memberLabel(m)} · {roleLabel(m.role)}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Role" className="sm:w-40">
            {(c) => (
              <Select
                {...c}
                value={newRole}
                onChange={(e) => setNewRole(e.target.value as ProjectRole)}
              >
                {roles.map((r) => (
                  <option key={r} value={r}>
                    {ROLE_LABELS[r]}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Button type="submit" variant="secondary" disabled={!adding} busy={put.isPending}>
            Add to project
          </Button>
        </form>
      )}
      {members.error instanceof ApiError && members.error.status === 404 && (
        <Alert tone="neutral" title="Not available.">
          {api.admin
            ? "This project was deleted."
            : "This project was deleted or you no longer belong to it."}
        </Alert>
      )}
    </section>
  );
}
