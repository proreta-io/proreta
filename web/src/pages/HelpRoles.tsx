import { Link } from "react-router-dom";
import { Loading, Skeleton, Table, Td, Th, Tr } from "../components/ui";
import { usePermissions } from "../lib/permissions";
import { ROLE_LABELS } from "../lib/roles";
import type { MembershipRole } from "../api/endpoints";

const ROLES: MembershipRole[] = ["owner", "maintainer", "member", "viewer"];

const LEVELS = {
  account: "Account",
  organization: "Organization",
  project: "Project",
} as const;

/** What each role may do, from the server's own table, with the roles of the signed-in person marked. */
export default function HelpRoles() {
  const can = usePermissions();
  const orgRole = can.organizationRole;
  const projectRoles = (role: MembershipRole) =>
    can.projects.filter((p) => p.role === role).map((p) => p.name);

  return (
    <section className="flex max-w-[960px] flex-col gap-6">
      <div className="flex flex-col gap-2 pt-6 sm:pt-10">
        <h1 className="text-headline leading-[1.1] font-semibold tracking-[-0.02em] sm:text-display">
          Roles and permissions
        </h1>
        <p className="max-w-[720px] text-muted-foreground">
          The same four roles exist on the organization and on each project. A role on the
          organization applies to every project, and a role on a project applies to that project
          only. In a project the higher of the two applies, so a project role can raise what the
          organization role gives there but never lowers it. Organization permissions are held only
          through an organization role; a person who belongs through projects only never holds them.
        </p>
        <p>
          {orgRole ? (
            <>
              Your organization role:{" "}
              <strong className="font-medium">{ROLE_LABELS[orgRole]}</strong>.
            </>
          ) : (
            <>You have no organization role; you belong through projects only.</>
          )}{" "}
          <Link to="/settings" className="underline underline-offset-3">
            Settings
          </Link>
        </p>
      </div>
      {!can.ready ? (
        <Loading label="Loading roles…">
          <Skeleton className="w-2/3" />
          <Skeleton className="w-1/2" />
        </Loading>
      ) : (
        <Table>
          <caption className="sr-only">What each role may do</caption>
          <thead>
            <tr>
              <Th>Permission</Th>
              <Th>Level</Th>
              {ROLES.map((role) => {
                const mine = role === orgRole;
                const where = projectRoles(role);
                return (
                  <Th key={role}>
                    {ROLE_LABELS[role]}
                    {mine && <span className="block normal-case">Your organization role</span>}
                    {where.length > 0 && (
                      <span className="block normal-case">Your role in {where.join(", ")}</span>
                    )}
                  </Th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {can.catalogue.map((d) => (
              <Tr key={d.name}>
                <Td>
                  <span className="flex flex-col">
                    <code className="font-mono text-mono">{d.name}</code>
                    <span className="text-[13px] text-muted-foreground">{d.description}</span>
                  </span>
                </Td>
                <Td className="text-caption">{LEVELS[d.level]}</Td>
                {ROLES.map((role) => (
                  <Td key={role} className="text-caption">
                    {d.roles.includes(role) ? (
                      "Yes"
                    ) : (
                      <>
                        <span aria-hidden="true">–</span>
                        <span className="sr-only">No</span>
                      </>
                    )}
                  </Td>
                ))}
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
    </section>
  );
}
