import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";
import OrganizationSwitcher from "./OrganizationSwitcher";
import ThemeToggle from "./ThemeToggle";
import { Button } from "./ui";

/** Two letters for the account button: "jonathan.whitfield@x" → "JW", "alice@x" → "AL". */
export function initials(name: string): string {
  const parts = (name.split("@")[0] ?? "").split(/[._\-\s]+/).filter(Boolean);
  const letters =
    parts.length > 1 ? (parts[0]?.[0] ?? "") + (parts[1]?.[0] ?? "") : (parts[0] ?? "").slice(0, 2);
  return letters.toUpperCase() || "?";
}

/**
 * The account square in the top bar; it opens a panel with the login, the organization, the
 * theme and Log out. Escape, a click outside and a navigation close it.
 */
export default function UserMenu({ organization = true }: { organization?: boolean }) {
  const auth = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  // Keyed by the history entry: any navigation closes the panel, and it stays closed on Back.
  const [openFor, setOpenFor] = useState<string | null>(null);
  if (openFor !== null && openFor !== location.key) setOpenFor(null);
  const open = openFor === location.key;
  const root = useRef<HTMLDivElement>(null);
  const user = auth.session?.user;

  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpenFor(null);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);

  if (!user) return null;
  const name = user.login;

  async function onLogout() {
    await auth.logout();
    void navigate("/login", { replace: true });
  }

  return (
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- Escape closes the panel from any control inside; the wrapper is not a control.
    <div
      ref={root}
      className="relative"
      onKeyDown={(e) => {
        if (e.key === "Escape" && open) {
          setOpenFor(null);
          root.current?.querySelector("button")?.focus();
        }
      }}
    >
      <Button
        size="square"
        // Starts with the visible initials, so speech input can name it (WCAG 2.5.3).
        aria-label={`${initials(name)}, account`}
        aria-expanded={open}
        aria-controls="account-panel"
        title={name}
        className="font-mono text-caption font-semibold"
        onClick={() => setOpenFor(open ? null : location.key)}
      >
        {initials(name)}
      </Button>
      {open && (
        <div
          id="account-panel"
          className="absolute top-full right-0 z-50 mt-2 flex w-72 max-w-[calc(100vw-2rem)] flex-col border border-border bg-popover shadow-overlay"
        >
          <div className="flex flex-col gap-0.5 border-b border-border-faint px-4 py-3">
            <span className="eyebrow">Signed in as</span>
            <span className="font-mono text-mono break-words">{name}</span>
          </div>
          {organization && (
            <div className="flex flex-col gap-1.5 border-b border-border-faint px-4 py-3">
              <span className="eyebrow">Organization</span>
              <OrganizationSwitcher className="max-w-none" />
            </div>
          )}
          <div className="flex items-center justify-between gap-3 border-b border-border-faint px-4 py-2">
            <span className="text-caption text-muted-foreground">Theme</span>
            <ThemeToggle />
          </div>
          <div className="flex items-center justify-between gap-2 px-2 py-2">
            <Link to="/profile" className="px-2 py-1.5 text-caption hover:underline">
              Profile
            </Link>
            <Button variant="ghost" size="small" onClick={() => void onLogout()}>
              Log out
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
