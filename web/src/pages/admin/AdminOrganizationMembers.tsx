import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { ApiError, errorMessage } from "../../api/client";
import {
  addAdminMemberByEmail,
  listAdminMembers,
  queryKeys,
  removeAdminMember,
  setAdminMember,
  type AdminMember,
  type AdminOrganization,
  type MembershipRole,
} from "../../api/endpoints";
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

const ROLES: MembershipRole[] = ["owner", "admin", "member", "viewer"];

type Pending =
  | { kind: "role"; member: AdminMember; role: MembershipRole }
  | { kind: "remove"; member: AdminMember };

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
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<MembershipRole>("member");
  const [addError, setAddError] = useState<string | undefined>();
  const [noAccount, setNoAccount] = useState(false);

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
    mutationFn: () => addAdminMemberByEmail(organization.id, email.trim(), role),
    onSuccess: (added) => {
      setEmail("");
      setAddError(undefined);
      setNoAccount(false);
      refresh();
      toast(`${added.login} is ${ROLE_LABELS[added.role].toLowerCase()} now`);
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 404) {
        setNoAccount(true);
        setAddError(undefined);
      } else {
        setNoAccount(false);
        setAddError(errorMessage(err));
      }
    },
  });

  function onAdd(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setAddError(undefined);
    setNoAccount(false);
    add.mutate();
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
                    <span className="font-mono text-[13px] break-all">{m.login}</span>
                    {m.isPlatformAdmin && <Badge tone="inverse">System admin</Badge>}
                    {m.status === "disabled" && <Badge tone="deleted">Disabled</Badge>}
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
                <Td numeric>{formatDate(m.joinedAt)}</Td>
                <Td numeric>
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
          {noAccount && (
            <Alert tone="neutral" title="No account with this email.">
              Invite the person below instead; they create the account when they use the link.
            </Alert>
          )}
          <div className="flex flex-wrap items-end gap-2">
            <Field label="Email of the account" className="min-w-[220px] flex-1">
              {(c) => (
                <Input
                  {...c}
                  type="email"
                  autoComplete="off"
                  maxLength={254}
                  required
                  value={email}
                  onChange={(e) => {
                    setEmail(e.target.value);
                    setNoAccount(false);
                  }}
                />
              )}
            </Field>
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
            <Button type="submit" variant="secondary" busy={add.isPending} disabled={add.isPending}>
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
