import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { errorMessage, errorMeta } from "../../api/client";
import { listAdminPlans, queryKeys } from "../../api/endpoints";
import {
  Badge,
  buttonClass,
  EmptyNote,
  ErrorState,
  Loading,
  Skeleton,
  Table,
  Td,
  Th,
  Tr,
} from "../../components/ui";
import { LIMITS, limitText } from "../../lib/planLimits";
import { adminTitle } from "./AdminOverview";

/** Every plan with its limits and the organizations on it; the seeded ones come first. */
export default function AdminPlans() {
  const plans = useQuery({ queryKey: queryKeys.adminPlans, queryFn: listAdminPlans });
  return (
    <section className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className={adminTitle}>Plans</h1>
        <Link to="/admin/plans/new" className={buttonClass("primary", "small")}>
          New plan
        </Link>
      </div>
      <p className="max-w-[720px] text-muted-foreground">
        A plan sets the limits of every organization on it. Editing a plan applies to all of them at
        once; nothing they already have is deleted, only new resources over a lowered limit are
        refused.
      </p>
      {plans.isPending && (
        <Loading label="Loading plans…">
          <Skeleton className="h-24 w-full" />
        </Loading>
      )}
      {plans.isError && (
        <ErrorState
          title="Cannot load plans"
          detail={errorMessage(plans.error)}
          meta={errorMeta(plans.error)}
          onRetry={() => void plans.refetch()}
        />
      )}
      {plans.data && plans.data.items.length === 0 && <EmptyNote>No plans.</EmptyNote>}
      {plans.data && plans.data.items.length > 0 && (
        <Table>
          <thead>
            <tr>
              <Th>Plan</Th>
              <Th numeric>Organizations</Th>
              <Th>Limits</Th>
            </tr>
          </thead>
          <tbody>
            {plans.data.items.map((plan) => (
              <Tr key={plan.key}>
                <Td>
                  <Link to={`/admin/plans/${plan.key}`} className="font-medium hover:underline">
                    {plan.name}
                  </Link>{" "}
                  {plan.system && <Badge>System</Badge>}
                  <span className="block font-mono text-mono text-subtle-foreground">
                    {plan.key}
                  </span>
                </Td>
                <Td numeric>{plan.organizations}</Td>
                <Td>
                  <ul className="m-0 flex list-none flex-wrap gap-x-4 gap-y-1 p-0 text-caption">
                    {LIMITS.map((l) => (
                      <li key={l.field}>
                        <span className="text-muted-foreground">{l.label}</span>{" "}
                        <span className="font-mono">{limitText(plan.limits[l.field])}</span>
                      </li>
                    ))}
                  </ul>
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
    </section>
  );
}
