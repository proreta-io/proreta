import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type SubmitEvent } from "react";
import { ApiError, errorMessage, planLimitMessage } from "../api/client";
import {
  addStatusPageDomain,
  listStatusPageDomains,
  POLL_MS,
  queryKeys,
  removeStatusPageDomain,
  setPrimaryStatusPageDomain,
  verifyStatusPageDomain,
  type StatusPage as Page,
  type StatusPageDomain as Domain,
} from "../api/endpoints";
import { useToast } from "../components/Toasts";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  CopyField,
  DiamondShape,
  DotShape,
  EmptyNote,
  ErrorState,
  Field,
  HollowDotShape,
  Input,
  Loading,
  TriangleShape,
} from "../components/ui";
import { formatAgo, formatDate } from "../lib/format";
import { isUnlimited } from "../lib/limits";
import {
  checkError,
  checkResult,
  DOMAINS_PER_PAGE,
  ERROR_TEXT,
  isDomainsDisabled,
  isServing,
  nextStep,
  STATE_TEXT,
} from "../lib/statusPageDomains";
import { useNow } from "../lib/useNow";
import { useUsage } from "../lib/usage";

function StateBadge({ d }: { d: Domain }) {
  const text = STATE_TEXT[d.state];
  if (d.state === "active")
    return (
      <Badge tone="success" icon={<DotShape />}>
        {text}
      </Badge>
    );
  if (d.state === "verified")
    return (
      <Badge tone="warning" icon={<DiamondShape />}>
        {text}
      </Badge>
    );
  if (d.state === "failed")
    return (
      <Badge tone="danger" icon={<TriangleShape />}>
        {text}
      </Badge>
    );
  return (
    <Badge tone="neutral" icon={<HollowDotShape />}>
      {text}
    </Badge>
  );
}

/**
 * The "Custom domain" section of a status page: hidden when the installation has custom domains
 * off. Everybody who reads the page sees the list; `canManage` adds the actions and the form.
 */
export function DomainsSection({ page, canManage }: { page: Page; canManage: boolean }) {
  const usage = useUsage().data;
  const heading = useRef<HTMLHeadingElement>(null);
  const list = useQuery({
    queryKey: queryKeys.statusPageDomains(page.id),
    queryFn: () => listStatusPageDomains(page.id),
    // Checks run in the background: keep the states fresh while one is still unsettled.
    refetchInterval: (q) =>
      q.state.data?.items.some((d) => d.state === "pending" || d.state === "verified")
        ? POLL_MS
        : false,
  });
  if (list.isError && isDomainsDisabled(list.error)) return null;

  const items = list.data?.items ?? [];
  const limit = usage?.limits.statusPageDomains;
  const noPlan = limit === 0;

  return (
    <section aria-labelledby="domains-h" className="flex flex-col gap-3">
      <h2 id="domains-h" ref={heading} tabIndex={-1} className="text-lead font-semibold">
        Custom domain
      </h2>
      <p className="text-caption text-muted-foreground">
        Show this page on your own address, such as status.example.com. You prove the domain is
        yours with a DNS record, and point it at us with another. The page stays available at its
        Proreta address too.
      </p>
      {list.isPending ? (
        <Loading label="Loading domains…" />
      ) : list.isError ? (
        <ErrorState
          variant="section"
          title="Cannot load domains"
          detail={errorMessage(list.error)}
          onRetry={() => void list.refetch()}
        />
      ) : (
        <>
          {items.length === 0 && !(canManage && !noPlan) && (
            <EmptyNote>No custom domain.</EmptyNote>
          )}
          {items.length > 0 && (
            <ul className="flex flex-col border border-border">
              {items.map((d) => (
                <DomainRow
                  key={d.id}
                  page={page}
                  d={d}
                  others={items.length - 1}
                  canManage={canManage}
                  onRemoved={() => heading.current?.focus()}
                />
              ))}
            </ul>
          )}
          {canManage &&
            (noPlan ? (
              <p className="text-caption text-muted-foreground">
                Your plan does not include custom domains.
              </p>
            ) : items.length >= DOMAINS_PER_PAGE ? (
              <p className="text-caption text-muted-foreground">
                A status page can have at most {DOMAINS_PER_PAGE} domains. Remove one to add
                another.
              </p>
            ) : (
              <AddDomain
                page={page}
                count={items.length}
                hint={
                  limit !== undefined && !isUnlimited(limit)
                    ? `Your plan allows ${limit} custom domain${limit === 1 ? "" : "s"} in total; ${usage?.usage.statusPageDomains ?? 0} in use.`
                    : undefined
                }
              />
            ))}
        </>
      )}
    </section>
  );
}

function AddDomain({ page, count, hint }: { page: Page; count: number; hint?: string }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const [host, setHost] = useState("");
  const [error, setError] = useState<string>();
  const [formError, setFormError] = useState<string>();
  const add = useMutation({
    mutationFn: () => addStatusPageDomain(page.id, host.trim()),
    onSuccess: (d) => {
      setHost("");
      void queryClient.invalidateQueries({ queryKey: queryKeys.statusPageDomains(page.id) });
      void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
      toast(`${d.hostUnicode} added. Publish the DNS records shown below it.`);
    },
    onError: (err) => {
      const fe = err instanceof ApiError ? err.fieldErrorMap() : {};
      if (fe.host) setError(fe.host);
      else if (
        err instanceof ApiError &&
        err.status === 409 &&
        err.type.endsWith(":domain-unavailable")
      )
        setError("This host is not available.");
      else if (
        err instanceof ApiError &&
        err.limit === "statusPageDomains" &&
        count >= DOMAINS_PER_PAGE
      )
        setFormError(`A status page can have at most ${DOMAINS_PER_PAGE} domains.`);
      else setFormError(planLimitMessage(err) ?? errorMessage(err));
    },
  });

  function onSubmit(e: SubmitEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(undefined);
    const v = host.trim();
    const bad = !v
      ? "Enter a domain."
      : /[/:@\s]/.test(v)
        ? "Enter the host name only, such as status.example.com."
        : undefined;
    setError(bad);
    if (!bad) add.mutate();
  }

  return (
    <form
      onSubmit={onSubmit}
      noValidate
      aria-label="Add a domain"
      className="flex max-w-[560px] flex-col gap-3"
    >
      {formError && <Alert>{formError}</Alert>}
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start">
        <Field
          label="Domain"
          error={error}
          hint={hint ?? "A name below your own domain; the bare domain itself cannot be used."}
          className="sm:flex-1"
        >
          {(c) => (
            <Input
              {...c}
              mono
              autoComplete="off"
              inputMode="url"
              placeholder="status.example.com"
              value={host}
              onChange={(e) => {
                setHost(e.target.value);
                setError(undefined);
              }}
            />
          )}
        </Field>
        <Button type="submit" busy={add.isPending} className="sm:mt-[26px]">
          Add domain
        </Button>
      </div>
    </form>
  );
}

function DomainRow({
  page,
  d,
  others,
  canManage,
  onRemoved,
}: {
  page: Page;
  d: Domain;
  others: number;
  canManage: boolean;
  onRemoved: () => void;
}) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const now = useNow(30_000);
  const [notice, setNotice] = useState("");
  const [making, setMaking] = useState(false);
  const [removing, setRemoving] = useState(false);
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: queryKeys.statusPageDomains(page.id) });

  const verify = useMutation({
    mutationFn: () => verifyStatusPageDomain(page.id, d.id),
    onSuccess: (res) => {
      setNotice(checkResult(res));
      refresh();
    },
    onError: (err) => setNotice(checkError(err)),
  });
  const primary = useMutation({
    mutationFn: () => setPrimaryStatusPageDomain(page.id, d.id),
    onSuccess: () => {
      setMaking(false);
      refresh();
      toast(`${d.hostUnicode} is the primary domain`);
    },
    onError: (err) => {
      setMaking(false);
      toast(`Change failed. ${checkError(err)}`, "error");
    },
  });
  const remove = useMutation({
    mutationFn: () => removeStatusPageDomain(page.id, d.id),
    onSuccess: () => {
      setRemoving(false);
      refresh();
      void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
      toast(`${d.hostUnicode} removed`);
      onRemoved();
    },
    onError: (err) => {
      setRemoving(false);
      toast(`Remove failed. ${errorMessage(err)}`, "error");
    },
  });

  const expires = d.state === "pending" && d.expiresAt ? formatDate(d.expiresAt) : undefined;
  const needsCname = d.state === "pending" || d.state === "verified" || d.state === "failed";
  const needsTxt = d.state === "pending" || d.state === "failed";

  return (
    <li
      aria-label={d.hostUnicode}
      className="flex flex-col gap-3 border-t border-border-faint p-3 first:border-t-0 sm:p-4"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-body font-medium break-all">{d.hostUnicode}</span>
        <StateBadge d={d} />
        {d.primary && <Badge tone="strong">Primary</Badge>}
        {d.disabled && <Badge tone="danger">Stopped</Badge>}
      </div>
      {d.hostUnicode !== d.host && (
        <span className="font-mono text-mono text-muted-foreground">{d.host}</span>
      )}
      <p className="text-caption text-muted-foreground">{nextStep(d, expires)}</p>
      <p className="font-mono text-mono text-subtle-foreground">
        Last check: {formatAgo(d.lastCheckedAt, now)}
        {d.lastError && d.state !== "active" ? ` · ${ERROR_TEXT[d.lastError]}` : ""}
      </p>
      {d.state === "active" && !d.disabled && (
        <a
          className="font-mono text-mono break-all underline underline-offset-2"
          href={`https://${d.host}/`}
          target="_blank"
          rel="noreferrer"
        >
          https://{d.host}/
        </a>
      )}
      {(needsTxt || needsCname) && (
        <div className="flex flex-col gap-3" role="group" aria-label={`DNS records for ${d.host}`}>
          {needsTxt && (
            <RecordCard
              kind="TXT"
              purpose="Proves the domain is yours"
              name={d.verification.txtName}
              value={d.verification.txtValue}
            />
          )}
          {needsCname &&
            (d.verification.cnameTarget ? (
              <RecordCard
                kind="CNAME"
                purpose="Sends visitors to the page"
                name={d.host}
                value={d.verification.cnameTarget}
              />
            ) : (
              <p className="text-caption text-muted-foreground">
                The CNAME target is not set up on this installation yet. Ask your administrator.
              </p>
            ))}
        </div>
      )}
      <div aria-live="polite" className="text-caption">
        {notice}
      </div>
      {canManage && (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            size="small"
            busy={verify.isPending}
            disabled={d.disabled}
            onClick={() => verify.mutate()}
            aria-label={`Check ${d.host} now`}
          >
            Check now
          </Button>
          {isServing(d) && !d.primary && (
            <Button
              variant="secondary"
              size="small"
              onClick={() => setMaking(true)}
              aria-label={`Make ${d.host} primary`}
            >
              Make primary
            </Button>
          )}
          <Button
            variant="ghost-destructive"
            size="small"
            onClick={() => setRemoving(true)}
            aria-label={`Remove ${d.host}`}
          >
            Remove
          </Button>
        </div>
      )}
      {making && (
        <ConfirmDialog
          title="Make this the primary domain?"
          confirmLabel="Make primary"
          confirmVariant="primary"
          busy={primary.isPending}
          onConfirm={() => primary.mutate()}
          onCancel={() => setMaking(false)}
        >
          <p>
            Canonical links, the feed and the badge of this page will use {d.hostUnicode}. The other
            domains keep serving the page.
          </p>
        </ConfirmDialog>
      )}
      {removing && (
        <ConfirmDialog
          title={`Remove ${d.hostUnicode}?`}
          confirmLabel="Remove domain"
          busy={remove.isPending}
          onConfirm={() => remove.mutate()}
          onCancel={() => setRemoving(false)}
        >
          <p>
            {isServing(d)
              ? "The page stops being served on this domain at once, and visitors get “not found”."
              : "The domain stops being checked and no longer counts against your plan."}{" "}
            {d.primary && others > 0 && "Another domain of this page becomes the primary one. "}
            The DNS records stay at your DNS provider until you delete them there.
          </p>
        </ConfirmDialog>
      )}
    </li>
  );
}

function RecordCard({
  kind,
  purpose,
  name,
  value,
}: {
  kind: "TXT" | "CNAME";
  purpose: string;
  name: string;
  value: string;
}) {
  return (
    <div className="flex flex-col gap-3 border border-border-faint bg-muted p-3">
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="eyebrow">{kind} record</span>
        <span className="text-caption text-muted-foreground">{purpose}</span>
      </div>
      <CopyField
        label={`${kind} name`}
        value={name}
        once={false}
        copyAriaLabel={`Copy ${kind} name`}
      />
      <CopyField
        label={`${kind} value`}
        value={value}
        once={false}
        copyAriaLabel={`Copy ${kind} value`}
      />
    </div>
  );
}
