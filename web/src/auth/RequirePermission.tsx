import { Outlet } from "react-router-dom";
import { Loading, Skeleton } from "../components/ui";
import NotFound from "../pages/NotFound";
import { usePermissions } from "../lib/permissions";

/**
 * Renders the child routes only when the permission is held (anywhere: through the organization
 * role or in at least one project; `organization` limits it to the organization role). Without it
 * the page is the same as an unknown address, so a route does not reveal what exists.
 */
export default function RequirePermission({
  permission,
  organization,
}: {
  permission: string;
  organization?: boolean;
}) {
  const can = usePermissions();
  if (!can.ready) {
    return (
      <Loading label="Loading…" className="pt-10">
        <Skeleton className="h-8 w-64" />
      </Loading>
    );
  }
  const held = organization ? can.org(permission) : can.anywhere(permission);
  return held ? <Outlet /> : <NotFound />;
}
