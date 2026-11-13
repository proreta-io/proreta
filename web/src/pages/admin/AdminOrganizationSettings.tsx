import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { errorMessage, errorMeta } from "../../api/client";
import {
  createAdminSpecula,
  deleteAdminAuthDomain,
  getAdminUsage,
  listAdminAuthDomains,
  listAdminChannels,
  listAdminProjects,
  listAdminSpecula,
  listAdminStatusPages,
  queryKeys,
  revokeAdminSpecula,
  updateAdminAuthDomain,
  updateAdminSpecula,
  type AdminOrganization,
  type AuthDomain,
  type AuthDomainRole,
  type Specula,
  type SpeculaCreated,
} from "../../api/endpoints";
import { useToast } from "../../components/Toasts";
import {
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyNote,
  ErrorState,
  Loading,
  Meter,
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
  TriangleShape,
} from "../../components/ui";
import { channelTypeLabel, eventsLabel, reminderLabel } from "../../lib/channels";
import { channelScopeLabel } from "../../lib/projects";
import { ROLE_LABELS } from "../../lib/roles";
import { emptySpeculaForm, valuesFromSpecula } from "../../lib/specula";
import { SpeculaForm, SpeculaTable, TokenPanel, type SpeculaOps } from "../Specula";

/**
 * What the organization's owner sees under Settings, Channels, Status pages and Specula, for the
 * platform admin. Sign-in domains and Specula can be changed; channels and status pages are read-only.
 */
export default function AdminOrganizationSettings({
  organization,
}: {
  organization: AdminOrganization;
}) {
  const deleted = organization.deletedAt !== null;
  return (
    <>
      {/* A deleted organization has no quota to resolve. */}
      {!deleted && <PlanUsage organizationId={organization.id} />}
      <AuthDomains organizationId={organization.id} readOnly={deleted} />
      <Channels organizationId={organization.id} />
      <StatusPages organizationId={organization.id} />
      <SpeculaSection organizationId={organization.id} readOnly={deleted} />
    </>
  );
}

function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section aria-labelledby={`${id}-heading`} className="flex flex-col gap-3">
      <h2 id={`${id}-heading`} className="text-lead font-semibold">
        {title}
      </h2>
      {children}
    </section>
  );
}

/** The loading and error states of a section's query; `children` runs once the data is there. */
function Async<T>({
  query,
  what,
  children,
}: {
  query: {
    isPending: boolean;
    isError: boolean;
    error: unknown;
    data: T | undefined;
    refetch: () => unknown;
  };
  what: string;
  children: (data: T) => ReactNode;
}) {
  if (query.isPending) {
    return (
      <Loading label={`Loading ${what}…`}>
        <Skeleton className="w-2/3" />
      </Loading>
    );
  }
  if (query.isError || query.data === undefined) {
    return (
      <ErrorState
        variant="section"
        title={`Cannot load ${what}`}
        detail={errorMessage(query.error)}
        meta={errorMeta(query.error)}
        onRetry={() => void query.refetch()}
      />
    );
  }
  return <>{children(query.data)}</>;
}

const usageRows = [
  ["monitors", "Monitors"],
  ["heartbeats", "Heartbeat monitors"],
  ["channels", "Notification channels"],
  ["members", "Members and open invitations"],
  ["projects", "Projects"],
  ["statusPages", "Status pages"],
  ["specula", "Specula"],
] as const;

function PlanUsage({ organizationId }: { organizationId: string }) {
  const usage = useQuery({
    queryKey: queryKeys.adminUsage(organizationId),
    queryFn: () => getAdminUsage(organizationId),
  });
  return (
    <Section id="usage" title="Plan and usage">
      <Async query={usage} what="plan and usage">
        {(u) => (
          <div className="max-w-[560px] divide-y divide-border-faint border-t border-border-faint">
            {usageRows.map(([key, label]) => (
              <Meter key={key} label={label} used={u.usage[key]} limit={u.limits[key]} />
            ))}
          </div>
        )}
      </Async>
    </Section>
  );
}

const DOMAIN_ROLES: AuthDomainRole[] = ["viewer", "member", "maintainer"];

function AuthDomains({ organizationId, readOnly }: { organizationId: string; readOnly: boolean }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const domains = useQuery({
    queryKey: queryKeys.adminAuthDomains(organizationId),
    queryFn: () => listAdminAuthDomains(organizationId),
  });
  const [toDelete, setToDelete] = useState<AuthDomain | null>(null);
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminAuthDomains(organizationId) });
  const update = useMutation({
    mutationFn: (v: { id: string; body: { defaultRole?: AuthDomainRole; enabled?: boolean } }) =>
      updateAdminAuthDomain(organizationId, v.id, v.body),
    onSuccess: refresh,
    onError: (err) => toast(`Change failed. ${errorMessage(err)}`, "error"),
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteAdminAuthDomain(organizationId, id),
    onSuccess: () => {
      setToDelete(null);
      refresh();
      toast("Domain released");
    },
    onError: (err) => {
      setToDelete(null);
      toast(`Release failed. ${errorMessage(err)}`, "error");
    },
  });
  return (
    <Section id="auth-domains" title="Sign-in domains">
      <p className="max-w-[640px] text-caption text-muted-foreground">
        Google Workspace domains whose people join this organization on their first sign-in. Only
        the owner can claim a domain: it needs their own Google sign-in as proof.
      </p>
      <Async query={domains} what="the sign-in domains">
        {({ items }) =>
          items.length === 0 ? (
            <EmptyNote>No domain is claimed.</EmptyNote>
          ) : (
            <ul aria-label="Claimed domains" className="flex flex-col border-t border-border">
              {items.map((d) => (
                <li key={d.id} className="flex flex-col gap-3 border-b border-border py-3">
                  <span className="font-mono break-all">{d.domain}</span>
                  <div className="flex flex-wrap items-center gap-x-5 gap-y-3">
                    <span className="flex items-center gap-2">
                      <span aria-hidden="true" className="text-caption text-muted-foreground">
                        Joins as
                      </span>
                      <Select
                        aria-label={`Role for ${d.domain}`}
                        value={d.defaultRole}
                        disabled={readOnly || update.isPending}
                        onChange={(e) =>
                          update.mutate({
                            id: d.id,
                            body: { defaultRole: e.target.value as AuthDomainRole },
                          })
                        }
                      >
                        {DOMAIN_ROLES.map((r) => (
                          <option key={r} value={r}>
                            {ROLE_LABELS[r]}
                          </option>
                        ))}
                      </Select>
                    </span>
                    <Checkbox
                      label={
                        <>
                          Auto-join<span className="sr-only"> for {d.domain}</span>
                        </>
                      }
                      checked={d.enabled}
                      disabled={readOnly || update.isPending}
                      onChange={(e) =>
                        update.mutate({ id: d.id, body: { enabled: e.target.checked } })
                      }
                    />
                    {!readOnly && (
                      <Button
                        variant="secondary"
                        size="small"
                        className="sm:ml-auto"
                        onClick={() => setToDelete(d)}
                      >
                        Release {d.domain}
                      </Button>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )
        }
      </Async>
      {toDelete && (
        <ConfirmDialog
          title={`Release ${toDelete.domain}?`}
          confirmLabel="Release"
          busy={remove.isPending}
          onConfirm={() => remove.mutate(toDelete.id)}
          onCancel={() => setToDelete(null)}
        >
          People of this domain will no longer join on their first sign-in. Members who already
          joined stay. Another organization may claim the domain afterwards.
        </ConfirmDialog>
      )}
    </Section>
  );
}

/** The organization's projects, for the project columns; undefined until loaded. */
function useProjectNames(organizationId: string) {
  return useQuery({
    queryKey: queryKeys.adminProjects(organizationId),
    queryFn: () => listAdminProjects(organizationId),
  }).data?.items;
}

function Channels({ organizationId }: { organizationId: string }) {
  const channels = useQuery({
    queryKey: queryKeys.adminChannels(organizationId),
    queryFn: () => listAdminChannels(organizationId),
  });
  const projects = useProjectNames(organizationId);
  return (
    <Section id="channels" title="Notification channels">
      <p className="max-w-[640px] text-caption text-muted-foreground">
        Read-only. The webhook URL, headers and tokens are write-only and are never shown; the owner
        changes channels under Channels.
      </p>
      <Async query={channels} what="the notification channels">
        {({ items }) =>
          items.length === 0 ? (
            <EmptyNote>No notification channels. Notifications are off.</EmptyNote>
          ) : (
            <Table>
              <thead>
                <tr>
                  <Th>Name</Th>
                  <Th>Type</Th>
                  <Th className="max-sm:hidden">Project</Th>
                  <Th className="max-md:hidden">Events</Th>
                  <Th className="max-md:hidden">Reminder</Th>
                  <Th>Status</Th>
                </tr>
              </thead>
              <tbody>
                {items.map((c) => (
                  <Tr key={c.id} tone={c.status === "failed" ? "down" : "default"}>
                    <Td>
                      <span className="font-medium break-words">{c.name}</span>
                    </Td>
                    <Td className="text-caption">{channelTypeLabel[c.type]}</Td>
                    <Td className="text-caption max-sm:hidden">
                      {channelScopeLabel(c.projectId, projects)}
                    </Td>
                    <Td className="text-caption max-md:hidden">{eventsLabel(c.events)}</Td>
                    <Td className="text-caption whitespace-nowrap max-md:hidden">
                      {reminderLabel(c.reminderMinutes)}
                    </Td>
                    <Td>
                      {c.status === "active" ? (
                        <Badge tone="success">Active</Badge>
                      ) : (
                        <Badge tone="danger" icon={<TriangleShape size="small" />}>
                          Failed
                        </Badge>
                      )}
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )
        }
      </Async>
    </Section>
  );
}

function StatusPages({ organizationId }: { organizationId: string }) {
  const pages = useQuery({
    queryKey: queryKeys.adminStatusPages(organizationId),
    queryFn: () => listAdminStatusPages(organizationId),
  });
  const projects = useProjectNames(organizationId);
  return (
    <Section id="status-pages" title="Status pages">
      <p className="max-w-[640px] text-caption text-muted-foreground">
        Read-only. The owner and maintainers edit a page under Status pages.
      </p>
      <Async query={pages} what="the status pages">
        {({ items }) =>
          items.length === 0 ? (
            <EmptyNote>No status pages.</EmptyNote>
          ) : (
            <Table>
              <thead>
                <tr>
                  <Th>Page</Th>
                  <Th className="max-sm:hidden">Project</Th>
                  <Th className="max-md:hidden">Components</Th>
                  <Th>Status</Th>
                </tr>
              </thead>
              <tbody>
                {items.map((p) => (
                  <Tr key={p.id}>
                    <Td>
                      <span className="font-medium break-words">{p.title}</span>
                      <a
                        href={p.publicUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="block font-mono text-caption break-all underline underline-offset-3"
                      >
                        {p.slug}
                      </a>
                    </Td>
                    <Td className="text-caption max-sm:hidden">
                      {channelScopeLabel(p.projectId, projects)}
                    </Td>
                    <Td className="text-caption max-md:hidden">{p.componentCount}</Td>
                    <Td>
                      <span className="flex flex-wrap gap-1.5">
                        <Badge tone={p.published ? "success" : "neutral"}>
                          {p.published ? "Published" : "Unpublished"}
                        </Badge>
                        {p.passwordProtected && <Badge>Password</Badge>}
                      </span>
                    </Td>
                  </Tr>
                ))}
              </tbody>
            </Table>
          )
        }
      </Async>
    </Section>
  );
}

function SpeculaSection({
  organizationId,
  readOnly,
}: {
  organizationId: string;
  readOnly: boolean;
}) {
  const specula = useQuery({
    queryKey: queryKeys.adminSpecula(organizationId),
    queryFn: () => listAdminSpecula(organizationId),
  });
  const projects = useProjectNames(organizationId);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Specula | null>(null);
  const [created, setCreated] = useState<SpeculaCreated | null>(null);
  const ops: SpeculaOps = {
    create: (body) => createAdminSpecula(organizationId, body),
    update: (id, body) => updateAdminSpecula(organizationId, id, body),
    revoke: (id) => revokeAdminSpecula(organizationId, id),
    keys: [queryKeys.adminSpecula(organizationId), queryKeys.adminUsage(organizationId)],
  };
  return (
    <Section id="specula" title="Specula">
      {created && <TokenPanel created={created} onDone={() => setCreated(null)} />}
      {!readOnly && !created && !editing && !creating && (
        <div>
          <Button variant="secondary" onClick={() => setCreating(true)}>
            Register a Specula
          </Button>
        </div>
      )}
      {creating && !created && (
        <SpeculaForm
          mode="create"
          initial={emptySpeculaForm}
          projects={projects}
          ops={ops}
          onCancel={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false);
            setCreated(c ?? null);
          }}
        />
      )}
      {editing && (
        <SpeculaForm
          key={editing.id}
          mode="edit"
          id={editing.id}
          initial={valuesFromSpecula(editing)}
          projects={projects}
          ops={ops}
          onCancel={() => setEditing(null)}
          onSaved={() => setEditing(null)}
        />
      )}
      <Async query={specula} what="the Specula">
        {({ items }) =>
          items.length === 0 ? (
            <EmptyNote>No Specula is registered.</EmptyNote>
          ) : (
            <SpeculaTable
              items={items}
              projects={projects}
              manage={!readOnly}
              ops={ops}
              onEdit={(s) => {
                setCreating(false);
                setEditing(s);
              }}
            />
          )
        }
      </Async>
    </Section>
  );
}
