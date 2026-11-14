import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type SubmitEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  ADMIN_PAGE_SIZE,
  listAdminStatusPageDomains,
  setAdminStatusPageDomainDisabled,
  type AdminStatusPageDomain,
  type StatusPageDomainState,
} from "../../api/endpoints";
import {
  Badge,
  Button,
  ConfirmDialog,
  DiamondShape,
  DotShape,
  EmptyNote,
  ErrorState,
  Field,
  HollowDotShape,
  Input,
  Loading,
  Pagination,
  Select,
  Skeleton,
  Table,
  Td,
  Textarea,
  Th,
  Tr,
  TriangleShape,
} from "../../components/ui";
import { queryKeys } from "../../api/endpoints";
import { formatDateTime } from "../../lib/format";
import { adminTitle } from "./AdminOverview";

const FEATURE_OFF = "urn:proreta:problem:custom-domains-disabled";
const ORG_DELETED = "urn:proreta:problem:organization-deleted";
const MAX_REASON = 500;

const STATES: StatusPageDomainState[] = ["pending", "verified", "active", "failed"];
const parseState = (value: string | null): StatusPageDomainState | "" =>
  STATES.find((s) => s === value) ?? "";

const stateText: Record<StatusPageDomainState, string> = {
  pending: "Pending",
  verified: "Verified",
  active: "Active",
  failed: "Failed",
};

/** A state is a word and a shape; amber is for the failure, never red (red means down). */
function StateBadge({ state }: { state: StatusPageDomainState }) {
  const text = stateText[state];
  if (state === "failed")
    return (
      <Badge tone="warning" icon={<DiamondShape />}>
        {text}
      </Badge>
    );
  if (state === "pending") return <Badge icon={<HollowDotShape />}>{text}</Badge>;
  return <Badge icon={<DotShape />}>{text}</Badge>;
}

const isProblem = (err: unknown, type: string) => err instanceof ApiError && err.type === type;

function SwitchDialog({ domain, onClose }: { domain: AdminStatusPageDomain; onClose: () => void }) {
  const queryClient = useQueryClient();
  const disabling = !domain.disabled;
  const [reason, setReason] = useState("");
  const [touched, setTouched] = useState(false);
  const field = useRef<HTMLTextAreaElement>(null);
  const trimmed = reason.trim();
  const invalid = trimmed.length === 0 || reason.length > MAX_REASON;

  const mutation = useMutation({
    mutationFn: () => setAdminStatusPageDomainDisabled(domain.id, disabling, trimmed),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["admin", "domains"] });
      onClose();
    },
  });

  const deleted = isProblem(mutation.error, ORG_DELETED);
  const error = touched && trimmed.length === 0 ? "Give a reason." : undefined;

  return (
    <ConfirmDialog
      title={`${disabling ? "Disable" : "Enable"} ${domain.hostUnicode}?`}
      confirmLabel={disabling ? "Disable domain" : "Enable domain"}
      confirmVariant={disabling ? "destructive" : "primary"}
      busy={mutation.isPending}
      initialFocus={field}
      onCancel={onClose}
      onConfirm={() => {
        setTouched(true);
        if (!invalid) mutation.mutate();
      }}
    >
      <p>
        {disabling
          ? "The host stops being served and stops getting a certificate at once. A certificate already issued stays at the proxy, but the page answers 404. The organization sees the domain as disabled."
          : "The host is served again and can get a certificate again, if it is still verified. The organization no longer sees the domain as disabled."}
      </p>
      <Field
        label="Reason"
        error={error}
        hint={`${reason.length} / ${MAX_REASON}. Kept in the organization's audit log.`}
      >
        {(control) => (
          <Textarea
            {...control}
            ref={field}
            rows={3}
            maxLength={MAX_REASON}
            required
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        )}
      </Field>
      {deleted ? (
        <p role="alert" className="text-caption">
          The organization is deleted, so its domains cannot be changed any more.
        </p>
      ) : (
        mutation.isError && (
          <p role="alert" className="flex items-start gap-1.5 text-caption text-down">
            <TriangleShape size="small" className="mt-[5px]" />
            <span>{errorMessage(mutation.error)}</span>
          </p>
        )
      )}
    </ConfirmDialog>
  );
}

/** Custom status page domains of all organizations, with the abuse switch; the state lives in the URL. */
export default function AdminDomains() {
  const [params, setParams] = useSearchParams();
  const query = params.get("query") ?? "";
  const state = parseState(params.get("state"));
  const offset = Math.max(0, Number.parseInt(params.get("offset") ?? "0", 10) || 0);
  const [draft, setDraft] = useState(query);
  const [target, setTarget] = useState<AdminStatusPageDomain | null>(null);

  const domains = useQuery({
    queryKey: queryKeys.adminDomains(query, state, offset),
    queryFn: () => listAdminStatusPageDomains(query, state, offset),
    placeholderData: keepPreviousData,
    // The feature being off is an answer, not a failure worth retrying.
    retry: (count, err) => !isProblem(err, FEATURE_OFF) && count < 1,
  });

  function update(next: { query?: string; state?: StatusPageDomainState | ""; offset?: number }) {
    const p = new URLSearchParams();
    const q = next.query ?? query;
    const s = next.state ?? state;
    const o = next.offset ?? offset;
    if (q) p.set("query", q);
    if (s) p.set("state", s);
    if (o > 0) p.set("offset", String(o));
    setParams(p);
  }

  function onSearch(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault();
    update({ query: draft.trim(), offset: 0 });
  }

  const data = domains.data;
  const off = isProblem(domains.error, FEATURE_OFF);
  const from = data && data.items.length > 0 ? offset + 1 : 0;
  const to = data ? offset + data.items.length : 0;

  return (
    <section className="flex flex-col">
      <h1 className={adminTitle}>Domains</h1>
      {off ? (
        <EmptyNote>Custom status page domains are turned off on this installation.</EmptyNote>
      ) : (
        <>
          <form
            role="search"
            onSubmit={onSearch}
            className="flex flex-wrap items-center gap-x-5 gap-y-3 pb-5"
          >
            <div className="flex min-w-0 flex-1 gap-2 sm:max-w-[480px]">
              <Input
                type="search"
                name="query"
                aria-label="Search domains"
                placeholder="Host, organization slug or name"
                maxLength={200}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
              />
              <Button type="submit" variant="secondary">
                Search
              </Button>
            </div>
            <Select
              aria-label="State"
              className="w-auto"
              value={state}
              onChange={(e) => update({ state: parseState(e.target.value), offset: 0 })}
            >
              <option value="">All states</option>
              {STATES.map((s) => (
                <option key={s} value={s}>
                  {stateText[s]}
                </option>
              ))}
            </Select>
          </form>

          {domains.isPending && (
            <Loading label="Loading domains…" className="border-t border-border pt-3">
              {["60%", "45%", "70%"].map((w) => (
                <span key={w} className="block" style={{ width: w }}>
                  <Skeleton />
                </span>
              ))}
            </Loading>
          )}
          {domains.isError && !data && (
            <ErrorState
              title="Cannot load domains"
              detail={errorMessage(domains.error)}
              meta={errorMeta(domains.error)}
              onRetry={() => void domains.refetch()}
            />
          )}
          {data && data.items.length === 0 && (
            <EmptyNote>
              {query || state ? "No domains match the filter." : "No custom domains yet."}
            </EmptyNote>
          )}
          {data && data.items.length > 0 && (
            <>
              <Table>
                <thead>
                  <tr>
                    <Th>Host</Th>
                    <Th>Organization</Th>
                    <Th>Status page</Th>
                    <Th>State</Th>
                    <Th numeric>Verified</Th>
                    <Th>
                      <span className="sr-only">Actions</span>
                    </Th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((d) => (
                    <Tr key={d.id} tone={d.disabled ? "warning" : "default"}>
                      <Td>
                        <span className="font-mono text-[13px] font-medium whitespace-nowrap">
                          {d.hostUnicode}
                        </span>
                        {d.hostUnicode !== d.host && (
                          <span className="block font-mono text-caption whitespace-nowrap text-muted-foreground">
                            {d.host}
                          </span>
                        )}
                      </Td>
                      <Td>
                        <Link
                          to={`/admin/organizations/${d.organizationId}`}
                          className="font-mono text-[13px] whitespace-nowrap hover:underline"
                        >
                          {d.organizationSlug}
                        </Link>
                      </Td>
                      <Td className="font-mono text-[13px] whitespace-nowrap">
                        {d.statusPageSlug}
                      </Td>
                      <Td>
                        <span className="flex flex-wrap items-center gap-2">
                          <StateBadge state={d.state} />
                          {d.primary && <Badge tone="plan">Primary</Badge>}
                          {d.disabled && (
                            <Badge tone="warning" icon={<DiamondShape />}>
                              Disabled
                            </Badge>
                          )}
                        </span>
                        {d.disabled && d.disabledAt && (
                          <span className="block text-caption text-muted-foreground">
                            Disabled {formatDateTime(d.disabledAt)}
                          </span>
                        )}
                      </Td>
                      <Td numeric>{d.verifiedAt ? formatDateTime(d.verifiedAt) : "Not yet"}</Td>
                      <Td>
                        <Button
                          variant="secondary"
                          size="small"
                          aria-label={`${d.disabled ? "Enable" : "Disable"} ${d.hostUnicode}`}
                          onClick={() => setTarget(d)}
                        >
                          {d.disabled ? "Enable" : "Disable"}
                        </Button>
                      </Td>
                    </Tr>
                  ))}
                </tbody>
              </Table>
              <Pagination
                page={Math.floor(offset / ADMIN_PAGE_SIZE) + 1}
                pageCount={Math.max(1, Math.ceil(data.total / ADMIN_PAGE_SIZE))}
                label={`${from}–${to} of ${data.total}`}
                onPage={(page) => update({ offset: (page - 1) * ADMIN_PAGE_SIZE })}
              />
            </>
          )}
        </>
      )}
      {target && <SwitchDialog key={target.id} domain={target} onClose={() => setTarget(null)} />}
    </section>
  );
}
