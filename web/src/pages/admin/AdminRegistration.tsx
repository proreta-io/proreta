import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type SubmitEvent } from "react";
import { errorMessage, errorMeta } from "../../api/client";
import {
  getAdminRegistration,
  queryKeys,
  updateAdminRegistration,
  type AdminRegistrationSettings,
} from "../../api/endpoints";
import { useToast } from "../../components/Toasts";
import { Alert, Button, ErrorState, Field, Loading, Select, Skeleton } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { adminTitle } from "./AdminOverview";

type Choice = "open" | "closed" | "environment";

const toChoice = (s: AdminRegistrationSettings): Choice =>
  s.source === "environment" ? "environment" : s.effective ? "open" : "closed";

const toValue = (c: Choice): boolean | null => (c === "environment" ? null : c === "open");

/** Self-service registration switch, changed by the platform admin without a redeploy. */
export default function AdminRegistration() {
  const settings = useQuery({
    queryKey: queryKeys.adminRegistration,
    queryFn: getAdminRegistration,
  });
  return (
    <section className="flex flex-col gap-8">
      <h1 className={adminTitle}>Registration</h1>
      {settings.isPending && (
        <Loading label="Loading the registration setting…">
          <Skeleton className="h-24 w-full" />
        </Loading>
      )}
      {settings.isError && (
        <ErrorState
          title="Cannot load the registration setting"
          detail={errorMessage(settings.error)}
          meta={errorMeta(settings.error)}
          onRetry={() => void settings.refetch()}
        />
      )}
      {settings.data && <RegistrationForm key={settings.data.updatedAt} settings={settings.data} />}
    </section>
  );
}

function RegistrationForm({ settings }: { settings: AdminRegistrationSettings }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const [choice, setChoice] = useState<Choice>(toChoice(settings));
  const save = useMutation({
    mutationFn: () => updateAdminRegistration(toValue(choice)),
    onSuccess: (next) => {
      queryClient.setQueryData(queryKeys.adminRegistration, next);
      // The sign-in and registration pages read the public config.
      void queryClient.invalidateQueries({ queryKey: queryKeys.publicConfig });
      void queryClient.invalidateQueries({ queryKey: queryKeys.authProviders });
      toast("Registration setting saved.");
    },
  });
  const submit = (e: SubmitEvent) => {
    e.preventDefault();
    save.mutate();
  };
  const environment = settings.environmentValue ? "open" : "closed";
  return (
    <div className="flex max-w-[640px] flex-col gap-6">
      <p className="text-muted-foreground">
        Self-service registration lets anyone create an organization of their own, with a password
        or with Google or GitHub. Invitation links, claimed Workspace domains and existing users
        work whatever you choose here. A choice made here wins over the installation&apos;s
        configuration and applies without a redeploy (within a few seconds on every replica).
      </p>
      <p>
        Registration is currently <strong>{settings.effective ? "open" : "closed"}</strong>
        {settings.source === "database"
          ? `, set here${settings.updatedAt ? ` on ${formatDateTime(settings.updatedAt)}` : ""}.`
          : ", following the installation's configuration."}
      </p>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <Field label="Registration">
          {(c) => (
            <Select {...c} value={choice} onChange={(e) => setChoice(e.target.value as Choice)}>
              <option value="open">Open</option>
              <option value="closed">Closed</option>
              <option value="environment">{`Follow the installation's configuration (${environment})`}</option>
            </Select>
          )}
        </Field>
        <div>
          <Button type="submit" busy={save.isPending} disabled={choice === toChoice(settings)}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </form>
    </div>
  );
}
