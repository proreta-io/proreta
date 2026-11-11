import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  deleteAdminOrganization,
  getAdminStats,
  getAdminOrganization,
  listAdminOrganizationUsers,
  queryKeys,
  resetAdminUserPassword,
  updateAdminOrganization,
  type AdminOrganization,
  type AdminOrganizationUpdate,
  type AdminUser,
} from "../../api/endpoints";
import TemporaryPasswordPanel from "../../components/TemporaryPasswordPanel";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/Toasts";
import {
  Alert,
  Badge,
  Button,
  buttonClass,
  ConfirmDialog,
  ErrorState,
  Field,
  Metric,
  MetricStrip,
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
  Loading,
  EmptyNote,
} from "../../components/ui";
import { formatDate, formatDateTime } from "../../lib/format";
import { OrganizationStatusBadge } from "./OrganizationStatusBadge";
import { HIDDEN_PLANS } from "../../lib/usage";

/** The message of a failed admin change: the field error of a 422, otherwise the problem. */
function changeError(err: unknown): string {
  if (err instanceof ApiError && err.status === 422) {
    const fields = err.fieldErrorMap();
    if (fields.planKey) return `Plan: ${fields.planKey}.`;
    if (fields.status) return `Status: ${fields.status}.`;
  }
  return errorMessage(err);
}

const back = (
  <Link
    to="/admin/organizations"
    className="self-start py-2.5 text-[13px] text-muted-foreground hover:text-foreground sm:py-0"
  >
    ← Organizations
  </Link>
);

export default function AdminOrganizationDetails() {
  const { id = "" } = useParams();
  // Keyed by organization: moving to another organization starts with fresh state, so a temporary password
  // shown for one organization never stays on the screen of the next.
  return <OrganizationDetails key={id} id={id} />;
}

function OrganizationDetails({ id }: { id: string }) {
  const organization = useQuery({
    queryKey: queryKeys.adminOrganization(id),
    queryFn: () => getAdminOrganization(id),
  });

  if (organization.isPending) {
    return (
      <Loading label="Loading organization…" className="pt-9">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="w-80 max-w-full" />
      </Loading>
    );
  }
  if (organization.isError) {
    if (organization.error instanceof ApiError && organization.error.status === 404) {
      return (
        <section className="flex flex-col items-start gap-4 py-10">
          <h1 className="text-headline font-semibold tracking-[-0.02em] sm:text-display">
            Organization not found
          </h1>
          <Link to="/admin/organizations" className={buttonClass("secondary")}>
            Back to organizations
          </Link>
        </section>
      );
    }
    return (
      <div className="pt-9">
        <ErrorState
          title="Cannot load the organization"
          detail={errorMessage(organization.error)}
          meta={errorMeta(organization.error)}
          onRetry={() => void organization.refetch()}
        />
      </div>
    );
  }
  return (
    <section className="flex flex-col gap-8 pt-4">
      <OrganizationInfo organization={organization.data} />
      <OrganizationUsers organization={organization.data} />
    </section>
  );
}

function OrganizationInfo({ organization }: { organization: AdminOrganization }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const plans = useQuery({ queryKey: queryKeys.adminStats, queryFn: getAdminStats });
  const [plan, setPlan] = useState(organization.planKey);
  const [planError, setPlanError] = useState<string | undefined>();
  const [confirm, setConfirm] = useState<"suspend" | "unsuspend" | "delete" | null>(null);
  const auth = useAuth();
  const deleted = organization.deletedAt !== null;
  const suspended = organization.status === "suspended";
  // Guard rails: the backend refuses these with 409 protected-organization.
  const protectedReason =
    organization.slug === "default"
      ? "The platform's own organization: it cannot be suspended or deleted."
      : organization.id === auth.session?.organization.id
        ? "Your own organization: you cannot suspend or delete it."
        : undefined;

  function refresh(updated?: AdminOrganization) {
    if (updated) queryClient.setQueryData(queryKeys.adminOrganization(organization.id), updated);
    else
      void queryClient.invalidateQueries({
        queryKey: queryKeys.adminOrganization(organization.id),
      });
    void queryClient.invalidateQueries({ queryKey: ["admin", "organizations"] });
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminStats });
  }

  const update = useMutation({
    mutationFn: (body: AdminOrganizationUpdate) => updateAdminOrganization(organization.id, body),
    onSuccess: (updated, body) => {
      setConfirm(null);
      refresh(updated);
      if (body.planKey) toast(`Plan changed to ${updated.planName}`);
      if (body.status)
        toast(
          updated.status === "suspended" ? "Organization suspended" : "Organization unsuspended",
        );
    },
    onError: (err, body) => {
      setConfirm(null);
      if (body.planKey) setPlanError(changeError(err));
      else toast(changeError(err), "error");
    },
  });

  const remove = useMutation({
    mutationFn: () => deleteAdminOrganization(organization.id),
    onSuccess: () => {
      setConfirm(null);
      refresh();
      toast("Organization deleted");
    },
    onError: (err) => {
      setConfirm(null);
      toast(changeError(err), "error");
    },
  });

  function onSavePlan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPlanError(undefined);
    update.mutate({ planKey: plan });
  }

  // `community` stays out of the picker until self-hosted packaging, unless the organization
  // is already on it.
  const planOptions = (
    plans.data?.plans ?? [
      { key: organization.planKey, name: organization.planName, organizations: 0 },
    ]
  ).filter((p) => !HIDDEN_PLANS.includes(p.key) || p.key === organization.planKey);

  return (
    <>
      <div className="flex flex-col gap-3">
        {back}
        <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-4">
          <div className="flex min-w-0 flex-col gap-2">
            <h1 className="text-headline leading-[1.1] font-semibold tracking-[-0.02em] break-words sm:text-display">
              {organization.name}
            </h1>
            <div className="flex flex-wrap items-center gap-3">
              <OrganizationStatusBadge organization={organization} />
              <span className="font-mono text-[13px] text-muted-foreground">
                {organization.slug}
              </span>
            </div>
          </div>
          {!deleted && protectedReason && (
            <p className="max-w-[280px] text-caption text-muted-foreground">{protectedReason}</p>
          )}
          {!deleted && !protectedReason && (
            <div className="flex flex-wrap gap-2">
              <Button
                variant="secondary"
                onClick={() => setConfirm(suspended ? "unsuspend" : "suspend")}
              >
                {suspended ? "Unsuspend" : "Suspend"}
              </Button>
              <Button variant="ghost-destructive" onClick={() => setConfirm("delete")}>
                Delete
              </Button>
            </div>
          )}
        </div>
      </div>

      <MetricStrip>
        <Metric label="Users" value={organization.users} />
        <Metric label="Monitors" value={organization.monitors} />
        <Metric label="Watchdogs" value={organization.heartbeats} />
        <Metric
          label="Created"
          value={formatDate(organization.createdAt)}
          note={
            organization.deletedAt ? `Deleted ${formatDateTime(organization.deletedAt)}` : undefined
          }
        />
      </MetricStrip>

      <dl className="grid gap-x-6 gap-y-1 sm:grid-cols-[140px_minmax(0,1fr)]">
        <dt className="text-caption text-muted-foreground">Organization id</dt>
        <dd className="font-mono text-[13px] break-all">{organization.id}</dd>
      </dl>

      {deleted ? (
        <Alert tone="neutral" title={`Plan: ${organization.planName}.`}>
          A deleted organization cannot be changed; its data is kept (see the admin runbook to
          restore it).
        </Alert>
      ) : (
        <form onSubmit={onSavePlan} className="flex max-w-[560px] flex-col gap-2">
          <Field
            label="Plan"
            hint="Existing monitors are kept when the new plan allows fewer; only new ones are refused."
            error={planError}
          >
            {(c) => (
              <div className="flex gap-2">
                <Select
                  {...c}
                  className="flex-1"
                  value={plan}
                  onChange={(e) => {
                    setPlan(e.target.value);
                    setPlanError(undefined);
                  }}
                >
                  {planOptions.map((p) => (
                    <option key={p.key} value={p.key}>
                      {p.name}
                    </option>
                  ))}
                </Select>
                <Button
                  type="submit"
                  variant="secondary"
                  busy={update.isPending && plan !== organization.planKey}
                  disabled={update.isPending || plan === organization.planKey}
                >
                  Save
                </Button>
              </div>
            )}
          </Field>
        </form>
      )}

      {confirm === "suspend" && (
        <ConfirmDialog
          title={`Suspend ${organization.name}?`}
          confirmLabel="Suspend organization"
          busy={update.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => update.mutate({ status: "suspended" })}
        >
          <p>
            Its users cannot sign in and their sessions end. Its monitors are not checked, its
            watchdogs are not evaluated and its Slack alerts wait. No data is deleted; you can
            unsuspend it later.
          </p>
        </ConfirmDialog>
      )}
      {confirm === "unsuspend" && (
        <ConfirmDialog
          title={`Unsuspend ${organization.name}?`}
          confirmLabel="Unsuspend organization"
          confirmVariant="primary"
          busy={update.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => update.mutate({ status: "active" })}
        >
          <p>
            Its users can sign in again and its monitors are checked again within a minute. Slack
            alerts that waited up to 15 minutes are sent now, older ones are dropped, and watchdogs
            that missed their deadline meanwhile go down until their next ping.
          </p>
        </ConfirmDialog>
      )}
      {confirm === "delete" && (
        <ConfirmDialog
          title={`Delete ${organization.name}?`}
          confirmLabel="Delete organization"
          busy={remove.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => remove.mutate()}
        >
          <p>
            The organization is marked deleted: its {organization.users} users cannot sign in, its{" "}
            {organization.monitors} monitors and {organization.heartbeats} watchdogs are not checked
            and it leaves the organization list. Its data is kept, but the panel cannot undo this.
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

function OrganizationUsers({ organization }: { organization: AdminOrganization }) {
  const toast = useToast();
  const users = useQuery({
    queryKey: queryKeys.adminUsers(organization.id),
    queryFn: () => listAdminOrganizationUsers(organization.id),
  });
  const [confirmUser, setConfirmUser] = useState<AdminUser | null>(null);
  // The temporary password lives only here: never in storage, gone when hidden or the page is
  // left. The mutation keeps no copy: gcTime 0 and reset() right after copying it.
  const [temporary, setTemporary] = useState<{ login: string; password: string } | null>(null);

  const reset = useMutation({
    mutationFn: (user: AdminUser) => resetAdminUserPassword(user.id),
    gcTime: 0,
    onSuccess: (res, user) => {
      setConfirmUser(null);
      setTemporary({ login: user.login, password: res.temporaryPassword });
      reset.reset();
    },
    onError: (err) => {
      setConfirmUser(null);
      toast(errorMessage(err), "error");
    },
  });

  return (
    <section aria-labelledby="users-heading" className="flex flex-col gap-3">
      <h2 id="users-heading" className="text-lead font-semibold">
        Users
      </h2>
      {temporary && (
        <TemporaryPasswordPanel login={temporary.login} password={temporary.password}>
          <Button
            variant="secondary"
            onClick={() => {
              setTemporary(null);
              reset.reset();
            }}
          >
            I handed it over, hide it
          </Button>
        </TemporaryPasswordPanel>
      )}
      {users.isPending && (
        <Loading label="Loading users…">
          <Skeleton className="w-1/2" />
        </Loading>
      )}
      {users.isError && (
        <ErrorState
          variant="section"
          title="Cannot load the users"
          onRetry={() => void users.refetch()}
        />
      )}
      {users.data && users.data.items.length === 0 && (
        <EmptyNote>This organization has no users.</EmptyNote>
      )}
      {users.data && users.data.items.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>Login</Th>
              <Th>Role</Th>
              <Th>Status</Th>
              <Th numeric>Created</Th>
              <Th numeric>
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {users.data.items.map((u) => (
              <Tr key={u.id}>
                <Td>
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-[13px] break-all">{u.login}</span>
                    {u.isPlatformAdmin && <Badge tone="inverse">System admin</Badge>}
                  </span>
                </Td>
                <Td>{u.role}</Td>
                <Td className={u.status === "disabled" ? "text-subtle-foreground" : undefined}>
                  {u.status}
                </Td>
                <Td numeric>{formatDate(u.createdAt)}</Td>
                <Td numeric>
                  <Button
                    variant="secondary"
                    size="small"
                    disabled={u.isPlatformAdmin}
                    aria-describedby={u.isPlatformAdmin ? `system-admin-note-${u.id}` : undefined}
                    onClick={() => setConfirmUser(u)}
                  >
                    Reset password
                  </Button>
                  {u.isPlatformAdmin && (
                    <span
                      id={`system-admin-note-${u.id}`}
                      className="mt-1 block font-sans text-caption text-muted-foreground"
                    >
                      Managed by the proreta-admin Secret, not here.
                    </span>
                  )}
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
      {users.data && users.data.total > users.data.items.length && (
        <p className="text-caption text-muted-foreground">
          Showing the first {users.data.items.length} of {users.data.total} users.
        </p>
      )}
      {confirmUser && (
        <ConfirmDialog
          title={`Reset the password of ${confirmUser.login}?`}
          confirmLabel="Reset password"
          confirmVariant="primary"
          busy={reset.isPending}
          onCancel={() => setConfirmUser(null)}
          onConfirm={() => reset.mutate(confirmUser)}
        >
          <p>
            The current password stops working and all of the user&apos;s sessions end. You get a
            temporary password once, to hand over yourself; the user must choose a new password at
            the next sign-in. Verify who asked for it first.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}
