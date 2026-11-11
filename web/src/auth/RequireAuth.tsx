import { Navigate, Outlet, useLocation } from "react-router-dom";
import { errorMessage, errorMeta } from "../api/client";
import SetNewPassword from "../components/SetNewPassword";
import { ErrorState } from "../components/ui";
import { useAuth } from "./AuthProvider";

/** Renders the child routes only with a session (and a password of the user's own); otherwise redirects to /login keeping the target. */
export default function RequireAuth() {
  const auth = useAuth();
  const location = useLocation();

  if (auth.status === "loading") {
    return (
      <main className="mx-auto max-w-[560px] px-4 py-16 text-muted-foreground">
        <p role="status">Checking your session…</p>
      </main>
    );
  }
  if (auth.status === "error") {
    return (
      <main className="mx-auto max-w-[560px] px-4 py-16">
        <ErrorState
          title="Cannot load your session"
          detail={errorMessage(auth.error)}
          meta={errorMeta(auth.error)}
          onRetry={auth.retry}
        />
      </main>
    );
  }
  if (!auth.session) {
    const from = location.pathname + location.search + location.hash;
    return <Navigate to="/login" replace state={{ from }} />;
  }
  // Nothing of the app shows until the password issued by somebody else is replaced; the same
  // address continues afterwards.
  if (auth.session.mustChangePassword) return <SetNewPassword />;
  return <Outlet />;
}

/** The path to return to after login: only same-app paths, never back to /login or /register. */
export function redirectTarget(state: unknown): string {
  const from =
    typeof state === "object" && state !== null ? (state as { from?: unknown }).from : undefined;
  if (
    typeof from !== "string" ||
    !from.startsWith("/") ||
    from.startsWith("//") ||
    from.includes("\\")
  ) {
    return "/";
  }
  for (const page of ["/login", "/register"]) {
    if (from === page || from.startsWith(`${page}?`)) return "/";
  }
  return from;
}
