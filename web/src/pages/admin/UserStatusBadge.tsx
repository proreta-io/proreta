import type { AdminUserSummary } from "../../api/endpoints";
import { Badge, DiamondShape } from "../../components/ui";

/** "Disabled" is amber like a suspended organization: red is for outages. */
export function UserStatusBadge({ status }: Pick<AdminUserSummary, "status">) {
  if (status === "disabled")
    return (
      <Badge tone="warning" icon={<DiamondShape />}>
        Disabled
      </Badge>
    );
  return <Badge>Active</Badge>;
}
