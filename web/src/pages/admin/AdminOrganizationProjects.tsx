import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { errorMessage, errorMeta, planLimitMessage } from "../../api/client";
import {
  createAdminProject,
  deleteAdminProject,
  listAdminOrganizationUsers,
  listAdminProjectMembers,
  listAdminProjects,
  putAdminProjectMember,
  queryKeys,
  removeAdminProjectMember,
  renameAdminProject,
  type AdminOrganization,
  type AdminProject,
  type ProjectMember,
  type ProjectRole,
} from "../../api/endpoints";
import { useToast } from "../../components/Toasts";
import {
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
} from "../../components/ui";
import { memberLabel } from "../../lib/members";
import { ROLE_LABELS, roleLabel } from "../../lib/roles";
import { countOf } from "../../lib/usage";

const NAME_MAX = 60;

/** Projects of one organization and who belongs to them; the platform admin acts as its owner. */
export default function AdminOrganizationProjects({
  organization,
}: {
  organization: AdminOrganization;
}) {
  const deleted = organization.deletedAt !== null;
  const projects = useQuery({
    queryKey: queryKeys.adminProjects(organization.id),
    queryFn: () => listAdminProjects(organization.id),
  });
  const [open, setOpen] = useState<string | null>(null);
  const items = projects.data?.items ?? [];
  const shown = items.find((p) => p.id === open);

  return (
    <section aria-labelledby="projects-heading" className="flex flex-col gap-3">
      <h2 id="projects-heading" className="text-lead font-semibold">
        Projects
      </h2>
      {!deleted && <NewProject organizationId={organization.id} />}
      {projects.isPending ? (
        <Loading label="Loading projects…">
          <Skeleton className="w-2/3" />
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
        <EmptyNote>This organization has no projects.</EmptyNote>
      ) : (
        <ProjectTable
          organizationId={organization.id}
          items={items}
          readOnly={deleted}
          open={open}
          onOpen={(id) => setOpen(open === id ? null : id)}
        />
      )}
      {shown && (
        <ProjectMembers
          key={shown.id}
          organizationId={organization.id}
          project={shown}
          readOnly={deleted}
        />
      )}
    </section>
  );
}

function useRefresh(organizationId: string) {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminProjects(organizationId) });
    void queryClient.invalidateQueries({ queryKey: ["admin", "organizations"] });
  };
}

function NewProject({ organizationId }: { organizationId: string }) {
  const refresh = useRefresh(organizationId);
  const toast = useToast();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | undefined>();
  const create = useMutation({
    mutationFn: (n: string) => createAdminProject(organizationId, n),
    onSuccess: (p) => {
      setName("");
      refresh();
      toast(`Project ${p.name} created`);
    },
    onError: (err) => setError(planLimitMessage(err) ?? errorMessage(err)),
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed || [...trimmed].length > NAME_MAX) {
      setError(`Use 1 to ${NAME_MAX} characters.`);
      return;
    }
    setError(undefined);
    create.mutate(trimmed);
  }

  return (
    <form
      onSubmit={onSubmit}
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
  organizationId,
  items,
  readOnly,
  open,
  onOpen,
}: {
  organizationId: string;
  items: AdminProject[];
  readOnly: boolean;
  open: string | null;
  onOpen: (id: string) => void;
}) {
  const refresh = useRefresh(organizationId);
  const toast = useToast();
  const [renaming, setRenaming] = useState<AdminProject | null>(null);
  const [deleting, setDeleting] = useState<AdminProject | null>(null);

  const remove = useMutation({
    mutationFn: (p: AdminProject) => deleteAdminProject(organizationId, p.id),
    onSuccess: (_, p) => {
      setDeleting(null);
      refresh();
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
            <Th className="max-sm:hidden">Slug</Th>
            <Th>People</Th>
            <Th className="max-sm:hidden">Monitors</Th>
            <Th className="max-sm:hidden">Channels</Th>
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
                  <RenameForm
                    organizationId={organizationId}
                    project={p}
                    onDone={() => setRenaming(null)}
                  />
                ) : (
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="font-medium break-words">{p.name}</span>
                    {p.isDefault && <Badge>Default</Badge>}
                  </span>
                )}
              </Td>
              <Td className="font-mono text-caption max-sm:hidden">{p.slug}</Td>
              <Td className="font-mono text-caption">{p.memberCount}</Td>
              <Td className="font-mono text-caption max-sm:hidden">{p.monitorCount}</Td>
              <Td className="font-mono text-caption max-sm:hidden">{p.channelCount}</Td>
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
                  {!readOnly && (
                    <>
                      <Button
                        variant="ghost"
                        size="small"
                        aria-label={`Rename ${p.name}`}
                        onClick={() => setRenaming(p)}
                      >
                        Rename
                      </Button>
                      <Button
                        variant="ghost-destructive"
                        size="small"
                        aria-label={`Delete ${p.name}`}
                        disabled={p.isDefault}
                        onClick={() => setDeleting(p)}
                      >
                        Delete
                      </Button>
                    </>
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
            {countOf(deleting.channelCount, "channel", "channels")}. The organization has to move or
            delete them first.
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
  organizationId,
  project,
  onDone,
}: {
  organizationId: string;
  project: AdminProject;
  onDone: () => void;
}) {
  const refresh = useRefresh(organizationId);
  const toast = useToast();
  const [name, setName] = useState(project.name);
  const [error, setError] = useState<string | undefined>();
  const save = useMutation({
    mutationFn: (n: string) => renameAdminProject(organizationId, project.id, n),
    onSuccess: (p) => {
      refresh();
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

function accessLabel(m: ProjectMember): string {
  if (m.organizationRole && m.effectiveRole === m.organizationRole) return "Organization role";
  return "Project role";
}

const PROJECT_ROLES: ProjectRole[] = ["owner", "maintainer", "member", "viewer"];

function ProjectMembers({
  organizationId,
  project,
  readOnly,
}: {
  organizationId: string;
  project: AdminProject;
  readOnly: boolean;
}) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const members = useQuery({
    queryKey: queryKeys.adminProjectMembers(organizationId, project.id),
    queryFn: () => listAdminProjectMembers(organizationId, project.id),
  });
  const org = useQuery({
    queryKey: queryKeys.adminUsers(organizationId),
    queryFn: () => listAdminOrganizationUsers(organizationId),
    enabled: !readOnly,
  });
  const [adding, setAdding] = useState("");

  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: queryKeys.adminProjectMembers(organizationId, project.id),
    });
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminProjects(organizationId) });
  };
  const put = useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: ProjectRole }) =>
      putAdminProjectMember(organizationId, project.id, userId, role),
    onSuccess: (m) => {
      refresh();
      setAdding("");
      toast(`${memberLabel(m)} in ${project.name}: ${ROLE_LABELS[m.effectiveRole].toLowerCase()}`);
    },
    onError: (err) => toast(errorMessage(err), "error"),
  });
  const remove = useMutation({
    mutationFn: (m: ProjectMember) =>
      removeAdminProjectMember(organizationId, project.id, m.userId),
    onSuccess: (_, m) => {
      refresh();
      toast(
        m.organizationRole
          ? `${memberLabel(m)} keeps the organization role in ${project.name}`
          : `${memberLabel(m)} removed from ${project.name}`,
      );
    },
    onError: (err) => toast(errorMessage(err), "error"),
  });

  const direct = new Set(
    (members.data?.items ?? []).filter((m) => m.explicit).map((m) => m.userId),
  );
  const candidates = (org.data?.items ?? []).filter((u) => !direct.has(u.id));
  const [newRole, setNewRole] = useState<ProjectRole>("member");

  return (
    <section
      aria-labelledby="project-people-heading"
      className="flex flex-col gap-4 border-t border-border pt-6"
    >
      <div className="flex flex-col gap-1.5">
        <h3 id="project-people-heading" className="text-lead font-semibold">
          People in {project.name}
        </h3>
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
              {!readOnly && (
                <Th>
                  <span className="sr-only">Actions</span>
                </Th>
              )}
            </tr>
          </thead>
          <tbody>
            {members.data.items.map((m) => {
              const adjustable = !readOnly;
              return (
                <Tr key={m.userId}>
                  <Td>
                    <span className="font-mono text-caption break-all">{memberLabel(m)}</span>
                  </Td>
                  <Td className="text-caption">
                    {m.organizationRole ? roleLabel(m.organizationRole) : "None"}
                  </Td>
                  <Td className="text-caption">{accessLabel(m)}</Td>
                  <Td className="text-caption">
                    {adjustable ? (
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
                        {PROJECT_ROLES.map((r) => (
                          <option key={r} value={r}>
                            {ROLE_LABELS[r]}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      ROLE_LABELS[m.effectiveRole]
                    )}
                  </Td>
                  {!readOnly && (
                    <Td>
                      {m.explicit && (
                        <div className="flex justify-end">
                          <Button
                            variant="ghost-destructive"
                            size="small"
                            aria-label={`Remove ${memberLabel(m)} from ${project.name}`}
                            busy={remove.isPending && remove.variables?.userId === m.userId}
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
      {!readOnly && (
        <form
          aria-label={`Add someone to ${project.name}`}
          className="flex flex-col gap-2 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            if (adding) put.mutate({ userId: adding, role: newRole });
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
                {candidates.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.login} · {roleLabel(u.role)}
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
                {PROJECT_ROLES.map((r) => (
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
    </section>
  );
}
