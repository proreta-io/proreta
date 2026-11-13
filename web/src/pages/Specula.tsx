import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { errorMessage, errorMeta } from "../api/client";
import {
  createSpecula,
  listSpecula,
  queryKeys,
  revokeSpecula,
  updateSpecula,
  type Project,
  type Specula,
  type SpeculaCreated,
} from "../api/endpoints";
import { useToast } from "../components/Toasts";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  CopyField,
  EmptyNote,
  ErrorState,
  Field,
  Input,
  Loading,
  Segmented,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
  type BadgeTone,
} from "../components/ui";
import { formatAgo } from "../lib/format";
import { usePermissions } from "../lib/permissions";
import { countOf, useUsage } from "../lib/usage";
import {
  emptySpeculaForm,
  mapSpeculaErrors,
  runSettings,
  sharedWith,
  SPECULA_NAME_MAX,
  STATUS_HINTS,
  STATUS_LABELS,
  toSpeculaRequest,
  validateSpeculaForm,
  valuesFromSpecula,
  type SpeculaFormErrors,
  type SpeculaFormValues,
} from "../lib/specula";
import { useNow } from "../lib/useNow";
import { useProjectScope } from "../lib/useProjectScope";

const STATUS_TONES: Record<Specula["status"], BadgeTone> = {
  pending: "neutral",
  online: "success",
  offline: "warning",
  revoked: "deleted",
};

const codeBlock =
  "m-0 overflow-x-auto border border-border-faint bg-muted px-4 py-3.5 font-mono text-mono leading-[1.65] whitespace-pre";

/** Specula executors: programs that check services inside your own network. */
export default function SpeculaPage() {
  // Registering, editing and revoking are organization-level; reading follows the project roles.
  const manage = usePermissions().org("specula.manage");
  const { projects } = useProjectScope();
  const list = useQuery({
    queryKey: [...queryKeys.specula, manage],
    queryFn: () => listSpecula(undefined, manage),
  });
  const usage = useUsage().data;
  const limit = usage?.limits.specula;
  const full = limit !== undefined && usage!.usage.specula >= limit;
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Specula | null>(null);
  const [created, setCreated] = useState<SpeculaCreated | null>(null);
  const items = list.data?.items ?? [];

  return (
    <section className="flex max-w-[960px] flex-col gap-6">
      <div className="flex flex-col gap-2 pt-6 sm:pt-10">
        <h1 className="text-headline leading-[1.1] font-semibold tracking-[-0.02em] sm:text-display">
          Specula
        </h1>
        <p className="text-muted-foreground">
          A Specula runs inside your own network and checks services that are not reachable from the
          internet. It only makes outbound HTTPS calls to Proreta. Share it with all projects or
          with chosen ones, then pick it under &ldquo;Run from&rdquo; when you add a monitor.
        </p>
      </div>

      {created && <TokenPanel created={created} onDone={() => setCreated(null)} />}

      {manage && !created && !editing && (
        <div>
          {creating ? (
            <SpeculaForm
              key="new"
              mode="create"
              initial={emptySpeculaForm}
              projects={projects.data?.items}
              onCancel={() => setCreating(false)}
              onSaved={(c) => {
                setCreating(false);
                setCreated(c ?? null);
              }}
            />
          ) : (
            <div className="flex flex-col items-start gap-2">
              <Button disabled={full} onClick={() => setCreating(true)}>
                Register a Specula
              </Button>
              {limit !== undefined && (
                <span className="text-caption text-muted-foreground">
                  {full
                    ? `Your plan allows ${countOf(limit, "Specula", "Specula")}, and the limit is reached. Revoke one to register another.`
                    : `${usage!.usage.specula} of ${limit} Specula on your plan.`}
                </span>
              )}
            </div>
          )}
        </div>
      )}

      {editing && (
        <SpeculaForm
          key={editing.id}
          mode="edit"
          id={editing.id}
          initial={valuesFromSpecula(editing)}
          projects={projects.data?.items}
          onCancel={() => setEditing(null)}
          onSaved={() => setEditing(null)}
        />
      )}

      {list.isPending ? (
        <Loading label="Loading Specula…">
          <Skeleton className="w-2/3" />
          <Skeleton className="w-1/2" />
        </Loading>
      ) : list.isError ? (
        <ErrorState
          title="Cannot load Specula"
          detail={errorMessage(list.error)}
          meta={errorMeta(list.error)}
          onRetry={() => void list.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyNote>
          {manage
            ? "No Specula yet. Register one to monitor services inside your network."
            : "No Specula is shared with your projects."}
        </EmptyNote>
      ) : (
        <SpeculaTable
          items={items}
          projects={projects.data?.items}
          manage={manage}
          onEdit={(s) => {
            setCreating(false);
            setEditing(s);
          }}
        />
      )}
    </section>
  );
}

function SpeculaTable({
  items,
  projects,
  manage,
  onEdit,
}: {
  items: Specula[];
  projects: Project[] | undefined;
  manage: boolean;
  onEdit: (s: Specula) => void;
}) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const now = useNow(15_000);
  const [revoking, setRevoking] = useState<Specula | null>(null);
  const revoke = useMutation({
    mutationFn: (s: Specula) => revokeSpecula(s.id),
    onSuccess: (_, s) => {
      setRevoking(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.specula });
      void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
      void queryClient.invalidateQueries({ queryKey: queryKeys.monitors });
      toast(`${s.name} revoked`);
    },
    onError: (err) => {
      setRevoking(null);
      toast(`Revoke failed. ${errorMessage(err)}`, "error");
    },
  });

  return (
    <>
      <Table>
        <thead>
          <tr>
            <Th>Name</Th>
            <Th>Status</Th>
            <Th className="max-sm:hidden">Version</Th>
            <Th className="max-sm:hidden">Last seen</Th>
            <Th className="max-md:hidden">Shared with</Th>
            {manage && (
              <Th>
                <span className="sr-only">Actions</span>
              </Th>
            )}
          </tr>
        </thead>
        <tbody>
          {items.map((s) => (
            <Tr key={s.id}>
              <Td>
                <span className="font-medium break-words">{s.name}</span>
                {s.hostname && (
                  <span className="block font-mono text-caption text-muted-foreground max-sm:hidden">
                    {s.hostname}
                  </span>
                )}
              </Td>
              <Td>
                <span title={STATUS_HINTS[s.status]}>
                  <Badge tone={STATUS_TONES[s.status]}>{STATUS_LABELS[s.status]}</Badge>
                </span>
              </Td>
              <Td className="font-mono text-caption max-sm:hidden">{s.version || "-"}</Td>
              <Td className="text-caption max-sm:hidden">
                {s.lastSeenAt ? formatAgo(s.lastSeenAt, now) : "never"}
              </Td>
              <Td className="text-caption max-md:hidden">{sharedWith(s, projects)}</Td>
              {manage && (
                <Td>
                  {s.status !== "revoked" && (
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="small"
                        aria-label={`Edit ${s.name}`}
                        onClick={() => onEdit(s)}
                      >
                        Edit
                      </Button>
                      <Button
                        variant="ghost-destructive"
                        size="small"
                        aria-label={`Revoke ${s.name}`}
                        onClick={() => setRevoking(s)}
                      >
                        Revoke
                      </Button>
                    </div>
                  )}
                </Td>
              )}
            </Tr>
          ))}
        </tbody>
      </Table>
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          confirmLabel="Revoke"
          busy={revoke.isPending}
          onConfirm={() => revoke.mutate(revoking)}
          onCancel={() => setRevoking(null)}
        >
          The executor stops working at once and its token cannot be used again. Monitors that run
          there stop being checked until you move them to another location. This cannot be undone.
        </ConfirmDialog>
      )}
    </>
  );
}

function SpeculaForm({
  mode,
  id,
  initial,
  projects,
  onCancel,
  onSaved,
}: {
  mode: "create" | "edit";
  id?: string;
  initial: SpeculaFormValues;
  projects: Project[] | undefined;
  onCancel: () => void;
  onSaved: (created?: SpeculaCreated) => void;
}) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<SpeculaFormErrors>({});
  const [general, setGeneral] = useState<string | undefined>();

  const save = useMutation({
    mutationFn: (v: SpeculaFormValues): Promise<SpeculaCreated | undefined> =>
      mode === "create"
        ? createSpecula(toSpeculaRequest(v))
        : updateSpecula(id!, toSpeculaRequest(v)).then(() => undefined),
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.specula });
      void queryClient.invalidateQueries({ queryKey: queryKeys.monitors });
      void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
      if (mode === "create") onSaved(saved);
      else {
        toast("Changes saved");
        onSaved();
      }
    },
    onError: (err) => {
      const mapped = mapSpeculaErrors(err);
      setErrors(mapped.fields);
      setGeneral(mapped.general);
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const found = validateSpeculaForm(values);
    setErrors(found);
    setGeneral(undefined);
    if (Object.keys(found).length > 0) return;
    save.mutate(values);
  }

  const projectList = projects ?? [];
  return (
    <form
      onSubmit={onSubmit}
      noValidate
      aria-label={mode === "create" ? "Register a Specula" : "Edit the Specula"}
      className="flex max-w-[560px] flex-col gap-5 border border-border p-5"
    >
      <h2 className="text-lead font-semibold">
        {mode === "create" ? "Register a Specula" : `Edit ${initial.name}`}
      </h2>
      {general && (
        <Alert title={mode === "create" ? "Could not register it." : "Could not save."}>
          {general}
        </Alert>
      )}
      <fieldset disabled={save.isPending} className="flex min-w-0 flex-col gap-5">
        <Field
          label="Name"
          hint="For example the cluster or network it runs in."
          error={errors.name}
        >
          {(c) => (
            <Input
              {...c}
              name="speculaName"
              autoComplete="off"
              maxLength={SPECULA_NAME_MAX * 2}
              placeholder="Production cluster"
              value={values.name}
              onChange={(e) => {
                setValues((v) => ({ ...v, name: e.target.value }));
                setErrors((x) => ({ ...x, name: undefined }));
              }}
            />
          )}
        </Field>

        <div className="flex flex-col gap-2">
          <span className="text-caption font-medium">Who can use it</span>
          <Segmented
            label="Who can use it"
            name="speculaSharing"
            value={values.allProjects ? "all" : "chosen"}
            onChange={(v) => {
              setValues((x) => ({ ...x, allProjects: v === "all" }));
              setErrors((x) => ({ ...x, projectIds: undefined }));
            }}
            options={[
              { value: "all", label: "All projects" },
              { value: "chosen", label: "Chosen projects" },
            ]}
          />
          {!values.allProjects && (
            <fieldset className="flex flex-col gap-1" aria-label="Projects">
              {projectList.map((p) => (
                <Checkbox
                  key={p.id}
                  name="speculaProjects"
                  value={p.id}
                  label={p.name}
                  checked={values.projectIds.includes(p.id)}
                  onChange={(e) => {
                    setValues((x) => ({
                      ...x,
                      projectIds: e.target.checked
                        ? [...x.projectIds, p.id]
                        : x.projectIds.filter((id) => id !== p.id),
                    }));
                    setErrors((x) => ({ ...x, projectIds: undefined }));
                  }}
                />
              ))}
              {errors.projectIds && (
                <span role="alert" className="text-caption text-down">
                  {errors.projectIds}
                </span>
              )}
            </fieldset>
          )}
        </div>
      </fieldset>
      <div className="flex flex-col gap-2 sm:flex-row">
        <Button type="submit" busy={save.isPending}>
          {mode === "create" ? "Register" : "Save changes"}
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** The one-time token after registering: Proreta keeps only a hash and cannot show it again. */
function TokenPanel({ created, onDone }: { created: SpeculaCreated; onDone: () => void }) {
  const url = window.location.origin;
  return (
    <section
      aria-labelledby="specula-token-heading"
      className="flex max-w-[640px] flex-col gap-5 border border-border p-5"
    >
      <h2 id="specula-token-heading" className="text-lead font-semibold">
        {created.specula.name} is registered
      </h2>
      <CopyField label="Token" value={created.token} copyLabel="Copy token" once />
      <p className="text-muted-foreground">
        Proreta keeps only a hash of the token and cannot show it again. If you lose it, revoke this
        Specula and register a new one. Treat it like a password: whoever has it can claim this
        Specula&apos;s checks and receives the headers and bodies of its monitors.
      </p>
      <div className="flex flex-col gap-2.5">
        <h3 className="text-[14px] font-semibold">Settings of the executor</h3>
        <p className="text-muted-foreground">
          Start the Specula executor inside your network with these environment variables. It needs
          to reach Proreta over HTTPS and the services you want to check.
        </p>
        <pre aria-label="Settings of the executor" className={codeBlock}>
          <code>{runSettings(url, created.token)}</code>
        </pre>
      </div>
      <div>
        <Button onClick={onDone}>I saved the token</Button>
      </div>
    </section>
  );
}
