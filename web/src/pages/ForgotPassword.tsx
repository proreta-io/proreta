import { useMutation, useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, errorMessage } from "../api/client";
import { getAuthProviders, queryKeys, requestPasswordReset } from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import AuthScreen, { AuthHeading, authLink } from "../components/AuthScreen";
import { Alert, Button, buttonClass, cx, Field, Input, Loading, Skeleton } from "../components/ui";
import { formatDuration } from "../lib/format";
import { normalizeEmail } from "../lib/registerForm";

/**
 * Asks for a password reset link. The page says the same thing whether or not the address has an
 * account, because the server does: nobody can use it to find out who is registered.
 */
export default function ForgotPassword() {
  const auth = useAuth();
  // Not before the session check has answered: a 401 from it drops every cached query, which
  // would swallow a request still in flight and leave this page loading for good.
  const providers = useQuery({
    queryKey: queryKeys.authProviders,
    queryFn: getAuthProviders,
    enabled: auth.status !== "loading",
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
  const [email, setEmail] = useState("");
  const send = useMutation({ mutationFn: (address: string) => requestPasswordReset(address) });

  if (providers.isPending) {
    return (
      <AuthScreen>
        <Loading label="Loading…">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-11 w-full" />
        </Loading>
      </AuthScreen>
    );
  }
  const available = providers.data?.emailEnabled === true && providers.data.passwordLoginEnabled;
  if (!available) {
    return (
      <AuthScreen>
        <AuthHeading>Forgot your password?</AuthHeading>
        <Alert tone="neutral" title="This installation cannot email a reset link.">
          Ask the person who runs Proreta, or an administrator of your organization, to reset your
          password.
        </Alert>
        <Link to="/login" className={cx(buttonClass(), "mt-1.5")}>
          Back to sign in
        </Link>
      </AuthScreen>
    );
  }
  if (send.isSuccess) {
    return (
      <AuthScreen>
        <AuthHeading>Check your email</AuthHeading>
        <Alert tone="neutral" title="If that address has an account, a link is on its way.">
          It works once and for an hour. If nothing arrives in a few minutes, check the spam folder
          or try again.
        </Alert>
        <Link to="/login" className={cx(buttonClass(), "mt-1.5")}>
          Back to sign in
        </Link>
      </AuthScreen>
    );
  }

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    send.mutate(normalizeEmail(email));
  }
  const err = send.error;
  const text =
    err instanceof ApiError && err.status === 429
      ? err.retryAfter
        ? `Too many requests. Try again in ${formatDuration(err.retryAfter)}.`
        : "Too many requests. Try again later."
      : err
        ? errorMessage(err)
        : undefined;
  const fieldError = err instanceof ApiError ? err.fieldErrorMap().email : undefined;

  return (
    <AuthScreen
      footer={
        <Link to="/login" className={authLink}>
          Back to sign in
        </Link>
      }
    >
      <AuthHeading>Forgot your password?</AuthHeading>
      <p className="text-muted-foreground">
        Enter the email address of your account and we will send you a link to choose a new
        password.
      </p>
      <form onSubmit={onSubmit} className="flex flex-col gap-[18px]">
        {text && !fieldError && <Alert>{text}</Alert>}
        <Field label="Email" error={fieldError}>
          {(c) => (
            <Input
              {...c}
              type="email"
              name="email"
              autoComplete="email"
              autoCapitalize="none"
              spellCheck={false}
              required
              readOnly={send.isPending}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          )}
        </Field>
        <Button type="submit" busy={send.isPending} className="mt-1.5">
          {send.isPending ? "Sending…" : "Send reset link"}
        </Button>
      </form>
    </AuthScreen>
  );
}
