import { Link } from "react-router-dom";
import SettingsSection from "../components/SettingsSection";
import { Badge } from "../components/ui";
import { usePermissions } from "../lib/permissions";
import { ROLE_LABELS } from "../lib/roles";

const link = "underline underline-offset-3";

interface Entry {
  to: string;
  label: string;
  description: string;
}

function Entries({ items }: { items: Entry[] }) {
  return (
    <ul className="flex flex-col divide-y divide-border-faint border-y border-border-faint">
      {items.map((e) => (
        <li key={e.to + e.label} className="flex flex-col gap-0.5 py-3">
          <Link to={e.to} className={`${link} w-fit font-medium`}>
            {e.label}
          </Link>
          <span className="text-[13px] text-muted-foreground">{e.description}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Where the organization and the projects are managed, by permission: each entry is shown only to
 * people who hold the permission it needs, and leads to the screen that does the work.
 */
export default function SettingsAreas() {
  const can = usePermissions();
  const members = can.org("organization.members.manage");
  const organization: Entry[] = [
    can.org("organization.members.read") && {
      to: "/members",
      label: members ? "People and invitations" : "People",
      description: members
        ? "Organization roles, invitation links and removing people."
        : "Who belongs to the organization and with which role.",
    },
    can.org("project.read") && {
      to: "/projects",
      label: "Projects",
      description: can.org("project.create")
        ? "Create, rename and delete projects, and give people roles in them."
        : "The projects of the organization and who works in them.",
    },
    can.org("channel.manage") && {
      to: "/channels",
      label: "Notification channels",
      description: "Channels of every project and the channels shared by all projects.",
    },
    can.org("organization.statuspages.manage") && {
      to: "/status-page",
      label: "Status pages",
      description: "Pages of every project and the pages of the organization.",
    },
    can.org("organization.domains.manage") && {
      to: "/settings#sign-in-domains-heading",
      label: "Sign-in domains",
      description: "Who may join with a Google Workspace account.",
    },
    can.org("usage.read") && {
      to: "/settings#plan-heading",
      label: "Plan and usage",
      description: "The plan and what the organization uses of its limits.",
    },
  ].filter((e): e is Entry => e !== false);

  const managed = can.projects.filter(
    (p) =>
      p.permissions.includes("project.members.manage") ||
      p.permissions.includes("channel.manage") ||
      p.permissions.includes("statuspage.manage"),
  );

  return (
    <>
      <SettingsSection
        id="roles"
        title="Roles"
        description="What each role may do, and which roles you hold."
      >
        <p>
          <Link to="/help/roles" className={link}>
            Roles and permissions
          </Link>
        </p>
      </SettingsSection>
      {organization.length > 0 && (
        <SettingsSection
          id="organization"
          title="Organization"
          description="Settings that apply to the whole organization."
        >
          <Entries items={organization} />
        </SettingsSection>
      )}
      {managed.length > 0 && (
        <SettingsSection
          id="projects"
          title="Projects"
          description="The projects you manage. A project role applies in that project only."
        >
          <ul className="flex flex-col divide-y divide-border-faint border-y border-border-faint">
            {managed.map((p) => (
              <li key={p.projectId} className="flex flex-col gap-1.5 py-3">
                <span className="flex items-center gap-2">
                  <strong className="font-medium">{p.name}</strong>
                  <Badge>{ROLE_LABELS[p.role]}</Badge>
                </span>
                <span className="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">
                  {p.permissions.includes("project.members.manage") && (
                    <Link
                      to={`/projects?project=${p.projectId}`}
                      aria-label={`People and invitations of ${p.name}`}
                      className={link}
                    >
                      People and invitations
                    </Link>
                  )}
                  {p.permissions.includes("channel.manage") && (
                    <Link to="/channels" aria-label={`Channels of ${p.name}`} className={link}>
                      Channels
                    </Link>
                  )}
                  {p.permissions.includes("statuspage.manage") && (
                    <Link
                      to={`/status-page?project=${p.projectId}`}
                      aria-label={`Status pages of ${p.name}`}
                      className={link}
                    >
                      Status pages
                    </Link>
                  )}
                </span>
              </li>
            ))}
          </ul>
        </SettingsSection>
      )}
    </>
  );
}
