import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type SubmitEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  createAdminPlan,
  deleteAdminPlan,
  getAdminPlan,
  queryKeys,
  updateAdminPlan,
  type AdminPlan,
  type ResourceCounts,
} from "../../api/endpoints";
import { useToast } from "../../components/Toasts";
import {
  Alert,
  Button,
  buttonClass,
  Checkbox,
  ConfirmDialog,
  ErrorState,
  Field,
  Input,
  Loading,
  ReadOnlyValue,
  Skeleton,
} from "../../components/ui";
import { isUnlimited, UNLIMITED } from "../../lib/limits";
import { LIMITS } from "../../lib/planLimits";
import { adminTitle } from "./AdminOverview";

type Draft = {
  name: string;
  limits: Record<keyof ResourceCounts, { unlimited: boolean; value: string }>;
};

/** A new plan starts with empty limits: each one is a decision. */
const fromPlan = (limits?: ResourceCounts): Draft["limits"] =>
  Object.fromEntries(
    LIMITS.map((l) => {
      const n = limits?.[l.field];
      return [
        l.field,
        {
          unlimited: n !== undefined && isUnlimited(n),
          value: n === undefined || isUnlimited(n) ? "" : String(n),
        },
      ];
    }),
  ) as Draft["limits"];

/** The number a row holds; NaN while the text is not a whole number from 0 up. */
const numberOf = (row: { unlimited: boolean; value: string }) =>
  row.unlimited ? UNLIMITED : /^\d{1,10}$/.test(row.value.trim()) ? Number(row.value) : NaN;

/** Creates a plan (/admin/plans/new) or edits one (/admin/plans/:key). */
export default function AdminPlanForm() {
  const { key } = useParams();
  const plan = useQuery({
    queryKey: queryKeys.adminPlan(key ?? ""),
    queryFn: () => getAdminPlan(key ?? ""),
    enabled: key !== undefined,
  });
  return (
    <section className="flex flex-col gap-6">
      <div>
        <Link to="/admin/plans" className="text-caption text-muted-foreground hover:underline">
          ← Plans
        </Link>
        <h1 className={adminTitle}>{key ? (plan.data?.name ?? "Plan") : "New plan"}</h1>
      </div>
      {key === undefined && <PlanForm />}
      {key !== undefined && plan.isPending && (
        <Loading label="Loading the plan…">
          <Skeleton className="h-24 w-full" />
        </Loading>
      )}
      {plan.isError && (
        <ErrorState
          title="Cannot load the plan"
          detail={errorMessage(plan.error)}
          meta={errorMeta(plan.error)}
          onRetry={() => void plan.refetch()}
        />
      )}
      {plan.data && <PlanForm key={plan.dataUpdatedAt} plan={plan.data} />}
    </section>
  );
}

function PlanForm({ plan }: { plan?: AdminPlan }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const [planKey, setPlanKey] = useState("");
  const [draft, setDraft] = useState<Draft>({
    name: plan?.name ?? "",
    limits: fromPlan(plan?.limits),
  });
  const [confirm, setConfirm] = useState<"save" | "delete" | null>(null);
  const [clientErrors, setClientErrors] = useState<Record<string, string>>({});

  const done = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminPlans });
    // The organization page picks its plan from the stats.
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminStats });
  };
  const body = () => ({
    name: draft.name.trim(),
    limits: Object.fromEntries(
      LIMITS.map((l) => [l.field, numberOf(draft.limits[l.field])]),
    ) as ResourceCounts,
  });
  const save = useMutation({
    mutationFn: () =>
      plan
        ? updateAdminPlan(plan.key, body())
        : createAdminPlan({ key: planKey.trim(), ...body() }),
    onSuccess: (saved) => {
      setConfirm(null);
      done();
      toast(plan ? `Plan ${saved.name} saved.` : `Plan ${saved.name} created.`);
      void navigate("/admin/plans");
    },
    onError: () => setConfirm(null),
  });
  const remove = useMutation({
    mutationFn: () => deleteAdminPlan(plan?.key ?? ""),
    onSuccess: () => {
      setConfirm(null);
      done();
      toast(`Plan ${plan?.name} deleted.`);
      void navigate("/admin/plans");
    },
    onError: () => setConfirm(null),
  });

  // Limits lowered below what an organization on the plan uses now (edit only).
  const below = plan?.peakUsage
    ? LIMITS.filter((l) => {
        const n = numberOf(draft.limits[l.field]);
        return n < (plan.peakUsage?.[l.field] ?? 0);
      })
    : [];

  const submit = (e: SubmitEvent<HTMLFormElement>) => {
    e.preventDefault();
    const errors: Record<string, string> = {};
    if (!draft.name.trim()) errors.name = "Enter a name.";
    for (const l of LIMITS)
      if (Number.isNaN(numberOf(draft.limits[l.field])))
        errors[`limits.${l.field}`] = "Enter a whole number from 0 up, or choose Unlimited.";
    setClientErrors(errors);
    if (Object.keys(errors).length > 0) return;
    if (below.length > 0) setConfirm("save");
    else save.mutate();
  };

  const server = save.error instanceof ApiError ? save.error.fieldErrorMap() : {};
  const fields = { ...server, ...clientErrors };
  const general = [save.error, remove.error].find(
    (err) => err && !(err instanceof ApiError && Object.keys(err.fieldErrorMap()).length > 0),
  );
  const setRow = (field: keyof ResourceCounts, row: Partial<Draft["limits"][typeof field]>) =>
    setDraft((d) => ({ ...d, limits: { ...d.limits, [field]: { ...d.limits[field], ...row } } }));

  return (
    <form onSubmit={submit} noValidate className="flex max-w-[640px] flex-col gap-6">
      {plan ? (
        <p className="text-muted-foreground">
          {plan.organizations === 0
            ? "No organization is on this plan."
            : `${plan.organizations} ${plan.organizations === 1 ? "organization is" : "organizations are"} on this plan. A change applies to all of them at once.`}{" "}
          Existing resources are never deleted: where a limit is lowered below what an organization
          has, only new ones are refused.
        </p>
      ) : (
        <p className="text-muted-foreground">
          A custom plan can be assigned to an organization on its page. Every limit needs a value.
        </p>
      )}
      {general && <Alert>{errorMessage(general)}</Alert>}
      {plan ? (
        <div className="flex flex-col gap-1.5">
          <span className="text-caption font-medium">Key</span>
          <ReadOnlyValue note="Never changes">{plan.key}</ReadOnlyValue>
        </div>
      ) : (
        <Field
          label="Key"
          hint="Lowercase letters, digits and hyphens, up to 63 characters. It never changes."
          error={fields.key}
        >
          {(c) => (
            <Input
              {...c}
              name="key"
              autoComplete="off"
              spellCheck={false}
              maxLength={63}
              value={planKey}
              onChange={(e) => setPlanKey(e.target.value)}
            />
          )}
        </Field>
      )}
      <Field label="Name" error={fields.name}>
        {(c) => (
          <Input
            {...c}
            name="name"
            maxLength={200}
            value={draft.name}
            onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
          />
        )}
      </Field>
      <fieldset className="m-0 flex min-w-0 flex-col gap-5 border-0 p-0">
        <legend className="mb-3 p-0 text-caption font-medium">Limits</legend>
        {LIMITS.map((l) => {
          const row = draft.limits[l.field];
          const peak = plan?.peakUsage?.[l.field];
          return (
            <div key={l.field} className="flex flex-col gap-2 sm:flex-row sm:items-start sm:gap-5">
              <Field
                label={l.label}
                hint={
                  <>
                    {l.hint}
                    {peak !== undefined && plan && plan.organizations > 0 && (
                      <> The busiest organization on this plan uses {peak}.</>
                    )}
                  </>
                }
                error={fields[`limits.${l.field}`]}
                className="sm:flex-1"
              >
                {(c) => (
                  <Input
                    {...c}
                    name={`limit-${l.field}`}
                    type="number"
                    inputMode="numeric"
                    min={0}
                    step={1}
                    disabled={row.unlimited}
                    placeholder={row.unlimited ? "Unlimited" : undefined}
                    value={row.unlimited ? "" : row.value}
                    onChange={(e) => setRow(l.field, { value: e.target.value })}
                  />
                )}
              </Field>
              <Checkbox
                label="Unlimited"
                aria-label={`${l.label}: unlimited`}
                className="sm:mt-6"
                checked={row.unlimited}
                onChange={(e) => setRow(l.field, { unlimited: e.target.checked })}
              />
            </div>
          );
        })}
      </fieldset>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" busy={save.isPending}>
          {save.isPending ? "Saving…" : plan ? "Save" : "Create plan"}
        </Button>
        <Link to="/admin/plans" className={buttonClass("secondary", "default")}>
          Cancel
        </Link>
        {plan?.deletable && (
          <Button type="button" variant="destructive" onClick={() => setConfirm("delete")}>
            Delete plan
          </Button>
        )}
      </div>
      {plan && !plan.deletable && (
        <p className="text-caption text-muted-foreground">
          {plan.system
            ? "A system plan cannot be deleted."
            : "The plan cannot be deleted while an organization (a deleted one too) is on it."}
        </p>
      )}
      {confirm === "save" && (
        <ConfirmDialog
          title="Lower limits below current use?"
          confirmLabel="Save anyway"
          confirmVariant="primary"
          busy={save.isPending}
          onConfirm={() => save.mutate()}
          onCancel={() => setConfirm(null)}
        >
          <p>
            An organization on this plan already uses more than the new limit of{" "}
            {below
              .map((l) => `${l.label.toLowerCase()} (${plan?.peakUsage?.[l.field]} in use)`)
              .join(", ")}
            . It keeps what it has and cannot add more until it is under the limit.
          </p>
        </ConfirmDialog>
      )}
      {confirm === "delete" && plan && (
        <ConfirmDialog
          title={`Delete ${plan.name}?`}
          confirmLabel="Delete plan"
          busy={remove.isPending}
          onConfirm={() => remove.mutate()}
          onCancel={() => setConfirm(null)}
        >
          <p>No organization is on this plan. It cannot be restored; create it again if needed.</p>
        </ConfirmDialog>
      )}
    </form>
  );
}
