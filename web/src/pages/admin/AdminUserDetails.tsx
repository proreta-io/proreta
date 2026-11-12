import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  disableAdminUser,
  enableAdminUser,
  getAdminUser,
  queryKeys,
  revokeAdminUserSessions,
  setAdminPlatformAdmin,
  type AdminUserDetail,
  type AdminUserMembership,
} from "../../api/endpoints";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/Toasts";
import {
  Badge,
  Button,
  buttonClass,
  ConfirmDialog,
  EmptyNote,
  ErrorState,
  Loading,
  Metric,
  MetricStrip,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
} from "../../components/ui";
import { formatDate, formatDateTime } from "../../lib/format";
import { UserStatusBadge } from "./UserStatusBadge";

const back = (
  <Link
    to="/admin/users"
    className="self-start py-2.5 text-[13px] text-muted-foreground hover:text-foreground sm:py-0"
  >
    ← Users
  </Link>
);

const organizationStatusText = (s: AdminUserMembership["organizationStatus"]) =>
  s === "active" ? "Active" : s === "suspended" ? "Suspended" : "Deleted";

export default function AdminUserDetails() {
  const { id = "" } = useParams();
  return <UserDetails key={id} id={id} />;
}

function UserDetails({ id }: { id: string }) {
  const detail = useQuery({ queryKey: queryKeys.adminUser(id), queryFn: () => getAdminUser(id) });

  if (detail.isPending) {
    return (
      <Loading label="Loading user…" className="pt-9">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="w-80 max-w-full" />
      </Loading>
    );
  }
  if (detail.isError) {
    if (detail.error instanceof ApiError && detail.error.status === 404) {
      return (
        <section className="flex flex-col items-start gap-4 py-10">
          <h1 className="text-headline font-semibold tracking-[-0.02em] sm:text-display">
            User not found
          </h1>
          <Link to="/admin/users" className={buttonClass("secondary")}>
            Back to users
          </Link>
        </section>
      );
    }
    return (
      <div className="pt-9">
        <ErrorState
          title="Cannot load the user"
          detail={errorMessage(detail.error)}
          meta={errorMeta(detail.error)}
          onRetry={() => void detail.refetch()}
        />
      </div>
    );
  }
  return <UserInfo detail={detail.data} />;
}

function UserInfo({ detail }: { detail: AdminUserDetail }) {
  const { user, memberships, openSessions } = detail;
  const queryClient = useQueryClient();
  const toast = useToast();
  const me = useAuth().session?.user.id;
  const [confirm, setConfirm] = useState<
    "disable" | "enable" | "revoke" | "grant-admin" | "revoke-admin" | null
  >(null);
  const disabled = user.status === "disabled";
  const revokeBlocked = user.isSystemAdmin
    ? "The system admin is configured by the deployment and cannot be revoked here."
    : user.id === me
      ? "You cannot revoke your own access. Ask another platform admin."
      : null;

  function refresh(updated?: AdminUserDetail) {
    if (updated) queryClient.setQueryData(queryKeys.adminUser(user.id), updated);
    else void queryClient.invalidateQueries({ queryKey: queryKeys.adminUser(user.id) });
    void queryClient.invalidateQueries({ queryKey: ["admin", "all-users"] });
  }

  const setStatus = useMutation({
    mutationFn: (next: "disable" | "enable") =>
      next === "disable" ? disableAdminUser(user.id) : enableAdminUser(user.id),
    onSuccess: (updated, next) => {
      setConfirm(null);
      refresh(updated);
      toast(next === "disable" ? "User disabled" : "User enabled");
    },
    onError: (err) => {
      setConfirm(null);
      toast(errorMessage(err), "error");
    },
  });

  const setPlatformAdmin = useMutation({
    mutationFn: (enabled: boolean) => setAdminPlatformAdmin(user.id, enabled),
    onSuccess: (updated, enabled) => {
      setConfirm(null);
      refresh(updated);
      toast(enabled ? "Platform admin granted" : "Platform admin revoked");
    },
    onError: (err) => {
      setConfirm(null);
      toast(errorMessage(err), "error");
    },
  });

  const revoke = useMutation({
    mutationFn: () => revokeAdminUserSessions(user.id),
    onSuccess: (res) => {
      setConfirm(null);
      refresh();
      toast(
        res.sessionsRevoked === 0
          ? "The user had no open sessions"
          : `Signed out: ${res.sessionsRevoked} ${res.sessionsRevoked === 1 ? "session" : "sessions"} ended`,
      );
    },
    onError: (err) => {
      setConfirm(null);
      toast(errorMessage(err), "error");
    },
  });

  return (
    <section className="flex flex-col gap-8 pt-4">
      <div className="flex flex-col gap-3">
        {back}
        <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-4">
          <div className="flex min-w-0 flex-col gap-2">
            <h1 className="font-mono text-headline leading-[1.1] font-semibold tracking-[-0.02em] break-all sm:text-display">
              {user.login}
            </h1>
            <div className="flex flex-wrap items-center gap-3">
              <UserStatusBadge status={user.status} />
              {user.isSystemAdmin && <Badge tone="inverse">System admin</Badge>}
              {user.isPlatformAdmin && !user.isSystemAdmin && (
                <Badge tone="inverse">Platform admin</Badge>
              )}
              {user.displayName && (
                <span className="text-[13px] text-muted-foreground">{user.displayName}</span>
              )}
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" onClick={() => setConfirm("revoke")}>
              Sign out everywhere
            </Button>
            {disabled ? (
              <Button variant="secondary" onClick={() => setConfirm("enable")}>
                Enable
              </Button>
            ) : (
              !user.isSystemAdmin && (
                <Button variant="ghost-destructive" onClick={() => setConfirm("disable")}>
                  Disable
                </Button>
              )
            )}
          </div>
        </div>
        {user.isSystemAdmin && (
          <p className="max-w-[420px] text-caption text-muted-foreground">
            The system admin cannot be disabled here.
          </p>
        )}
      </div>

      <MetricStrip>
        <Metric label="Organizations" value={user.organizationCount} />
        <Metric label="Open sessions" value={openSessions} />
        <Metric label="Created" value={formatDate(user.createdAt)} />
        <Metric label="Email" value={user.emailVerified ? "Verified" : "Not verified"} />
      </MetricStrip>

      <dl className="grid gap-x-6 gap-y-1 sm:grid-cols-[140px_minmax(0,1fr)]">
        <dt className="text-caption text-muted-foreground">User id</dt>
        <dd className="font-mono text-[13px] break-all">{user.id}</dd>
        {user.mustChangePassword && (
          <>
            <dt className="text-caption text-muted-foreground">Password</dt>
            <dd className="text-[13px]">A temporary password: the user must replace it.</dd>
          </>
        )}
      </dl>

      <section aria-labelledby="platform-admin-heading" className="flex flex-col gap-3">
        <h2 id="platform-admin-heading" className="text-lead font-semibold">
          Platform admin
        </h2>
        <p className="max-w-[560px] text-[13px] text-muted-foreground">
          {user.isPlatformAdmin
            ? "This user is a platform admin: full control of the whole instance, every organization, user and setting."
            : "This user is not a platform admin."}
        </p>
        {user.isPlatformAdmin ? (
          <div className="flex flex-col items-start gap-2">
            <Button
              variant="secondary"
              disabled={revokeBlocked !== null}
              aria-describedby={revokeBlocked ? "platform-admin-note" : undefined}
              onClick={() => setConfirm("revoke-admin")}
            >
              Revoke platform admin
            </Button>
            {revokeBlocked && (
              <p
                id="platform-admin-note"
                className="max-w-[420px] text-caption text-muted-foreground"
              >
                {revokeBlocked}
              </p>
            )}
          </div>
        ) : (
          <div className="flex flex-col items-start gap-2">
            <Button
              variant="secondary"
              disabled={disabled}
              aria-describedby={disabled ? "platform-admin-note" : undefined}
              onClick={() => setConfirm("grant-admin")}
            >
              Grant platform admin
            </Button>
            {disabled && (
              <p
                id="platform-admin-note"
                className="max-w-[420px] text-caption text-muted-foreground"
              >
                Enable the user first: only an active user can be a platform admin.
              </p>
            )}
          </div>
        )}
      </section>

      <section aria-labelledby="memberships-heading" className="flex flex-col gap-3">
        <h2 id="memberships-heading" className="text-lead font-semibold">
          Organizations
        </h2>
        {memberships.length === 0 && <EmptyNote>This user belongs to no organization.</EmptyNote>}
        {memberships.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>Organization</Th>
                <Th>Role</Th>
                <Th>Status</Th>
                <Th numeric>Joined</Th>
              </tr>
            </thead>
            <tbody>
              {memberships.map((m) => (
                <Tr
                  key={m.organizationId}
                  tone={
                    m.organizationStatus === "deleted"
                      ? "deleted"
                      : m.organizationStatus === "suspended"
                        ? "warning"
                        : "default"
                  }
                >
                  <Td>
                    <Link
                      to={`/admin/organizations/${m.organizationId}`}
                      className="font-medium hover:underline"
                    >
                      {m.organizationName}
                    </Link>
                    <span className="block font-mono text-mono text-subtle-foreground">
                      {m.organizationSlug}
                    </span>
                  </Td>
                  <Td>{m.role}</Td>
                  <Td>{organizationStatusText(m.organizationStatus)}</Td>
                  <Td numeric>
                    <time dateTime={m.joinedAt} title={formatDateTime(m.joinedAt)}>
                      {formatDate(m.joinedAt)}
                    </time>
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </section>

      {confirm === "disable" && (
        <ConfirmDialog
          title={`Disable ${user.login}?`}
          confirmLabel="Disable user"
          busy={setStatus.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => setStatus.mutate("disable")}
        >
          <p>
            The user cannot sign in in any organization, with a password, Google or GitHub, and all
            of the user&apos;s {openSessions} open sessions end now. Memberships and data are kept;
            you can enable the user again later.
          </p>
        </ConfirmDialog>
      )}
      {confirm === "enable" && (
        <ConfirmDialog
          title={`Enable ${user.login}?`}
          confirmLabel="Enable user"
          confirmVariant="primary"
          busy={setStatus.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => setStatus.mutate("enable")}
        >
          <p>The user can sign in again with the same password or provider.</p>
        </ConfirmDialog>
      )}
      {confirm === "grant-admin" && (
        <ConfirmDialog
          title={`Make ${user.login} a platform admin?`}
          confirmLabel="Grant platform admin"
          confirmVariant="primary"
          busy={setPlatformAdmin.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => setPlatformAdmin.mutate(true)}
        >
          <p>
            A platform admin has full control of the whole instance: every organization, user, plan
            and setting, and can grant or revoke the same access for others. The user keeps their
            own organizations. The change is recorded in the audit log.
          </p>
        </ConfirmDialog>
      )}
      {confirm === "revoke-admin" && (
        <ConfirmDialog
          title={`Revoke platform admin from ${user.login}?`}
          confirmLabel="Revoke platform admin"
          busy={setPlatformAdmin.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => setPlatformAdmin.mutate(false)}
        >
          <p>
            The user loses access to the Admin Area at once. Sessions, organizations and data are
            kept. The change is recorded in the audit log.
          </p>
        </ConfirmDialog>
      )}
      {confirm === "revoke" && (
        <ConfirmDialog
          title={`Sign ${user.login} out everywhere?`}
          confirmLabel="Sign out everywhere"
          confirmVariant="primary"
          busy={revoke.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => revoke.mutate()}
        >
          <p>
            All of the user&apos;s sessions in every organization end now. The user can sign in
            again.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}
