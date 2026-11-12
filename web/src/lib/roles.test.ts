import type { SessionInfo } from "../api/endpoints";
import {
  abilities,
  assignableRoles,
  canManageMember,
  PROJECTS_ONLY_LABEL,
  roleAtLeast,
  roleLabel,
} from "./roles";

const as = (role: SessionInfo["role"]) => ({ role }) as SessionInfo;

describe("roles", () => {
  it("orders the roles", () => {
    expect(roleAtLeast("owner", "maintainer")).toBe(true);
    expect(roleAtLeast("member", "member")).toBe(true);
    expect(roleAtLeast("viewer", "member")).toBe(false);
    expect(roleAtLeast(undefined, "viewer")).toBe(false);
    expect(roleAtLeast(null, "viewer")).toBe(false);
  });

  it("names a missing organization role", () => {
    expect(roleLabel(null)).toBe(PROJECTS_ONLY_LABEL);
    expect(roleLabel("maintainer")).toBe("Maintainer");
  });

  it("gives a person without an organization role only what their projects give", () => {
    const none = as(null);
    expect(abilities(none)).toMatchObject({
      hasOrganizationRole: false,
      editMonitors: false,
      manageChannels: false,
      manageMembers: false,
      manageStatusPage: false,
      seeUsage: false,
      deleteOrganization: false,
    });
    expect(abilities(none, [{ role: "viewer" }])).toMatchObject({ editMonitors: false });
    expect(abilities(none, [{ role: "viewer" }, { role: "member" }])).toMatchObject({
      editMonitors: true,
      manageChannels: false,
    });
    // A project maintainer manages channels but holds no organization permission.
    expect(abilities(none, [{ role: "maintainer" }])).toMatchObject({
      manageChannels: true,
      manageMembers: false,
      manageStatusPage: true,
      seeUsage: false,
    });
    expect(abilities(none, [{ role: "member" }])).toMatchObject({ manageStatusPage: false });
    // A project role raises an organization viewer in that project.
    expect(abilities(as("viewer"), [{ role: "maintainer" }])).toMatchObject({
      editMonitors: true,
      manageChannels: true,
      manageMembers: false,
    });
  });

  it("derives what a session may do", () => {
    expect(abilities(as("viewer"))).toMatchObject({
      editMonitors: false,
      manageChannels: false,
      manageMembers: false,
      deleteOrganization: false,
    });
    expect(abilities(as("member"))).toMatchObject({ editMonitors: true, manageChannels: false });
    expect(abilities(as("maintainer"))).toMatchObject({
      manageChannels: true,
      manageMembers: true,
      deleteOrganization: false,
    });
    expect(abilities(as("owner")).deleteOrganization).toBe(true);
    expect(abilities(null).editMonitors).toBe(false);
  });

  it("never offers a role above the actor's own", () => {
    expect(assignableRoles("owner")).toEqual(["owner", "maintainer", "member", "viewer"]);
    expect(assignableRoles("maintainer")).toEqual(["maintainer", "member", "viewer"]);
    expect(assignableRoles("member")).toEqual([]);
    expect(assignableRoles(null)).toEqual([]);
  });

  it("keeps admins away from owners", () => {
    expect(canManageMember("maintainer", "owner")).toBe(false);
    expect(canManageMember("maintainer", "maintainer")).toBe(true);
    expect(canManageMember("owner", "owner")).toBe(true);
    expect(canManageMember("member", "viewer")).toBe(false);
    // A person without an organization role may be managed by any maintainer; nobody without a role manages.
    expect(canManageMember("maintainer", null)).toBe(true);
    expect(canManageMember(null, "viewer")).toBe(false);
  });
});
