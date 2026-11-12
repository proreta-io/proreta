import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { errorMessage } from "../../api/client";
import {
  listAdminAllUsers,
  listAdminMembers,
  queryKeys,
  removeAdminMember,
  resetAdminUserPassword,
  setAdminMember,
  type AdminMember,
  type AdminOrganization,
  type AdminUserSummary,
  type MembershipRole,
} from "../../api/endpoints";
import TemporaryPasswordPanel from "../../components/TemporaryPasswordPanel";
import { useToast } from "../../components/Toasts";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyNote,
  ErrorState,
  Field,
  Input,
  Loading,
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
} from "../../components/ui";
import { formatDate } from "../../lib/format";
import { ROLE_LABELS } from "../../lib/roles";

const ROLES: MembershipRole[] = ["owner", "maintainer", "member", "viewer"];

type Pending =
  | { kind: "role"; member: AdminMember; role: MembershipRole }
  | { kind: "remove"; member: AdminMember }
  | { kind: "reset"; member: AdminMember };

/** How many matches the picker shows; the search narrows the rest. */
const MATCHES_SHOWN = 8;

/** Members of an organization as the platform admin manages them. */
export default function AdminOrganizationMembers({
  organization,
}: {
  organization: AdminOrganization;
}) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const deleted = organization.deletedAt !== null;
  const members = useQuery({
    queryKey: queryKeys.adminMembers(organization.id),
    queryFn: () => listAdminMembers(organization.id),
  });
  const [pending, setPending] = useState<Pending | null>(null);
  const [search, setSearch] = useState("");
  const [term, setTerm] = useState("");
  const [picked, setPicked] = useState<AdminUserSummary | null>(null);
  const [role, setRole] = useState<MembershipRole>("member");
  const [addError, setAddError] = useState<string | undefined>();
  // The temporary password lives only here: never in storage, gone when hidden or the page is
  // left. The mutation keeps no copy: gcTime 0 and reset() right after copying it.
  const [temporary, setTemporary] = useState<{ login: string; password: string } | null>(null);

  useEffect(() => {
    const t = setTimeout(() => setTerm(search.trim()), 250);
    return () => clearTimeout(t);
  }, [search]);
  const matches = useQuery({
    queryKey: queryKeys.adminAllUsers(term, "", 0),
    queryFn: () => listAdminAllUsers(term, "", 0),
    enabled: term.length >= 2 && picked === null,
  });

  function refresh() {
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminMembers(organization.id) });
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminUsers(organization.id) });
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminOrganization(organization.id) });
  }

  const change = useMutation({
    mutationFn: ({ member, role }: { member: AdminMember; role: MembershipRole }) =>
      setAdminMember(organization.id, member.userId, role),
    onSuccess: (updated) => {
      setPending(null);
      refresh();
      toast(`${updated.login} is now ${ROLE_LABELS[updated.role].toLowerCase()}`);
    },
    onError: (err) => {
      setPending(null);
      toast(errorMessage(err), "error");
    },
  });

  const remove = useMutation({
    mutationFn: (member: AdminMember) => removeAdminMember(organization.id, member.userId),
    onSuccess: (_, member) => {
      setPending(null);
      refresh();
      toast(`${member.login} was removed`);
    },
    onError: (err) => {
      setPending(null);
      toast(errorMessage(err), "error");
    },
  });

  const add = useMutation({
    mutationFn: (user: AdminUserSummary) => setAdminMember(organization.id, user.id, role),
    onSuccess: (added) => {
      setPicked(null);
      setSearch("");
      setTerm("");
      setAddError(undefined);
      refresh();
      toast(`${added.login} is ${ROLE_LABELS[added.role].toLowerCase()} now`);
    },
    onError: (err) => setAddError(errorMessage(err)),
  });

  const reset = useMutation({
    mutationFn: (member: AdminMember) => resetAdminUserPassword(member.userId),
    gcTime: 0,
    onSuccess: (res, member) => {
      setPending(null);
      setTemporary({ login: member.login, password: res.temporaryPassword });
      reset.reset();
    },
    onError: (err) => {
      setPending(null);
      toast(errorMessage(err), "error");
    },
  });

  function onAdd(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!picked) return;
    setAddError(undefined);
    add.mutate(picked);
  }

  function onRoleChange(member: AdminMember, next: MembershipRole) {
    // Owner changes are confirmed; the others apply at once.
    if (next === "owner" || member.role === "owner")
      setPending({ kind: "role", member, role: next });
    else change.mutate({ member, role: next });
  }

  return (
    <section aria-labelledby="members-heading" className="flex flex-col gap-3">
      <h2 id="members-heading" className="text-lead font-semibold">
        Members
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
      {members.isPending && (
        <Loading label="Loading members…">
          <Skeleton className="w-1/2" />
        </Loading>
      )}
      {members.isError && (
        <ErrorState
          variant="section"
          title="Cannot load the members"
          onRetry={() => void members.refetch()}
        />
      )}
      {members.data && members.data.items.length === 0 && (
        <EmptyNote>This organization has no members.</EmptyNote>
      )}
      {members.data && members.data.items.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>Login</Th>
              <Th>Role</Th>
              <Th>Status</Th>
              <Th numeric>Joined</Th>
              <Th numeric>
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {members.data.items.map((m) => (
              <Tr key={m.userId}>
                <Td>
                  <span className="flex flex-wrap items-center gap-2">
                    <Link
                      to={`/admin/users/${m.userId}`}
                      className="font-mono text-[13px] break-all hover:underline"
                    >
                      {m.login}
                    </Link>
                    {m.isPlatformAdmin && <Badge tone="inverse">System admin</Badge>}
                  </span>
                </Td>
                <Td>
                  {deleted || m.isPlatformAdmin ? (
                    ROLE_LABELS[m.role]
                  ) : (
                    <Select
                      aria-label={`Role of ${m.login}`}
                      className="w-[130px]"
                      value={m.role}
                      disabled={change.isPending}
                      onChange={(e) => onRoleChange(m, e.target.value as MembershipRole)}
                    >
                      {ROLES.map((r) => (
                        <option key={r} value={r}>
                          {ROLE_LABELS[r]}
                        </option>
                      ))}
                    </Select>
                  )}
                </Td>
                <Td className={m.status === "disabled" ? "text-subtle-foreground" : undefined}>
                  {m.status}
                </Td>
                <Td numeric>{formatDate(m.joinedAt)}</Td>
                <Td numeric>
                  <span className="inline-flex flex-wrap items-center justify-end gap-2">
                    <Button
                      variant="secondary"
                      size="small"
                      disabled={m.isPlatformAdmin}
                      aria-label={`Reset password of ${m.login}`}
                      aria-describedby={
                        m.isPlatformAdmin ? `system-admin-note-${m.userId}` : undefined
                      }
                      onClick={() => setPending({ kind: "reset", member: m })}
                    >
                      Reset password
                    </Button>
                    {!deleted && !m.isPlatformAdmin && (
                      <Button
                        variant="ghost-destructive"
                        size="small"
                        aria-label={`Remove ${m.login}`}
                        onClick={() => setPending({ kind: "remove", member: m })}
                      >
                        Remove
                      </Button>
                    )}
                  </span>
                  {m.isPlatformAdmin && (
                    <span
                      id={`system-admin-note-${m.userId}`}
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

      {!deleted && (
        <form onSubmit={onAdd} className="flex max-w-[640px] flex-col gap-3 pt-2">
          <h3 className="text-[15px] font-semibold">Add an existing user</h3>
          {addError && <Alert>{addError}</Alert>}
          {picked ? (
            <p className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-[13px] break-all">{picked.login}</span>
              <Button
                type="button"
                variant="ghost"
                size="small"
                onClick={() => {
                  setPicked(null);
                  setAddError(undefined);
                }}
              >
                Change
              </Button>
            </p>
          ) : (
            <>
              <Field label="Find a user" hint="Type at least two letters of the email or the name.">
                {(c) => (
                  <Input
                    {...c}
                    type="search"
                    autoComplete="off"
                    maxLength={254}
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                  />
                )}
              </Field>
              <UserMatches
                term={term}
                loading={matches.isFetching}
                error={matches.isError}
                users={matches.data?.items}
                exclude={new Set(members.data?.items.map((m) => m.userId))}
                onPick={(u) => {
                  setPicked(u);
                  setAddError(undefined);
                }}
              />
            </>
          )}
          <div className="flex flex-wrap items-end gap-2">
            <Field label="Role">
              {(c) => (
                <Select
                  {...c}
                  className="w-[130px]"
                  value={role}
                  onChange={(e) => setRole(e.target.value as MembershipRole)}
                >
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {ROLE_LABELS[r]}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Button
              type="submit"
              variant="secondary"
              busy={add.isPending}
              disabled={add.isPending || !picked}
            >
              Add member
            </Button>
          </div>
          <p className="text-caption text-muted-foreground">
            The plan&apos;s member limit applies: raise the plan first if it is full. A new role
            applies on the member&apos;s next request.
          </p>
        </form>
      )}

      {pending?.kind === "role" && (
        <ConfirmDialog
          title={`Make ${pending.member.login} ${ROLE_LABELS[pending.role].toLowerCase()}?`}
          confirmLabel="Change role"
          confirmVariant="primary"
          busy={change.isPending}
          onCancel={() => setPending(null)}
          onConfirm={() => change.mutate({ member: pending.member, role: pending.role })}
        >
          <p>
            {pending.role === "owner"
              ? "An owner can do everything in this organization, including deleting it and managing other owners."
              : "They lose the owner rights in this organization on their next request."}{" "}
            The change is recorded in the organization&apos;s audit log.
          </p>
        </ConfirmDialog>
      )}
      {pending?.kind === "reset" && (
        <ConfirmDialog
          title={`Reset the password of ${pending.member.login}?`}
          confirmLabel="Reset password"
          confirmVariant="primary"
          busy={reset.isPending}
          onCancel={() => setPending(null)}
          onConfirm={() => reset.mutate(pending.member)}
        >
          <p>
            The current password stops working and all of the user&apos;s sessions end. You get a
            temporary password once, to hand over yourself; the user must choose a new password at
            the next sign-in. Verify who asked for it first.
          </p>
        </ConfirmDialog>
      )}
      {pending?.kind === "remove" && (
        <ConfirmDialog
          title={`Remove ${pending.member.login}?`}
          confirmLabel="Remove member"
          busy={remove.isPending}
          onCancel={() => setPending(null)}
          onConfirm={() => remove.mutate(pending.member)}
        >
          <p>
            They lose access to {organization.name} at once and are signed out of it. Their account
            and other organizations stay. The last owner cannot be removed.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function UserMatches({
  term,
  loading,
  error,
  users,
  exclude,
  onPick,
}: {
  term: string;
  loading: boolean;
  error: boolean;
  users: AdminUserSummary[] | undefined;
  exclude: Set<string>;
  onPick: (user: AdminUserSummary) => void;
}) {
  if (term.length < 2) return null;
  if (error) return <p className="text-caption text-muted-foreground">Cannot search users.</p>;
  if (!users)
    return loading ? <p className="text-caption text-muted-foreground">Searching…</p> : null;
  const shown = users.filter((u) => !u.isPlatformAdmin && !exclude.has(u.id));
  if (shown.length === 0)
    return (
      <p className="text-caption text-muted-foreground">
        No account matches “{term}”, or it is a member already. Invite the person below if they have
        no account yet.
      </p>
    );
  return (
    <ul
      aria-label="Matching users"
      className="flex flex-col divide-y divide-border border border-border"
    >
      {shown.slice(0, MATCHES_SHOWN).map((u) => (
        <li key={u.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
          <span className="flex min-w-0 flex-col">
            <span className="font-mono text-[13px] break-all">{u.login}</span>
            <span className="text-caption text-muted-foreground">
              {[u.displayName, u.status === "disabled" ? "disabled" : ""]
                .filter(Boolean)
                .join(" · ")}
            </span>
          </span>
          <Button
            type="button"
            variant="secondary"
            size="small"
            aria-label={`Select ${u.login}`}
            onClick={() => onPick(u)}
          >
            Select
          </Button>
        </li>
      ))}
    </ul>
  );
}
