import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ApiError, errorMessage } from "../api/client";
import {
  createInvitation,
  deleteOrganization,
  leaveOrganization,
  listInvitations,
  queryKeys,
  revokeInvitation,
  type Invitation,
  type InvitationRole,
  type MembershipRole,
} from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import { useEmailEnabled } from "../lib/oauth";
import PeopleTable from "../components/PeopleTable";
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
  Select,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
  cx,
  type BadgeTone,
} from "../components/ui";
import { formatDate, formatDateTime } from "../lib/format";
import { assignableRoles, ROLE_DESCRIPTIONS, ROLE_HINTS, ROLE_LABELS } from "../lib/roles";
import { absoluteLink } from "../lib/inviteLink";
import { useOrganizationPeople } from "../lib/people";
import { usePermissions } from "../lib/permissions";

/** Members, roles and invitations of the organization. */
export default function Members() {
  const auth = useAuth();
  const can = usePermissions();
  const role = can.organizationRole;
  const org = auth.session?.organization.name ?? "this organization";
  const people = useOrganizationPeople();
  if (!can.ready) return null;
  if (!can.org("organization.members.read")) {
    return (
      <section className="flex max-w-[960px] flex-col">
        <h1 className="pt-6 pb-2 text-headline leading-[1.1] font-semibold tracking-[-0.02em] sm:pt-10 sm:text-display">
          Members
        </h1>
        <p className="pb-6 text-muted-foreground sm:pb-9">{ROLE_HINTS.organizationLevel}</p>
        <Section id="leave" title="Leave">
          <LeaveOrDelete />
        </Section>
      </section>
    );
  }
  return (
    <section className="flex flex-col">
      <h1 className="pt-6 pb-2 text-headline leading-[1.1] font-semibold tracking-[-0.02em] sm:pt-10 sm:text-display">
        Members
      </h1>
      <p className="max-w-[960px] pb-6 text-muted-foreground sm:pb-9">
        People in <strong className="font-medium text-foreground">{org}</strong>. You are{" "}
        {role ? `${article(role)} ${ROLE_LABELS[role].toLowerCase()}` : "a member"}:{" "}
        {role
          ? ROLE_DESCRIPTIONS[role].charAt(0).toLowerCase() + ROLE_DESCRIPTIONS[role].slice(1)
          : ""}{" "}
        A role here applies to every project; a role in a project can only raise it there.{" "}
        <Link to="/help/roles" className="underline underline-offset-3">
          What each role may do
        </Link>
      </p>
      <Section id="members" title="People" wide>
        <PeopleTable api={people} organizationName={org} />
      </Section>
      <Section
        wide
        id="invitations"
        title="Invitations"
        description="A link joins one person with the role you choose. It works once and for 7 days. No email is sent: hand it over yourself."
      >
        {can.org("organization.members.manage") ? (
          <Invitations />
        ) : (
          <p className="text-muted-foreground">{ROLE_HINTS.manageMembers}</p>
        )}
      </Section>
      <Section id="leave" title="Leave or delete">
        <LeaveOrDelete />
      </Section>
    </section>
  );
}

function article(role: MembershipRole) {
  return role === "owner" ? "an" : "a";
}

function Section({
  id,
  title,
  description,
  wide = false,
  children,
}: {
  id: string;
  title: string;
  description?: string;
  /** The heading above the content, for a table that needs the full width. */
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <section
      aria-labelledby={`${id}-heading`}
      className={cx(
        "grid gap-4 border-t border-border pt-6 pb-7 sm:pt-8 sm:pb-10",
        !wide && "sm:grid-cols-[220px_minmax(0,720px)] sm:gap-12",
      )}
    >
      <div className="flex flex-col gap-1.5">
        <h2 id={`${id}-heading`} className="text-lead font-semibold">
          {title}
        </h2>
        {description && <p className="text-[13px] text-muted-foreground">{description}</p>}
      </div>
      <div className="flex min-w-0 flex-col gap-4">{children}</div>
    </section>
  );
}

const STATUS_TONES: Record<Invitation["status"], BadgeTone> = {
  open: "neutral",
  accepted: "success",
  revoked: "deleted",
  expired: "deleted",
};

/** The link of a new invitation; kept in component state only, never stored. */
interface CreatedLink {
  url: string;
  role: InvitationRole;
  /** The address the link was queued to, when the admin gave one. */
  emailedTo?: string;
}

function Invitations() {
  const auth = useAuth();
  const queryClient = useQueryClient();
  const toast = useToast();
  const invitations = useQuery({
    queryKey: queryKeys.invitations,
    queryFn: () => listInvitations(),
  });
  const [dialog, setDialog] = useState(false);
  const [role, setRole] = useState<InvitationRole>("member");
  const [label, setLabel] = useState("");
  const emailEnabled = useEmailEnabled();
  const [email, setEmail] = useState("");
  // Until the person touches the box it follows the platform admin's default.
  const [sendChoice, setSendChoice] = useState<boolean | undefined>();
  const sendByEmail = sendChoice ?? auth.session?.emailInvitations === true;
  const [emailError, setEmailError] = useState<string | undefined>();
  const [created, setCreated] = useState<CreatedLink | null>(null);
  const [error, setError] = useState<string | undefined>();
  const [revoking, setRevoking] = useState<Invitation | null>(null);
  const roles = assignableRoles(auth.session?.role).filter(
    (r): r is InvitationRole => r !== "owner",
  );

  const create = useMutation({
    mutationFn: () =>
      createInvitation(role, label.trim(), emailEnabled && sendByEmail ? email.trim() : ""),
    // The answer holds the secret link: it must not stay in the query cache.
    gcTime: 0,
    onSuccess: (res) => {
      setDialog(false);
      setCreated({
        url: absoluteLink(res.inviteUrl),
        role: res.invitation.role,
        emailedTo: res.emailQueued ? email.trim().toLowerCase() : undefined,
      });
      setLabel("");
      setEmail("");
      setSendChoice(undefined);
      create.reset();
      void queryClient.invalidateQueries({ queryKey: queryKeys.invitations });
      void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
    },
    onError: (err) => {
      const fieldMessage = err instanceof ApiError ? err.fieldErrorMap().email : undefined;
      if (fieldMessage) setEmailError(fieldMessage);
      else setError(errorMessage(err));
    },
  });

  const revoke = useMutation({
    mutationFn: (inv: Invitation) => revokeInvitation(inv.id),
    onSuccess: () => {
      setRevoking(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.invitations });
      void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
      toast("Invitation revoked");
    },
    onError: (err) => {
      setRevoking(null);
      toast(errorMessage(err), "error");
    },
  });

  return (
    <>
      {created ? (
        <section
          aria-labelledby="invite-link-heading"
          className="flex flex-col gap-4 border border-border p-5"
        >
          <h3 id="invite-link-heading" className="text-lead font-semibold">
            Invitation link ({ROLE_LABELS[created.role].toLowerCase()})
          </h3>
          {created.emailedTo && (
            <Alert tone="neutral">
              An email with the link is on its way to <strong>{created.emailedTo}</strong>. You can
              also copy it here.
            </Alert>
          )}
          <CopyField label="Invitation link" value={created.url} copyLabel="Copy link" />
          <p className="text-muted-foreground">
            Anyone with this link can join as {ROLE_LABELS[created.role].toLowerCase()} until it is
            used, revoked or 7 days pass, whatever email address they use.{" "}
            {created.emailedTo
              ? "Do not share it further."
              : "Send it over a channel you trust, not a public chat."}{" "}
            Proreta keeps only a hash of it.
          </p>
          <div>
            <Button variant="secondary" onClick={() => setCreated(null)}>
              I sent it, hide the link
            </Button>
          </div>
        </section>
      ) : (
        <div>
          <Button
            onClick={() => {
              setError(undefined);
              setEmailError(undefined);
              setDialog(true);
            }}
          >
            Invite someone
          </Button>
        </div>
      )}

      {invitations.isPending && (
        <Loading label="Loading invitations…">
          <Skeleton className="w-full" />
        </Loading>
      )}
      {invitations.isError && (
        <ErrorState
          variant="section"
          title="Cannot load the invitations"
          onRetry={() => void invitations.refetch()}
        />
      )}
      {invitations.data && invitations.data.items.length === 0 && (
        <EmptyNote>No invitations yet.</EmptyNote>
      )}
      {invitations.data && invitations.data.items.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>Invitation</Th>
              <Th>Role</Th>
              <Th>Status</Th>
              <Th numeric>Expires</Th>
              <Th numeric>
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {invitations.data.items.map((inv) => (
              <Tr key={inv.id}>
                <Td>
                  <span className="block">{inv.label || "No note"}</span>
                  <span className="block text-caption text-muted-foreground">
                    by {inv.createdBy}
                    {inv.acceptedBy ? ` · joined: ${inv.acceptedBy}` : ""}
                  </span>
                </Td>
                <Td>
                  {ROLE_LABELS[inv.role]}
                  {inv.projectName && (
                    <span className="block text-caption text-muted-foreground">
                      in {inv.projectName}
                    </span>
                  )}
                </Td>
                <Td>
                  <Badge tone={STATUS_TONES[inv.status]}>{inv.status}</Badge>
                </Td>
                <Td numeric>
                  {inv.status === "open"
                    ? formatDateTime(inv.expiresAt)
                    : formatDate(inv.expiresAt)}
                </Td>
                <Td numeric>
                  {inv.status === "open" && (
                    <Button
                      variant="ghost-destructive"
                      size="small"
                      onClick={() => setRevoking(inv)}
                    >
                      Revoke
                    </Button>
                  )}
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}

      {dialog && (
        <ConfirmDialog
          title="Invite someone"
          confirmLabel={
            emailEnabled && sendByEmail && email.trim()
              ? "Create link and send email"
              : "Create link"
          }
          confirmVariant="primary"
          busy={create.isPending}
          onCancel={() => setDialog(false)}
          onConfirm={() => {
            setError(undefined);
            create.mutate();
          }}
        >
          <div className="flex flex-col gap-4">
            {error && <Alert>{error}</Alert>}
            <Field label="Role" hint={ROLE_DESCRIPTIONS[role]}>
              {(c) => (
                <Select
                  {...c}
                  value={role}
                  onChange={(e) => setRole(e.target.value as InvitationRole)}
                >
                  {roles.map((r) => (
                    <option key={r} value={r}>
                      {ROLE_LABELS[r]}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            {emailEnabled && (
              <>
                <Field
                  label="Email the link to (optional)"
                  hint="You still get the link here, and anyone who has it can join."
                  error={emailError}
                >
                  {(c) => (
                    <Input
                      {...c}
                      type="email"
                      autoComplete="off"
                      maxLength={254}
                      value={email}
                      onChange={(e) => {
                        setEmail(e.target.value);
                        setEmailError(undefined);
                      }}
                    />
                  )}
                </Field>
                <Checkbox
                  label="Send by email"
                  description="Proreta emails the link to this address when you create it."
                  checked={sendByEmail}
                  onChange={(e) => setSendChoice(e.target.checked)}
                />
              </>
            )}
            <Field label="Note (optional)" hint="For you only, for example the person's name.">
              {(c) => (
                <Input
                  {...c}
                  maxLength={80}
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                />
              )}
            </Field>
          </div>
        </ConfirmDialog>
      )}
      {revoking && (
        <ConfirmDialog
          title="Revoke this invitation?"
          confirmLabel="Revoke invitation"
          busy={revoke.isPending}
          onCancel={() => setRevoking(null)}
          onConfirm={() => revoke.mutate(revoking)}
        >
          <p>The link stops working at once and its place in your plan is freed.</p>
        </ConfirmDialog>
      )}
    </>
  );
}

function LeaveOrDelete() {
  const auth = useAuth();
  const navigate = useNavigate();
  const toast = useToast();
  const can = usePermissions();
  const org = auth.session?.organization;
  const [confirm, setConfirm] = useState<"leave" | "delete" | null>(null);

  const leave = useMutation({
    mutationFn: leaveOrganization,
    onSuccess: () => {
      auth.endSession();
      void navigate("/login", { replace: true });
    },
    onError: (err) => {
      setConfirm(null);
      toast(errorMessage(err), "error");
    },
  });
  const remove = useMutation({
    mutationFn: deleteOrganization,
    onSuccess: () => {
      auth.endSession();
      void navigate("/login", { replace: true });
    },
    onError: (err) => {
      setConfirm(null);
      toast(errorMessage(err), "error");
    },
  });
  if (!org) return null;
  const isDefault = org.slug === "default";

  return (
    <>
      <div className="flex flex-col gap-2">
        <p className="text-muted-foreground">
          Leaving ends your access to {org.name}; your account and other organizations stay. An
          organization always keeps an owner: make someone else an owner before you leave as the
          last one.
        </p>
        <div>
          <Button variant="secondary" onClick={() => setConfirm("leave")}>
            Leave organization
          </Button>
        </div>
      </div>
      {can.org("organization.delete") && !isDefault && (
        <div className="flex flex-col gap-2 border-t border-border-faint pt-4">
          <p className="text-muted-foreground">
            Deleting stops every monitor and alert of {org.name} and signs every member out of it.
            The data is kept but nobody can use it any more.
          </p>
          <div>
            <Button variant="ghost-destructive" onClick={() => setConfirm("delete")}>
              Delete organization
            </Button>
          </div>
        </div>
      )}
      {can.org("organization.delete") && isDefault && (
        <p className="text-caption text-muted-foreground">
          This is the platform&apos;s own organization: it cannot be deleted.
        </p>
      )}
      {confirm === "leave" && (
        <ConfirmDialog
          title={`Leave ${org.name}?`}
          confirmLabel="Leave"
          busy={leave.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => leave.mutate()}
        >
          <p>You are signed out. To come back, someone has to invite you again.</p>
        </ConfirmDialog>
      )}
      {confirm === "delete" && (
        <ConfirmDialog
          title={`Delete ${org.name}?`}
          confirmLabel="Delete organization"
          busy={remove.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => remove.mutate()}
        >
          <p>
            Monitors stop, alerts are no longer sent and every member loses access. The app cannot
            undo this; the platform administrator can restore it.
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}
