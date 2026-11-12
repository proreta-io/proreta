import {
  assignableRoles,
  canManageMember,
  PROJECTS_ONLY_LABEL,
  roleAtLeast,
  roleLabel,
} from "./roles";

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
