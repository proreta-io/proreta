import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, errorMessage } from "../../api/client";
import {
  createAdminInvitation,
  listAdminInvitations,
  queryKeys,
  revokeAdminInvitation,
  type AdminOrganization,
  type Invitation,
  type InvitationRole,
} from "../../api/endpoints";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/Toasts";
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
  type BadgeTone,
} from "../../components/ui";
import { formatDate, formatDateTime } from "../../lib/format";
import { useEmailEnabled } from "../../lib/oauth";
import { ROLE_DESCRIPTIONS, ROLE_LABELS } from "../../lib/roles";

const INVITATION_ROLES: InvitationRole[] = ["maintainer", "member", "viewer"];

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
  emailedTo?: string;
}

function absoluteLink(url: string) {
  return url.startsWith("/") ? `${globalThis.location.origin}${url}` : url;
}

/** Invitations of an organization as the platform admin manages them. */
export default function AdminOrganizationInvitations({
  organization,
}: {
  organization: AdminOrganization;
}) {
  const auth = useAuth();
  const queryClient = useQueryClient();
  const toast = useToast();
  const deleted = organization.deletedAt !== null;
  const invitations = useQuery({
    queryKey: queryKeys.adminInvitations(organization.id),
    queryFn: () => listAdminInvitations(organization.id),
  });
  const emailEnabled = useEmailEnabled();
  const [dialog, setDialog] = useState(false);
  const [role, setRole] = useState<InvitationRole>("member");
  const [label, setLabel] = useState("");
  const [email, setEmail] = useState("");
  const [sendChoice, setSendChoice] = useState<boolean | undefined>();
  const sendByEmail = sendChoice ?? auth.session?.emailInvitations === true;
  const [emailError, setEmailError] = useState<string | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [created, setCreated] = useState<CreatedLink | null>(null);
  const [revoking, setRevoking] = useState<Invitation | null>(null);

  function refresh() {
    void queryClient.invalidateQueries({ queryKey: queryKeys.adminInvitations(organization.id) });
  }

  const create = useMutation({
    mutationFn: () =>
      createAdminInvitation(
        organization.id,
        role,
        label.trim(),
        emailEnabled && sendByEmail ? email.trim() : "",
      ),
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
      refresh();
    },
    onError: (err) => {
      const fieldMessage = err instanceof ApiError ? err.fieldErrorMap().email : undefined;
      if (fieldMessage) setEmailError(fieldMessage);
      else setError(errorMessage(err));
    },
  });

  const revoke = useMutation({
    mutationFn: (inv: Invitation) => revokeAdminInvitation(organization.id, inv.id),
    onSuccess: () => {
      setRevoking(null);
      refresh();
      toast("Invitation revoked");
    },
    onError: (err) => {
      setRevoking(null);
      toast(errorMessage(err), "error");
    },
  });

  return (
    <section aria-labelledby="invitations-heading" className="flex flex-col gap-3">
      <h2 id="invitations-heading" className="text-lead font-semibold">
        Invitations
      </h2>
      <p className="max-w-[640px] text-[13px] text-muted-foreground">
        For a person without an account. A link joins one person with the role you choose, works
        once and for 7 days. An owner is made by adding the account or promoting after they join.
      </p>

      {created ? (
        <section
          aria-labelledby="admin-invite-link-heading"
          className="flex flex-col gap-4 border border-border p-5"
        >
          <h3 id="admin-invite-link-heading" className="text-lead font-semibold">
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
            used, revoked or 7 days pass. It is shown only now; Proreta keeps only a hash of it.
          </p>
          <div>
            <Button variant="secondary" onClick={() => setCreated(null)}>
              I sent it, hide the link
            </Button>
          </div>
        </section>
      ) : (
        !deleted && (
          <div>
            <Button
              variant="secondary"
              onClick={() => {
                setError(undefined);
                setEmailError(undefined);
                setDialog(true);
              }}
            >
              Invite someone
            </Button>
          </div>
        )
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
                <Td>{ROLE_LABELS[inv.role]}</Td>
                <Td>
                  <Badge tone={STATUS_TONES[inv.status]}>{inv.status}</Badge>
                </Td>
                <Td numeric>
                  {inv.status === "open"
                    ? formatDateTime(inv.expiresAt)
                    : formatDate(inv.expiresAt)}
                </Td>
                <Td numeric>
                  {inv.status === "open" && !deleted && (
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
                  {INVITATION_ROLES.map((r) => (
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
          <p>The link stops working at once and its place in the plan is freed.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
