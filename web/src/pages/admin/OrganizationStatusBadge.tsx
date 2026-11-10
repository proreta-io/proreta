import type { AdminOrganization } from "../../api/endpoints";
import { Badge, DiamondShape } from "../../components/ui";

/** "Deleted", "Suspended" or "Active"; a deleted organization is also suspended in the API. */
export function organizationStatusText(
  organization: Pick<AdminOrganization, "status" | "deletedAt">,
): string {
  if (organization.deletedAt) return "Deleted";
  return organization.status === "suspended" ? "Suspended" : "Active";
}

/** The row tone for an organization: red is for outages, so a suspension is amber. */
export function organizationTone(
  organization: Pick<AdminOrganization, "status" | "deletedAt">,
): "default" | "warning" | "deleted" {
  if (organization.deletedAt) return "deleted";
  return organization.status === "suspended" ? "warning" : "default";
}

export function OrganizationStatusBadge({
  organization,
}: {
  organization: Pick<AdminOrganization, "status" | "deletedAt">;
}) {
  const text = organizationStatusText(organization);
  if (text === "Deleted") return <Badge tone="deleted">{text}</Badge>;
  if (text === "Suspended")
    return (
      <Badge tone="warning" icon={<DiamondShape />}>
        {text}
      </Badge>
    );
  return <Badge>{text}</Badge>;
}
