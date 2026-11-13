import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { ApiError, errorMessage } from "../api/client";
import { queryKeys, verifyEmail } from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import AuthScreen, { AuthHeading } from "../components/AuthScreen";
import { Alert, buttonClass, cx, Loading, Skeleton } from "../components/ui";
import { formatDuration } from "../lib/format";

/**
 * The page the link in a verification email opens. The token is in the URL fragment, which the
 * browser never sends to a server; it goes to the API only in the request body. It is read once
 * into component state, removed from the address bar and the history entry, used once, and
 * never put into a redirect or router state. It needs no session: the token proves control of
 * the mailbox, so the link works from any browser.
 */
export default function VerifyEmail() {
  const location = useLocation();
  const [token] = useState(() => location.hash.replace(/^#/, ""));
  useEffect(() => {
    if (globalThis.location.hash) {
      globalThis.history.replaceState(globalThis.history.state, "", "/verify-email");
    }
  }, []);
  const auth = useAuth();
  const queryClient = useQueryClient();
  const verify = useMutation({
    mutationFn: verifyEmail,
    // A signed-in user sees the banner go away.
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: queryKeys.me }),
  });
  const started = useRef(false);
  useEffect(() => {
    if (token === "" || started.current) return;
    started.current = true;
    verify.mutate(token);
  }, [token, verify]);

  const next = auth.session ? (
    <Link to="/" className={cx(buttonClass(), "mt-1.5")}>
      Continue to Proreta
    </Link>
  ) : (
    <Link to="/login" className={cx(buttonClass(), "mt-1.5")}>
      Go to sign in
    </Link>
  );

  if (token === "") {
    return (
      <Screen>
        <Alert tone="neutral" title="This link is incomplete.">
          Open the whole link from the email, or request a new one from the banner in Proreta.
        </Alert>
        {next}
      </Screen>
    );
  }
  if (verify.isSuccess) {
    return (
      <Screen>
        <Alert tone="neutral" title="Your email address is verified.">
          Thank you. You can close this page.
        </Alert>
        {next}
      </Screen>
    );
  }
  if (verify.isError) {
    const err = verify.error;
    if (err instanceof ApiError && err.status === 429) {
      return (
        <Screen>
          <Alert tone="neutral" title="Too many attempts.">
            {err.retryAfter
              ? `Try again in ${formatDuration(err.retryAfter)}.`
              : "Try again later."}
          </Alert>
          {next}
        </Screen>
      );
    }
    if (err instanceof ApiError && err.status === 404) {
      return (
        <Screen>
          <Alert tone="neutral" title="This link is not valid.">
            It may have expired, been used or been replaced by a newer email. Sign in and request a
            new one from the banner at the top of the page.
          </Alert>
          {next}
        </Screen>
      );
    }
    return (
      <Screen>
        <Alert title="Cannot verify the address.">{errorMessage(err)}</Alert>
        {next}
      </Screen>
    );
  }
  return (
    <Screen>
      <Loading label="Verifying your email address…">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="w-full" />
      </Loading>
    </Screen>
  );
}

function Screen({ children }: { children: React.ReactNode }) {
  return (
    <AuthScreen>
      <AuthHeading>Verify your email</AuthHeading>
      {children}
    </AuthScreen>
  );
}
