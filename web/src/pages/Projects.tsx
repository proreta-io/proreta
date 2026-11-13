import { Link, useSearchParams } from "react-router-dom";
import ProjectInvitations from "../components/ProjectInvitations";
import ProjectsPanel from "../components/ProjectsPanel";
import { useOrganizationPeople } from "../lib/people";
import { usePermissions } from "../lib/permissions";

/** Projects of the organization and who belongs to them. */
export default function Projects() {
  const can = usePermissions();
  const people = useOrganizationPeople();
  // The settings area links here with ?project= to open the people of one project.
  const [params] = useSearchParams();

  return (
    <section className="flex max-w-[960px] flex-col gap-6">
      <div className="flex flex-col gap-2 pt-6 sm:pt-10">
        <h1 className="text-headline leading-[1.1] font-semibold tracking-[-0.02em] sm:text-display">
          Projects
        </h1>
        <p className="text-muted-foreground">
          Projects split the monitors and notification channels of the organization. A role on the
          organization applies to every project; a role in a project applies there only and can
          raise the organization role but never lowers it. People without an organization role see
          only the projects they have a role in.{" "}
          {/* Only for those who see the people there: the others find just Leave on Members. */}
          {can.org("organization.members.read") && (
            <>
              The roles of one person in every project are on{" "}
              <Link to="/members" className="underline underline-offset-3">
                Members
              </Link>
              .{" "}
            </>
          )}
          <Link to="/help/roles" className="underline underline-offset-3">
            What each role may do
          </Link>
        </p>
      </div>
      <ProjectsPanel
        api={people}
        initialOpen={params.get("project")}
        heading="h2"
        extra={(p) =>
          "role" in p &&
          can.inProject(p.id, "project.members.manage") && (
            <ProjectInvitations key={`invitations-${p.id}`} project={p} />
          )
        }
      />
    </section>
  );
}
