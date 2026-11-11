import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { ApiError, errorMessage, errorMeta } from "../../api/client";
import {
  deleteAdminEmailSettings,
  getAdminEmailSettings,
  queryKeys,
  sendAdminTestEmail,
  updateAdminEmailFeatures,
  updateAdminEmailSettings,
  type AdminEmailConnectionUpdate,
  type AdminEmailSettings,
  type AdminEmailTestResult,
} from "../../api/endpoints";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/Toasts";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  ErrorState,
  Field,
  Input,
  Loading,
  Select,
  Skeleton,
  WriteOnlyValue,
} from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { adminTitle } from "./AdminOverview";

type Tls = AdminEmailConnectionUpdate["tls"];

const DEFAULT_PORT: Record<Tls, number> = { starttls: 587, tls: 465, none: 25 };

/** What each failure category means, in the admin's words. The server's own text is never shown. */
const FAILURES: Record<string, string> = {
  connect: "The connection broke or the server did not speak SMTP. Check the host and port.",
  refused: "Nothing accepted the connection. Check the host and port, and any firewall.",
  dns: "The host name does not resolve from the jobs process.",
  blocked_address:
    "The host resolves to an address that is never used for mail (link-local, cloud metadata or unspecified).",
  tls: "The secure connection failed: the server does not offer it on this port, or its certificate does not match the host name.",
  auth: "The server refused the username or password.",
  timeout: "The server did not answer in time.",
  recipient_rejected: "The server refused the recipient of the test message.",
  rejected:
    "The server refused the sender or the message. Check the sender address and its domain.",
  bad_message: "The test message could not be built.",
  not_delivered:
    "The test message was not picked up within three minutes. Is the jobs process running?",
  other: "The test failed for another reason.",
};

/** Settings of the outgoing email, edited by the platform admin; they take effect without a redeploy. */
export default function AdminEmail() {
  const settings = useQuery({
    queryKey: queryKeys.adminEmail,
    queryFn: getAdminEmailSettings,
    // While a test message is on its way the result is polled.
    refetchInterval: (query) => (query.state.data?.lastTest?.state === "pending" ? 2000 : false),
  });
  return (
    <section className="flex flex-col gap-8">
      <h1 className={adminTitle}>Email</h1>
      {settings.isPending && (
        <Loading label="Loading email settings…">
          <Skeleton className="h-24 w-full" />
        </Loading>
      )}
      {settings.isError && (
        <ErrorState
          title="Cannot load the email settings"
          detail={errorMessage(settings.error)}
          meta={errorMeta(settings.error)}
          onRetry={() => void settings.refetch()}
        />
      )}
      {settings.data && <EmailSettings key={settings.data.updatedAt} settings={settings.data} />}
    </section>
  );
}

function EmailSettings({ settings }: { settings: AdminEmailSettings }) {
  return (
    <div className="flex max-w-[640px] flex-col gap-10">
      <p className="text-muted-foreground">
        Everything about outgoing email is set here, not in the installation&apos;s configuration,
        so it changes without a redeploy. Email is off until you save a server, a test message
        succeeds and you turn a switch on. The password is stored encrypted and is never shown
        again.
      </p>
      <ConnectionForm settings={settings} />
      <TestSection settings={settings} />
      <Switches settings={settings} />
    </div>
  );
}

function ConnectionForm({ settings }: { settings: AdminEmailSettings }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const [host, setHost] = useState(settings.host);
  const [tls, setTls] = useState<Tls>(settings.tls);
  const [port, setPort] = useState(
    String(settings.configured ? settings.port : DEFAULT_PORT.starttls),
  );
  const [from, setFrom] = useState(settings.from);
  const [username, setUsername] = useState(settings.username);
  const [password, setPassword] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [removing, setRemoving] = useState(false);

  const portNumber = Number(port);
  const identityChanged =
    settings.configured &&
    (host.trim() !== settings.host ||
      portNumber !== settings.port ||
      username.trim() !== settings.username);
  // The stored password is only kept while the place it goes to is unchanged.
  const askPassword =
    username.trim() !== "" && (!settings.passwordSet || replacing || identityChanged);
  const reenter = settings.passwordSet && identityChanged && username.trim() !== "";

  const save = useMutation({
    mutationFn: () =>
      updateAdminEmailSettings({
        host: host.trim(),
        port: portNumber,
        tls,
        from: from.trim(),
        username: username.trim(),
        ...(askPassword && password ? { password } : {}),
      }),
    onSuccess: (next) => {
      setPassword("");
      setReplacing(false);
      queryClient.setQueryData(queryKeys.adminEmail, next);
      toast("Email settings saved.");
    },
  });
  const remove = useMutation({
    mutationFn: deleteAdminEmailSettings,
    onSuccess: () => {
      setRemoving(false);
      void queryClient.invalidateQueries({ queryKey: queryKeys.adminEmail });
      toast("Email server removed.");
    },
  });

  const fields = save.error instanceof ApiError ? save.error.fieldErrorMap() : {};
  const general =
    save.error && Object.keys(fields).length === 0 ? errorMessage(save.error) : undefined;

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    save.mutate();
  }

  return (
    <section aria-labelledby="connection-heading" className="flex flex-col gap-4">
      <h2 id="connection-heading" className="text-lead font-semibold">
        SMTP server
      </h2>
      <form onSubmit={onSubmit} className="flex flex-col gap-[18px]">
        {general && <Alert>{general}</Alert>}
        <Field
          label="Host"
          hint="A host name or IP address, without a port. A mail relay on a private network is fine."
          error={fields.host}
        >
          {(c) => (
            <Input
              {...c}
              name="host"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              required
              maxLength={253}
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
          )}
        </Field>
        <div className="grid gap-[18px] sm:grid-cols-2">
          <Field label="Connection security" error={fields.tls}>
            {(c) => (
              <Select
                {...c}
                value={tls}
                onChange={(e) => {
                  const next = e.target.value as Tls;
                  if (port === String(DEFAULT_PORT[tls])) setPort(String(DEFAULT_PORT[next]));
                  setTls(next);
                }}
              >
                <option value="starttls">STARTTLS (port 587)</option>
                <option value="tls">TLS (port 465)</option>
                <option value="none">None (localhost only)</option>
              </Select>
            )}
          </Field>
          <Field label="Port" error={fields.port}>
            {(c) => (
              <Input
                {...c}
                name="port"
                type="number"
                inputMode="numeric"
                min={1}
                max={65535}
                required
                value={port}
                onChange={(e) => setPort(e.target.value)}
              />
            )}
          </Field>
        </div>
        <Field
          label="Sender"
          hint="For example Proreta <proreta@example.com>. Use an address your domain's SPF and DKIM allow the server to send for."
          error={fields.from}
        >
          {(c) => (
            <Input
              {...c}
              name="from"
              autoComplete="off"
              spellCheck={false}
              required
              maxLength={320}
              value={from}
              onChange={(e) => setFrom(e.target.value)}
            />
          )}
        </Field>
        <Field
          label="Username (optional)"
          hint="Leave empty for a relay that trusts the network. The login is only sent over TLS."
          error={fields.username}
        >
          {(c) => (
            <Input
              {...c}
              name="username"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={256}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          )}
        </Field>
        {username.trim() !== "" &&
          (askPassword ? (
            <Field
              label="Password"
              hint={
                reenter
                  ? "Enter the password again: the host, port or username changed, and the stored password is not sent to a new place."
                  : "Stored encrypted. Nobody can read it back."
              }
              error={fields.password}
            >
              {(c) => (
                <Input
                  {...c}
                  name="password"
                  type="password"
                  autoComplete="new-password"
                  required
                  maxLength={512}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              )}
            </Field>
          ) : (
            <WriteOnlyValue
              label="Password"
              hint="Leave it to keep the stored password."
              onReplace={() => setReplacing(true)}
            />
          ))}
        <p className="text-caption text-muted-foreground">
          Changing the host, port, security, sender, username or password forgets the last
          successful test and turns the switches below off.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" busy={save.isPending}>
            {save.isPending ? "Saving…" : "Save server"}
          </Button>
          {settings.configured && (
            <Button type="button" variant="secondary" onClick={() => setRemoving(true)}>
              Remove server
            </Button>
          )}
        </div>
      </form>
      {removing && (
        <ConfirmDialog
          title="Remove the email server?"
          confirmLabel="Remove server"
          busy={remove.isPending}
          onCancel={() => setRemoving(false)}
          onConfirm={() => remove.mutate()}
        >
          <p>
            The settings and the stored password are erased and every switch goes off. Nothing is
            emailed until you set a server up again.
          </p>
          {remove.error && <Alert>{errorMessage(remove.error)}</Alert>}
        </ConfirmDialog>
      )}
    </section>
  );
}

function TestResult({ test }: { test: AdminEmailTestResult | null | undefined }) {
  if (!test)
    return <p className="text-muted-foreground">No test message was sent with these settings.</p>;
  if (test.state === "pending") {
    return (
      <p role="status" className="text-muted-foreground">
        Sending the test message… (requested {formatDateTime(test.requestedAt)})
      </p>
    );
  }
  const when = formatDateTime(test.finishedAt ?? test.requestedAt);
  if (test.state === "succeeded") {
    return (
      <p role="status">
        <Badge tone="neutral">Succeeded</Badge> The test message was accepted by the server on{" "}
        {when}.
      </p>
    );
  }
  const category = test.error ?? "other";
  return (
    <Alert title={`The test failed on ${when}`}>
      {FAILURES[category] ?? FAILURES.other}{" "}
      <span className="text-muted-foreground">
        ({category}
        {test.smtpCode ? `, SMTP ${test.smtpCode}` : ""})
      </span>
    </Alert>
  );
}

function TestSection({ settings }: { settings: AdminEmailSettings }) {
  const queryClient = useQueryClient();
  const auth = useAuth();
  const login = auth.session?.user.login ?? "";
  const ownAddress = login.includes("@") ? login : "";
  const [to, setTo] = useState(ownAddress);
  const pending = settings.lastTest?.state === "pending";
  const send = useMutation({
    mutationFn: () => sendAdminTestEmail(to.trim()),
    onSuccess: (next) => queryClient.setQueryData(queryKeys.adminEmail, next),
  });
  const err = send.error;
  const fields = err instanceof ApiError ? err.fieldErrorMap() : {};
  const text =
    err instanceof ApiError && err.status === 429
      ? "Test emails are limited to one every 10 seconds. Try again shortly."
      : err && !fields.to
        ? errorMessage(err)
        : undefined;
  return (
    <section aria-labelledby="test-heading" className="flex flex-col gap-4">
      <h2 id="test-heading" className="text-lead font-semibold">
        Test
      </h2>
      <p className="text-muted-foreground">
        Sends a real message through the saved settings. The switches below need a successful test
        of the current settings.
      </p>
      {text && <Alert>{text}</Alert>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          send.mutate();
        }}
        className="flex flex-col gap-[18px]"
      >
        <Field
          label="Send the test to"
          hint={ownAddress ? undefined : "Your login is not an email address: enter one."}
          error={fields.to}
        >
          {(c) => (
            <Input
              {...c}
              type="email"
              name="to"
              autoComplete="off"
              required={!ownAddress}
              maxLength={254}
              value={to}
              onChange={(e) => setTo(e.target.value)}
            />
          )}
        </Field>
        <div>
          <Button
            type="submit"
            variant="secondary"
            busy={send.isPending || pending}
            disabled={!settings.configured}
          >
            {pending ? "Sending…" : "Send test email"}
          </Button>
        </div>
      </form>
      {!settings.configured && (
        <p className="text-caption text-muted-foreground">Save a server first.</p>
      )}
      <TestResult test={settings.lastTest} />
    </section>
  );
}

function Switches({ settings }: { settings: AdminEmailSettings }) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const set = useMutation({
    mutationFn: updateAdminEmailFeatures,
    onSuccess: (next) => queryClient.setQueryData(queryKeys.adminEmail, next),
    onError: (err) => toast(errorMessage(err), "error"),
  });
  const current = {
    sendInvitations: settings.sendInvitations,
    sendEmailVerification: settings.sendEmailVerification,
    allowPasswordReset: settings.allowPasswordReset,
  };
  const locked = !settings.tested;
  const change = (key: keyof typeof current) => (e: { target: { checked: boolean } }) =>
    set.mutate({ ...current, [key]: e.target.checked });
  return (
    <section aria-labelledby="switches-heading" className="flex flex-col gap-4">
      <h2 id="switches-heading" className="text-lead font-semibold">
        What Proreta emails
      </h2>
      <p className="text-muted-foreground">
        {locked
          ? "These stay off until a test message succeeds with the current settings. Without them Proreta sends nothing and shows every link in the page."
          : `The current settings passed a test${settings.testedAt ? ` on ${formatDateTime(settings.testedAt)}` : ""}. Turning a switch off deletes nothing.`}
      </p>
      <Checkbox
        label="Send invitation links by email"
        description="The invite dialog's Send by email option is checked by default. Admins can always email an invitation once the server is tested."
        checked={settings.sendInvitations}
        disabled={locked || set.isPending}
        onChange={change("sendInvitations")}
      />
      <Checkbox
        label="Send email verification at registration"
        description="New accounts get a link to confirm their address. Nothing requires it."
        checked={settings.sendEmailVerification}
        disabled={locked || set.isPending}
        onChange={change("sendEmailVerification")}
      />
      <Checkbox
        label="Allow password reset by email"
        description="Shows Forgot your password? on the sign-in page. While off, reset links already sent do not work either; they work again, until they expire, if you turn it back on. Your own temporary-password action is not affected."
        checked={settings.allowPasswordReset}
        disabled={locked || set.isPending}
        onChange={change("allowPasswordReset")}
      />
    </section>
  );
}
