import type { AdminOrganization } from "../../api/endpoints";
import ProjectsPanel from "../../components/ProjectsPanel";
import { adminPeople } from "../../lib/people";

/** Projects of one organization and who belongs to them; the platform admin acts as its owner. */
export default function AdminOrganizationProjects({
  organization,
}: {
  organization: AdminOrganization;
}) {
  return (
    <section aria-labelledby="projects-heading" className="flex flex-col gap-3">
      <h2 id="projects-heading" className="text-lead font-semibold">
        Projects
      </h2>
      <ProjectsPanel api={adminPeople(organization)} />
    </section>
  );
}
