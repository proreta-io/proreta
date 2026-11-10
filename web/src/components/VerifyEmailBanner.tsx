import { useMutation } from "@tanstack/react-query";
import { ApiError, errorMessage } from "../api/client";
import { resendEmailVerification } from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import { formatDuration } from "../lib/format";
import { useEmailEnabled } from "../lib/oauth";
import { useToast } from "./Toasts";
import { Alert, Button } from "./ui";

/**
 * Asks a signed-in user to verify their email address. Shown only when the installation sends
 * email and the login is an address that was not verified (the platform admin's login is never
 * one). Nothing depends on it: it is a reminder with a way to get the link again.
 */
export default function VerifyEmailBanner() {
  const auth = useAuth();
  const emailEnabled = useEmailEnabled();
  const toast = useToast();
  const resend = useMutation({
    mutationFn: resendEmailVerification,
    onSuccess: () => toast("Verification email sent. Check your inbox."),
    onError: (err) => {
      if (err instanceof ApiError && err.status === 429) {
        toast(
          err.retryAfter
            ? `Too many requests. Try again in ${formatDuration(err.retryAfter)}.`
            : "Too many requests. Try again later.",
          "error",
        );
      } else {
        toast(errorMessage(err), "error");
      }
    },
  });
  const user = auth.session?.user;
  if (
    !emailEnabled ||
    !user ||
    user.isPlatformAdmin ||
    user.emailVerified ||
    !user.login.includes("@")
  ) {
    return null;
  }
  return (
    <div className="pt-4">
      <Alert
        tone="warning"
        title="Your email address is not verified."
        action={
          <Button
            variant="secondary"
            size="small"
            disabled={resend.isPending}
            onClick={() => resend.mutate()}
          >
            Send verification email
          </Button>
        }
      >
        We will send a link to {user.login}. Opening it is all it takes.
      </Alert>
    </div>
  );
}
