package authz

import "slices"

// Held returns the permissions the actor holds through the organization: the account level for
// everybody who belongs, and every permission of the organization role, which for a project
// permission means in every project. A person without an organization role holds the account
// level only.
func (a Actor) Held() []Permission {
	out := []Permission{}
	for _, d := range permissions {
		if (d.Level == LevelAccount && a.Belongs()) || (d.Level != LevelAccount && a.Holds(d.Name)) {
			out = append(out, d.Name)
		}
	}
	return out
}

// ProjectPermissions returns the project permissions a role holds inside a project.
func ProjectPermissions(role Role) []Permission {
	out := []Permission{}
	for _, d := range permissions {
		if d.Level == LevelProject && Has(role, d.Name) {
			out = append(out, d.Name)
		}
	}
	return out
}

// RolesWith returns the roles that hold the permission, highest first.
func RolesWith(p Permission) []Role {
	out := []Role{}
	for _, r := range Roles() {
		if slices.Contains(grants[r], p) {
			out = append(out, r)
		}
	}
	return out
}
