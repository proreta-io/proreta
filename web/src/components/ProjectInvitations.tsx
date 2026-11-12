import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, errorMessage } from "../api/client";
import {
  createInvitation,
  listInvitations,
  queryKeys,
  revokeInvitation,
  type Invitation,
  type InvitationRole,
  type Project,
} from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import { absoluteLink } from "../lib/inviteLink";
import { useEmailEnabled } from "../lib/oauth";
import { assignableRoles, ROLE_DESCRIPTIONS, ROLE_LABELS } from "../lib/roles";
import { formatDate, formatDateTime } from "../lib/format";
import { useToast } from "./Toasts";
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
} from "./ui";

const STATUS_TONES: Record<Invitation["status"], BadgeTone> = {
  open: "neutral",
  accepted: "success",
  revoked: "deleted",
  expired: "deleted",
};

interface CreatedLink {
  url: string;
  role: InvitationRole;
  emailedTo?: string;
}

/**
 * Invitations into one project, for the people who manage it. A link gives the invited person the
 * chosen role in this project only, never an organization role. The link is kept in component
 * state and not in the query cache.
 */
export default function ProjectInvitations({ project }: { project: Project }) {
  const auth = useAuth();
  const queryClient = useQueryClient();
  const toast = useToast();
  const key = queryKeys.projectInvitations(project.id);
  const invitations = useQuery({ queryKey: key, queryFn: () => listInvitations(project.id) });
  const roles = assignableRoles(project.role);
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

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: key });
    void queryClient.invalidateQueries({ queryKey: queryKeys.invitations });
    void queryClient.invalidateQueries({ queryKey: queryKeys.usage });
  };
  const create = useMutation({
    mutationFn: () =>
      createInvitation(
        role,
        label.trim(),
        emailEnabled && sendByEmail ? email.trim() : "",
        project.id,
      ),
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
    mutationFn: (inv: Invitation) => revokeInvitation(inv.id),
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
    <section
      aria-labelledby="project-invitations-heading"
      className="flex flex-col gap-4 border-t border-border pt-6"
    >
      <div className="flex flex-col gap-1.5">
        <h2 id="project-invitations-heading" className="text-lead font-semibold">
          Invitations to {project.name}
        </h2>
        <p className="text-[13px] text-muted-foreground">
          A link joins one person with the role you choose in this project only. It works once and
          for 7 days.
        </p>
      </div>
      {created ? (
        <div
          role="group"
          aria-label="Invitation link"
          className="flex flex-col gap-4 border border-border p-5"
        >
          {created.emailedTo && (
            <Alert tone="neutral">
              An email with the link is on its way to <strong>{created.emailedTo}</strong>. You can
              also copy it here.
            </Alert>
          )}
          <CopyField label="Invitation link" value={created.url} copyLabel="Copy link" />
          <p className="text-muted-foreground">
            Anyone with this link can join {project.name} as{" "}
            {ROLE_LABELS[created.role].toLowerCase()} until it is used, revoked or 7 days pass. Send
            it over a channel you trust. Proreta keeps only a hash of it.
          </p>
          <div>
            <Button variant="secondary" onClick={() => setCreated(null)}>
              I sent it, hide the link
            </Button>
          </div>
        </div>
      ) : (
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
      )}
      {invitations.isPending ? (
        <Loading label="Loading invitations…">
          <Skeleton className="w-1/2" />
        </Loading>
      ) : invitations.isError ? (
        <ErrorState
          variant="section"
          title="Cannot load the invitations"
          onRetry={() => void invitations.refetch()}
        />
      ) : invitations.data.items.length === 0 ? (
        <EmptyNote>No invitations to this project yet.</EmptyNote>
      ) : (
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
                  {inv.status === "open" && (
                    <Button
                      variant="ghost-destructive"
                      size="small"
                      aria-label={`Revoke the invitation ${inv.label || "without a note"}`}
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
          title={`Invite someone to ${project.name}`}
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
            <Field label="Role in this project" hint={ROLE_DESCRIPTIONS[role]}>
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
    </section>
  );
}
