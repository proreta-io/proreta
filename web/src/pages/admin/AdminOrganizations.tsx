import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { errorMessage, errorMeta } from "../../api/client";
import { ADMIN_PAGE_SIZE, listAdminOrganizations, queryKeys } from "../../api/endpoints";
import {
  Button,
  Checkbox,
  ErrorState,
  Input,
  Pagination,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
  Loading,
  EmptyNote,
} from "../../components/ui";
import { formatDate } from "../../lib/format";
import { adminTitle } from "./AdminOverview";
import { OrganizationStatusBadge, organizationTone } from "./OrganizationStatusBadge";

/** The list of organizations with search and pagination; the state lives in the URL. */
export default function AdminOrganizations() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const query = params.get("query") ?? "";
  const offset = Math.max(0, Number.parseInt(params.get("offset") ?? "0", 10) || 0);
  const includeDeleted = params.get("deleted") === "1";
  const [draft, setDraft] = useState(query);

  const organizations = useQuery({
    queryKey: queryKeys.adminOrganizations(query, offset, includeDeleted),
    queryFn: () => listAdminOrganizations(query, offset, includeDeleted),
    placeholderData: keepPreviousData,
  });

  function update(next: { query?: string; offset?: number; includeDeleted?: boolean }) {
    const p = new URLSearchParams();
    const q = next.query ?? query;
    const o = next.offset ?? offset;
    const d = next.includeDeleted ?? includeDeleted;
    if (q) p.set("query", q);
    if (o > 0) p.set("offset", String(o));
    if (d) p.set("deleted", "1");
    setParams(p);
  }

  function onSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    update({ query: draft.trim(), offset: 0 });
  }

  const data = organizations.data;
  const from = data && data.items.length > 0 ? offset + 1 : 0;
  const to = data ? offset + data.items.length : 0;

  return (
    <section className="flex flex-col">
      <h1 className={adminTitle}>Organizations</h1>
      <form
        role="search"
        onSubmit={onSearch}
        className="flex flex-wrap items-center gap-x-5 gap-y-3 pb-5"
      >
        <div className="flex min-w-0 flex-1 gap-2 sm:max-w-[480px]">
          <Input
            type="search"
            name="query"
            aria-label="Search organizations"
            placeholder="Name, slug, organization id or a member's email"
            maxLength={200}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <Button type="submit" variant="secondary">
            Search
          </Button>
        </div>
        <Checkbox
          label="Show deleted organizations"
          checked={includeDeleted}
          onChange={(e) => update({ includeDeleted: e.target.checked, offset: 0 })}
        />
      </form>

      {organizations.isPending && (
        <Loading label="Loading organizations…" className="border-t border-border pt-3">
          {["60%", "45%", "70%"].map((w) => (
            <span key={w} className="block" style={{ width: w }}>
              <Skeleton />
            </span>
          ))}
        </Loading>
      )}
      {organizations.isError && !data && (
        <ErrorState
          title="Cannot load organizations"
          detail={errorMessage(organizations.error)}
          meta={errorMeta(organizations.error)}
          onRetry={() => void organizations.refetch()}
        />
      )}
      {data && data.items.length === 0 && (
        <EmptyNote>{query ? `No organizations match “${query}”.` : "No organizations."}</EmptyNote>
      )}
      {data && data.items.length > 0 && (
        <>
          <Table>
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Plan</Th>
                <Th>Status</Th>
                <Th numeric>Users</Th>
                <Th numeric>Monitors</Th>
                <Th numeric>Watchdogs</Th>
                <Th numeric>Created</Th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((org) => (
                <Tr
                  key={org.id}
                  tone={organizationTone(org)}
                  interactive
                  onClick={() => navigate(`/admin/organizations/${org.id}`)}
                >
                  <Td>
                    <Link
                      to={`/admin/organizations/${org.id}`}
                      className="font-medium hover:underline"
                      onClick={(e) => e.stopPropagation()}
                    >
                      {org.name}
                    </Link>
                    <span className="block font-mono text-mono text-subtle-foreground">
                      {org.slug}
                    </span>
                  </Td>
                  <Td>{org.planName}</Td>
                  <Td>
                    <OrganizationStatusBadge organization={org} />
                  </Td>
                  <Td numeric>{org.users}</Td>
                  <Td numeric>{org.monitors}</Td>
                  <Td numeric>{org.heartbeats}</Td>
                  <Td numeric>{formatDate(org.createdAt)}</Td>
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
    </section>
  );
}
