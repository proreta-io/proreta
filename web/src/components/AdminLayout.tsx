import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import ErrorBoundary from "./ErrorBoundary";
import { PAGE } from "./Layout";
import UserMenu from "./UserMenu";
import { Badge, buttonClass, cx } from "./ui";

const navClass = ({ isActive }: { isActive: boolean }) =>
  cx(
    "flex shrink-0 items-center",
    isActive
      ? "font-medium shadow-[inset_0_-2px_0_var(--foreground)]"
      : "text-muted-foreground hover:text-foreground",
  );

/**
 * The Admin Area: its own shell with its own navigation and none of the application chrome
 * (no organization or project switcher), so it is clear that nothing here belongs to one
 * organization. The grey band is the signal that this is the privileged area.
 */
export default function AdminLayout() {
  const location = useLocation();

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="border-b border-border bg-muted">
        <div
          className={cx(PAGE, "flex min-h-[52px] flex-wrap items-center justify-between gap-x-6")}
        >
          <div className="flex items-center gap-4">
            <Link
              to="/admin"
              className="font-mono text-caption font-semibold tracking-[0.18em]"
              aria-label="Admin Area"
            >
              PRORETA
            </Link>
            <Badge tone="inverse">Platform admin</Badge>
          </div>
          <div className="flex items-center gap-3.5 py-1.5">
            <Link to="/" className={buttonClass("secondary", "small")}>
              Back to app
            </Link>
            <UserMenu organization={false} />
          </div>
        </div>
        <div className={cx(PAGE, "flex h-11 items-stretch overflow-x-auto")}>
          <nav aria-label="Admin" className="flex items-stretch gap-5">
            <NavLink to="/admin" end className={navClass}>
              Overview
            </NavLink>
            <NavLink to="/admin/organizations" className={navClass}>
              Organizations
            </NavLink>
            <NavLink to="/admin/users" className={navClass}>
              Users
            </NavLink>
            <NavLink to="/admin/domains" className={navClass}>
              Domains
            </NavLink>
            <NavLink to="/admin/plans" className={navClass}>
              Plans
            </NavLink>
            <NavLink to="/admin/email" className={navClass}>
              Email
            </NavLink>
            <NavLink to="/admin/registration" className={navClass}>
              Registration
            </NavLink>
            <NavLink to="/admin/retention" className={navClass}>
              Retention
            </NavLink>
          </nav>
        </div>
      </header>
      <main className={cx(PAGE, "flex flex-1 flex-col pb-16")}>
        <ErrorBoundary key={location.pathname}>
          <Outlet />
        </ErrorBoundary>
      </main>
    </div>
  );
}
