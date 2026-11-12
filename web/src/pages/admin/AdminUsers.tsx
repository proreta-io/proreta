import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { errorMessage, errorMeta } from "../../api/client";
import {
  ADMIN_PAGE_SIZE,
  listAdminAllUsers,
  queryKeys,
  type AdminUserStatusFilter,
} from "../../api/endpoints";
import {
  Badge,
  Button,
  EmptyNote,
  ErrorState,
  Input,
  Loading,
  Pagination,
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
} from "../../components/ui";
import { formatDate } from "../../lib/format";
import { adminTitle } from "./AdminOverview";
import { UserStatusBadge } from "./UserStatusBadge";

const parseStatus = (value: string | null): AdminUserStatusFilter | "" =>
  value === "active" || value === "disabled" ? value : "";

/** Users of all organizations with search, a status filter and pagination; the state lives in the URL. */
export default function AdminUsers() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const query = params.get("query") ?? "";
  const status = parseStatus(params.get("status"));
  const offset = Math.max(0, Number.parseInt(params.get("offset") ?? "0", 10) || 0);
  const [draft, setDraft] = useState(query);

  const users = useQuery({
    queryKey: queryKeys.adminAllUsers(query, status, offset),
    queryFn: () => listAdminAllUsers(query, status, offset),
    placeholderData: keepPreviousData,
  });

  function update(next: { query?: string; status?: AdminUserStatusFilter | ""; offset?: number }) {
    const p = new URLSearchParams();
    const q = next.query ?? query;
    const s = next.status ?? status;
    const o = next.offset ?? offset;
    if (q) p.set("query", q);
    if (s) p.set("status", s);
    if (o > 0) p.set("offset", String(o));
    setParams(p);
  }

  function onSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    update({ query: draft.trim(), offset: 0 });
  }

  const data = users.data;
  const from = data && data.items.length > 0 ? offset + 1 : 0;
  const to = data ? offset + data.items.length : 0;

  return (
    <section className="flex flex-col">
      <h1 className={adminTitle}>Users</h1>
      <form
        role="search"
        onSubmit={onSearch}
        className="flex flex-wrap items-center gap-x-5 gap-y-3 pb-5"
      >
        <div className="flex min-w-0 flex-1 gap-2 sm:max-w-[480px]">
          <Input
            type="search"
            name="query"
            aria-label="Search users"
            placeholder="Email or name"
            maxLength={200}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <Button type="submit" variant="secondary">
            Search
          </Button>
        </div>
        <Select
          aria-label="Status"
          className="w-auto"
          value={status}
          onChange={(e) => update({ status: parseStatus(e.target.value), offset: 0 })}
        >
          <option value="">All statuses</option>
          <option value="active">Active</option>
          <option value="disabled">Disabled</option>
        </Select>
      </form>

      {users.isPending && (
        <Loading label="Loading users…" className="border-t border-border pt-3">
          {["60%", "45%", "70%"].map((w) => (
            <span key={w} className="block" style={{ width: w }}>
              <Skeleton />
            </span>
          ))}
        </Loading>
      )}
      {users.isError && !data && (
        <ErrorState
          title="Cannot load users"
          detail={errorMessage(users.error)}
          meta={errorMeta(users.error)}
          onRetry={() => void users.refetch()}
        />
      )}
      {data && data.items.length === 0 && (
        <EmptyNote>{query ? `No users match “${query}”.` : "No users."}</EmptyNote>
      )}
      {data && data.items.length > 0 && (
        <>
          <Table>
            <thead>
              <tr>
                <Th>User</Th>
                <Th>Status</Th>
                <Th numeric>Organizations</Th>
                <Th numeric>Created</Th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((u) => (
                <Tr
                  key={u.id}
                  tone={u.status === "disabled" ? "warning" : "default"}
                  interactive
                  onClick={() => navigate(`/admin/users/${u.id}`)}
                >
                  <Td>
                    <span className="flex flex-wrap items-center gap-2">
                      <Link
                        to={`/admin/users/${u.id}`}
                        className="font-mono text-[13px] font-medium break-all hover:underline"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {u.login}
                      </Link>
                      {u.isSystemAdmin && <Badge tone="inverse">System admin</Badge>}
                      {u.isPlatformAdmin && !u.isSystemAdmin && <Badge tone="inverse">Admin</Badge>}
                    </span>
                    {u.displayName && (
                      <span className="block text-caption text-muted-foreground">
                        {u.displayName}
                      </span>
                    )}
                  </Td>
                  <Td>
                    <UserStatusBadge status={u.status} />
                  </Td>
                  <Td numeric>{u.organizationCount}</Td>
                  <Td numeric>{formatDate(u.createdAt)}</Td>
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
