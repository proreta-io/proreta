import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { Link, useLocation } from "react-router-dom";
import { ApiError, errorMessage } from "../api/client";
import { getAuthProviders, queryKeys, resetPassword } from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import AuthScreen, { AuthHeading, ShowPassword } from "../components/AuthScreen";
import { Alert, Button, buttonClass, cx, Field, Input, Loading, Skeleton } from "../components/ui";
import { formatDuration } from "../lib/format";
import { PASSWORD_MAX, PASSWORD_MIN } from "../lib/registerForm";

const codePoints = (s: string) => [...s].length;

/**
 * The page the link in a reset email opens. The token is in the URL fragment, which the browser
 * never sends to a server; it is read once into component state, removed from the address bar and
 * the history entry, and goes to the API only in the request body. Changing the password signs the
 * account out everywhere, so the page ends at the sign-in link.
 */
export default function ResetPassword() {
  const location = useLocation();
  const auth = useAuth();
  // Not before the session check has answered (see ForgotPassword).
  const providers = useQuery({
    queryKey: queryKeys.authProviders,
    queryFn: getAuthProviders,
    enabled: auth.status !== "loading",
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
  const [token] = useState(() => location.hash.replace(/^#/, ""));
  useEffect(() => {
    if (globalThis.location?.hash) {
      globalThis.history.replaceState(globalThis.history.state, "", "/reset-password");
    }
  }, []);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [shown, setShown] = useState(false);
  const [problem, setProblem] = useState<string | undefined>();
  const reset = useMutation({ mutationFn: () => resetPassword(token, password) });

  const toLogin = (
    <Link to="/login" className={cx(buttonClass(), "mt-1.5")}>
      Go to sign in
    </Link>
  );
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
  // Off (or not set up): the server answers 404 to the link as well, so say so before asking for a
  // password that could not be saved.
  if (providers.data?.passwordResetEnabled !== true) {
    return (
      <AuthScreen>
        <AuthHeading>Reset your password</AuthHeading>
        <Alert tone="neutral" title="Password reset by email is not available.">
          Ask the person who runs Proreta, or an administrator of your organization, to reset your
          password.
        </Alert>
        <Link to="/login" className={cx(buttonClass(), "mt-1.5")}>
          Back to sign in
        </Link>
      </AuthScreen>
    );
  }
  const expired = reset.error instanceof ApiError && reset.error.status === 404;
  if (token === "" || expired) {
    return (
      <AuthScreen>
        <AuthHeading>Reset your password</AuthHeading>
        <Alert
          tone="neutral"
          title={token === "" ? "This link is incomplete." : "This link is not valid."}
        >
          {token === ""
            ? "Open the whole link from the email."
            : "It may have expired, been used or been replaced by a newer email."}{" "}
          You can ask for a new one.
        </Alert>
        <Link to="/forgot-password" className={cx(buttonClass(), "mt-1.5")}>
          Send a new link
        </Link>
      </AuthScreen>
    );
  }
  if (reset.isSuccess) {
    return (
      <AuthScreen>
        <AuthHeading>Password changed</AuthHeading>
        <Alert tone="neutral" title="Your password is changed.">
          You were signed out everywhere. Sign in with the new password.
        </Alert>
        {toLogin}
      </AuthScreen>
    );
  }

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const n = codePoints(password);
    if (n < PASSWORD_MIN || n > PASSWORD_MAX) {
      setProblem(`Use ${PASSWORD_MIN} to ${PASSWORD_MAX} characters.`);
    } else if (password !== confirm) {
      setProblem("The two passwords are not the same.");
    } else {
      setProblem(undefined);
      reset.mutate();
    }
  }
  const err = reset.error;
  const serverText = !err
    ? undefined
    : err instanceof ApiError && err.status === 429
      ? err.retryAfter
        ? `Too many attempts. Try again in ${formatDuration(err.retryAfter)}.`
        : "Too many attempts. Try again later."
      : err instanceof ApiError && err.fieldErrorMap().password
        ? err.fieldErrorMap().password
        : errorMessage(err);

  return (
    <AuthScreen>
      <AuthHeading>Choose a new password</AuthHeading>
      <form onSubmit={onSubmit} className="flex flex-col gap-[18px]">
        {(problem ?? serverText) && <Alert>{problem ?? serverText}</Alert>}
        <Field
          label="New password"
          hint={`${PASSWORD_MIN} to ${PASSWORD_MAX} characters. A passphrase works well.`}
        >
          {(c) => (
            <Input
              {...c}
              type={shown ? "text" : "password"}
              name="password"
              autoComplete="new-password"
              required
              readOnly={reset.isPending}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              action={
                <ShowPassword
                  shown={shown}
                  plural
                  controls={c.id}
                  onToggle={() => setShown((s) => !s)}
                />
              }
            />
          )}
        </Field>
        <Field label="Confirm new password">
          {(c) => (
            <Input
              {...c}
              type={shown ? "text" : "password"}
              name="confirm"
              autoComplete="new-password"
              required
              readOnly={reset.isPending}
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          )}
        </Field>
        <Button type="submit" busy={reset.isPending} className="mt-1.5">
          {reset.isPending ? "Saving…" : "Change password"}
        </Button>
      </form>
    </AuthScreen>
  );
}
