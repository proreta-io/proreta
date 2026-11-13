import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  getAdminRetention,
  queryKeys,
  updateAdminRetention,
  type AdminRetentionSettings,
} from "../../api/endpoints";
import { useToast } from "../../components/Toasts";
import { Alert, Button, ErrorState, Field, Input, Loading, Skeleton } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { adminTitle } from "./AdminOverview";

const DEFAULT_DAYS = 30;
const DEFAULT_MONTHS = 13;

/** What the form field holds: empty means the default. */
const shown = (value: number, isDefault: boolean) => (isDefault ? "" : String(value));
const parse = (text: string): number | null => (text.trim() === "" ? null : Number(text));

/** How long finished notification deliveries and hourly rollups are kept. */
export default function AdminRetention() {
  const settings = useQuery({ queryKey: queryKeys.adminRetention, queryFn: getAdminRetention });
  return (
    <section className="flex flex-col gap-8">
      <h1 className={adminTitle}>Retention</h1>
      {settings.isPending && (
        <Loading label="Loading the retention settings…">
          <Skeleton className="h-24 w-full" />
        </Loading>
      )}
      {settings.isError && (
        <ErrorState
          title="Cannot load the retention settings"
          detail={errorMessage(settings.error)}
          meta={errorMeta(settings.error)}
          onRetry={() => void settings.refetch()}
        />
      )}
      {settings.data && <RetentionForm key={settings.data.updatedAt} settings={settings.data} />}
    </section>
  );
}

function RetentionForm({ settings }: { settings: AdminRetentionSettings }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const [days, setDays] = useState(shown(settings.deliveryDays, settings.deliveryDefault));
  const [months, setMonths] = useState(shown(settings.rollupMonths, settings.rollupDefault));
  const save = useMutation({
    mutationFn: () => updateAdminRetention(parse(days), parse(months)),
    onSuccess: (next) => {
      queryClient.setQueryData(queryKeys.adminRetention, next);
      toast("Retention settings saved.");
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  const fields = save.error instanceof ApiError ? save.error.fieldErrorMap() : {};
  const unchanged =
    days === shown(settings.deliveryDays, settings.deliveryDefault) &&
    months === shown(settings.rollupMonths, settings.rollupDefault);
  const origin = (isDefault: boolean) => (isDefault ? "the default" : "set here");
  return (
    <div className="flex max-w-[640px] flex-col gap-6">
      <p className="text-muted-foreground">
        The jobs worker deletes old data so these tables do not grow forever. Finished notification
        deliveries (sent or failed) are deleted once they are older than the first period; hourly
        rollups of check results, which feed the charts and the status page history, once they are
        older than the second. Pending and in-progress deliveries are never deleted. The audit log
        and incidents are kept and never purged. A change applies at the next run of the worker
        (within a few hours), and deleted rows cannot be restored.
      </p>
      <p>
        Deliveries are kept <strong>{settings.deliveryDays} days</strong> (
        {origin(settings.deliveryDefault)}), rollups <strong>{settings.rollupMonths} months</strong>{" "}
        ({origin(settings.rollupDefault)})
        {settings.updatedAt ? `; last changed on ${formatDateTime(settings.updatedAt)}` : ""}.
      </p>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.error && Object.keys(fields).length === 0 && (
          <Alert>{errorMessage(save.error)}</Alert>
        )}
        <Field
          label="Keep finished deliveries for (days)"
          hint={`7 to 365. Leave empty for the default, ${DEFAULT_DAYS} days.`}
          error={fields.deliveryDays}
        >
          {(c) => (
            <Input
              {...c}
              name="deliveryDays"
              type="number"
              inputMode="numeric"
              min={7}
              max={365}
              placeholder={String(DEFAULT_DAYS)}
              value={days}
              onChange={(e) => setDays(e.target.value)}
            />
          )}
        </Field>
        <Field
          label="Keep hourly rollups for (months)"
          hint={`4 to 60; the status page shows 90 days. Leave empty for the default, ${DEFAULT_MONTHS} months.`}
          error={fields.rollupMonths}
        >
          {(c) => (
            <Input
              {...c}
              name="rollupMonths"
              type="number"
              inputMode="numeric"
              min={4}
              max={60}
              placeholder={String(DEFAULT_MONTHS)}
              value={months}
              onChange={(e) => setMonths(e.target.value)}
            />
          )}
        </Field>
        <div>
          <Button type="submit" busy={save.isPending} disabled={unchanged}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </form>
    </div>
  );
}
