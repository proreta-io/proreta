import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  deleteAdminOrganization,
  getAdminStats,
  getAdminOrganization,
  queryKeys,
  updateAdminOrganization,
  type AdminOrganization,
  type AdminOrganizationUpdate,
} from "../../api/endpoints";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/Toasts";
import {
  Alert,
  Button,
  buttonClass,
  ConfirmDialog,
  ErrorState,
  Field,
  Metric,
  MetricStrip,
  Select,
  Skeleton,
  Loading,
} from "../../components/ui";
import { formatDate, formatDateTime } from "../../lib/format";
import AdminOrganizationInvitations from "./AdminOrganizationInvitations";
import AdminOrganizationMembers from "./AdminOrganizationMembers";
import AdminOrganizationProjects from "./AdminOrganizationProjects";
import AdminOrganizationSettings from "./AdminOrganizationSettings";
import { OrganizationStatusBadge } from "./OrganizationStatusBadge";

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
      <AdminOrganizationMembers organization={organization.data} />
      <AdminOrganizationInvitations organization={organization.data} />
      <AdminOrganizationProjects organization={organization.data} />
      <AdminOrganizationSettings organization={organization.data} />
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

  const planOptions = plans.data?.plans ?? [
    { key: organization.planKey, name: organization.planName, organizations: 0 },
  ];

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
            hint="Existing monitors are kept when the new plan allows fewer; only new ones are refused. Community removes all quotas."
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
