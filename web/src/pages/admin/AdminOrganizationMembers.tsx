import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useState, type SubmitEvent } from "react";
import { errorMessage } from "../../api/client";
import {
  listAdminAllUsers,
  queryKeys,
  resetAdminUserPassword,
  setAdminMember,
  type AdminOrganization,
  type AdminUserSummary,
  type MembershipRole,
} from "../../api/endpoints";
import PeopleTable from "../../components/PeopleTable";
import TemporaryPasswordPanel from "../../components/TemporaryPasswordPanel";
import { useToast } from "../../components/Toasts";
import { Alert, Button, ConfirmDialog, Field, Input, Select } from "../../components/ui";
import { memberLabel } from "../../lib/members";
import { adminPeople, useRefreshPeople, type Person } from "../../lib/people";
import { NO_ROLE, PROJECTS_ONLY_LABEL, ROLE_LABELS, ROLES, roleLabel } from "../../lib/roles";

/** How many matches the picker shows; the search narrows the rest. */
const MATCHES_SHOWN = 8;

/** Members of an organization as the platform admin manages them. */
export default function AdminOrganizationMembers({
  organization,
}: {
  organization: AdminOrganization;
}) {
  const toast = useToast();
  const deleted = organization.deletedAt !== null;
  const api = adminPeople(organization);
  const refresh = useRefreshPeople(api);
  const members = useQuery({ queryKey: api.keys.members, queryFn: api.listMembers });
  const [resetting, setResetting] = useState<Person | null>(null);
  const [search, setSearch] = useState("");
  const [term, setTerm] = useState("");
  const [picked, setPicked] = useState<AdminUserSummary | null>(null);
  const [role, setRole] = useState<MembershipRole | typeof NO_ROLE>("member");
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

  const add = useMutation({
    mutationFn: (user: AdminUserSummary) =>
      setAdminMember(organization.id, user.id, role === NO_ROLE ? null : role),
    onSuccess: (added) => {
      setPicked(null);
      setSearch("");
      setTerm("");
      setAddError(undefined);
      void refresh();
      toast(`${added.login} is ${roleLabel(added.role).toLowerCase()} now`);
    },
    onError: (err) => setAddError(errorMessage(err)),
  });

  const reset = useMutation({
    mutationFn: (member: Person) => resetAdminUserPassword(member.userId),
    gcTime: 0,
    onSuccess: (res, member) => {
      setResetting(null);
      setTemporary({ login: memberLabel(member), password: res.temporaryPassword });
      reset.reset();
    },
    onError: (err) => {
      setResetting(null);
      toast(errorMessage(err), "error");
    },
  });

  function onAdd(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!picked) return;
    setAddError(undefined);
    add.mutate(picked);
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
      <PeopleTable
        api={api}
        organizationName={organization.name}
        userLink={(m) => `/admin/users/${m.userId}`}
        actions={(m, describedBy) => (
          <Button
            variant="secondary"
            size="small"
            disabled={m.isSystemAdmin}
            aria-label={`Reset password of ${memberLabel(m)}`}
            // The note under the role says why.
            aria-describedby={describedBy}
            onClick={() => setResetting(m)}
          >
            Reset password
          </Button>
        )}
      />

      {!deleted && (
        <form onSubmit={onAdd} className="flex max-w-[640px] flex-col gap-3 pt-2">
          <h3 className="text-[15px] font-semibold">Add an existing user</h3>
          {addError && <Alert>{addError}</Alert>}
          {picked ? (
            <p className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-[13px] break-words">{picked.login}</span>
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
                  className="w-[250px] max-w-full"
                  value={role}
                  onChange={(e) => setRole(e.target.value as MembershipRole | typeof NO_ROLE)}
                >
                  <option value={NO_ROLE}>{PROJECTS_ONLY_LABEL}</option>
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

      {resetting && (
        <ConfirmDialog
          title={`Reset the password of ${memberLabel(resetting)}?`}
          confirmLabel="Reset password"
          confirmVariant="primary"
          busy={reset.isPending}
          onCancel={() => setResetting(null)}
          onConfirm={() => reset.mutate(resetting)}
        >
          <p>
            The current password stops working and all of the user&apos;s sessions end. You get a
            temporary password once, to hand over yourself; the user must choose a new password at
            the next sign-in. Verify who asked for it first.
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
  const shown = users.filter((u) => !u.isSystemAdmin && !exclude.has(u.id));
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
