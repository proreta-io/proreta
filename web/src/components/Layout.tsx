import { useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";
import { usePermissions } from "../lib/permissions";
import ErrorBoundary from "./ErrorBoundary";
import ProjectSwitcher from "./ProjectSwitcher";
import UserMenu from "./UserMenu";
import VerifyEmailBanner from "./VerifyEmailBanner";
import { Button, buttonClass, cx } from "./ui";

/** The page width shared by the top bar and the content, so their edges line up. */
export const PAGE = "mx-auto w-full max-w-[1200px] px-4 sm:px-8";

const navClass = ({ isActive }: { isActive: boolean }) =>
  cx(
    "flex items-center whitespace-nowrap",
    isActive
      ? "font-medium shadow-[inset_0_-2px_0_var(--foreground)]"
      : "text-muted-foreground hover:text-foreground",
  );

export default function Layout({ children }: { children?: ReactNode }) {
  const auth = useAuth();
  const location = useLocation();
  // Keyed by path below, so the phone menu closes on every navigation.
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const menuOpen = menuFor === location.pathname;
  const isAdmin = auth.session?.user.isPlatformAdmin === true;
  const can = usePermissions();

  const links = (
    <>
      <NavLink to="/" end className={navClass}>
        Monitors
      </NavLink>
      {can.anywhere("statuspage.read") && (
        <NavLink to="/status-page" className={navClass}>
          Status pages
        </NavLink>
      )}
      {can.anywhere("channel.read") && (
        <NavLink to="/channels" className={navClass}>
          Channels
        </NavLink>
      )}
      {can.anywhere("specula.read") && (
        <NavLink to="/specula" className={navClass}>
          Specula
        </NavLink>
      )}
      <NavLink to="/settings" className={navClass}>
        Settings
      </NavLink>
    </>
  );
  // The Admin Area is another shell, so it is a button next to the account, like its "Back to app".
  const adminLink = isAdmin && (
    <Link to="/admin" className={buttonClass("secondary", "small")}>
      Admin
    </Link>
  );

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="border-b border-border">
        <div className={cx(PAGE, "flex h-[52px] items-stretch justify-between")}>
          <div className="flex items-stretch gap-8">
            <Link
              to="/"
              className="flex items-center font-mono text-caption font-semibold tracking-[0.18em]"
            >
              PRORETA
            </Link>
            <nav aria-label="Main" className="flex items-stretch gap-[22px] max-sm:hidden">
              {links}
            </nav>
          </div>
          <div className="flex items-center gap-3">
            <span className="flex items-center gap-3 max-sm:hidden">
              <ProjectSwitcher />
              {adminLink}
            </span>
            <Button
              variant="ghost"
              aria-expanded={menuOpen}
              aria-controls="phone-menu"
              className="sm:hidden"
              onClick={() => setMenuFor(menuOpen ? null : location.pathname)}
            >
              <span aria-hidden="true" className="flex w-3.5 flex-col gap-[3px]">
                <span className="h-[1.5px] bg-current" />
                <span className="h-[1.5px] bg-current" />
                <span className="h-[1.5px] bg-current" />
              </span>
              Menu
            </Button>
            <UserMenu />
          </div>
        </div>
        {menuOpen && (
          // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- Escape closes the menu from any of its links; it is not a control itself.
          <div
            id="phone-menu"
            className="flex flex-col border-t border-border-faint px-4 pb-3 sm:hidden"
            onKeyDown={(e) => {
              if (e.key === "Escape") setMenuFor(null);
            }}
          >
            <nav aria-label="Main" className="flex flex-col [&>a]:h-11 [&>a]:shadow-none">
              {links}
            </nav>
            <div className="flex items-center justify-between gap-3 border-t border-border-faint pt-3 empty:hidden">
              <ProjectSwitcher className="max-w-none flex-1" />
              {adminLink}
            </div>
          </div>
        )}
      </header>
      <div className={PAGE}>
        <VerifyEmailBanner />
      </div>
      <main className={cx(PAGE, "flex flex-1 flex-col pb-16")}>
        {/* Keyed by path so an error on one page does not stick after navigating away. */}
        <ErrorBoundary key={location.pathname}>{children ?? <Outlet />}</ErrorBoundary>
      </main>
    </div>
  );
}
