// Package authz is the one place that decides what a role may do. It is pure: the
// api resolves the actor (user and role of the session's membership) per request and asks this
// package; services ask it again for decisions that depend on the target (member management).
//
// Operations are the operation ids of api/openapi.yaml. An operation missing from the table is
// denied, and a test in internal/api fails when the spec has an operation the table does not list.
package authz

import (
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Role is a membership role.
type Role string

// Roles, highest first.
const (
	Owner      Role = "owner"
	Maintainer Role = "maintainer"
	Member     Role = "member"
	Viewer     Role = "viewer"
)

// ErrForbidden means the actor's role does not allow the action.
var ErrForbidden = errors.New("authz: forbidden")

// rank orders the roles; an unknown role ranks below viewer and is allowed nothing.
func rank(r Role) int {
	switch r {
	case Owner:
		return 4
	case Maintainer:
		return 3
	case Member:
		return 2
	case Viewer:
		return 1
	}
	return 0
}

// Valid reports whether r is one of the four roles.
func Valid(r Role) bool { return rank(r) > 0 }

// AtLeast reports whether r is min or higher.
func (r Role) AtLeast(min Role) bool { return rank(r) > 0 && rank(r) >= rank(min) }

// Actor is the user of a request and its role in the session's organization.
type Actor struct {
	UserID uuid.UUID
	Role   Role
	// Projects is the effective role in each project the actor belongs to.
	// Owners and maintainers see every project and need no entries.
	Projects map[uuid.UUID]Role
}

// ErrProjectForbidden means the actor sees the project but its role there does not allow the
// change (a member with the viewer override).
var ErrProjectForbidden = errors.New("authz: forbidden in this project")

// ProjectRole is the role of a member or viewer inside a project: the organization role, lowered
// by the project's override when there is one (an override never raises the organization role).
func ProjectRole(org Role, override *Role) Role {
	if override != nil && Valid(*override) && rank(*override) < rank(org) {
		return *override
	}
	return org
}

// AllProjects reports whether the actor sees every project of the organization.
func (a Actor) AllProjects() bool { return a.Role.AtLeast(Maintainer) }

// RoleIn returns the actor's effective role in a project and whether it sees the project at all.
func (a Actor) RoleIn(project uuid.UUID) (Role, bool) {
	if a.AllProjects() {
		return a.Role, true
	}
	r, ok := a.Projects[project]
	if !ok || !Valid(r) || !a.Role.AtLeast(Viewer) {
		return "", false
	}
	// Defence in depth: a project role never exceeds the organization role.
	return ProjectRole(a.Role, &r), true
}

// Sees reports whether the actor may read a project and what belongs to it.
func (a Actor) Sees(project uuid.UUID) bool {
	_, ok := a.RoleIn(project)
	return ok
}

// CanEditIn reports whether the actor may create, change, pause and delete monitors in a project.
func (a Actor) CanEditIn(project uuid.UUID) bool {
	r, ok := a.RoleIn(project)
	return ok && r.AtLeast(Member)
}

// Scope is the set of projects whose data a list may show: every project, or the listed ones.
type Scope struct {
	All bool
	IDs []uuid.UUID
}

// Scope returns the projects the actor sees.
func (a Actor) Scope() Scope {
	if a.AllProjects() {
		return Scope{All: true}
	}
	ids := make([]uuid.UUID, 0, len(a.Projects))
	for id := range a.Projects {
		ids = append(ids, id)
	}
	return Scope{IDs: ids}
}

// Includes reports whether the scope covers a project.
func (s Scope) Includes(project uuid.UUID) bool {
	if s.All {
		return true
	}
	for _, id := range s.IDs {
		if id == project {
			return true
		}
	}
	return false
}

// Narrow limits the scope to one project (a list filter); the result is empty when the scope
// does not include it.
func (s Scope) Narrow(project uuid.UUID) Scope {
	if s.Includes(project) {
		return Scope{IDs: []uuid.UUID{project}}
	}
	return Scope{IDs: []uuid.UUID{}}
}

// Permission is what a role may do: "<resource>.<action>". Operations are checked against
// permissions, never against role names.
type Permission string

// Level is the part of the product a permission applies to.
type Level string

const (
	// LevelOrganization permissions are held through an organization role.
	LevelOrganization Level = "organization"
	// LevelProject permissions apply inside one project.
	LevelProject Level = "project"
)

// Marks for operations that no role decides: no session at all, or the platform-admin flag
// (requirePlatformAdmin in internal/api) on top of any session.
const (
	NoSession     Permission = "public"
	PlatformAdmin Permission = "platform.admin"
)

// Public is what MinRole reports for operations without a session.
const Public Role = "public"

// Definition describes one permission.
type Definition struct {
	Name        Permission
	Level       Level
	Description string
}

// Permissions lists every permission, in the order of the generated documentation.
func Permissions() []Definition { return slices.Clone(permissions) }

var permissions = []Definition{
	{"account.use", LevelOrganization, "Use the session, switch and leave organizations, link sign-in identities"},
	{"organization.delete", LevelOrganization, "Delete the organization"},
	{"organization.domains.manage", LevelOrganization, "Manage the sign-in domains of the organization"},
	{"organization.members.read", LevelOrganization, "List the members of the organization"},
	{"organization.members.manage", LevelOrganization, "Change and remove members, manage invitations"},
	{"usage.read", LevelOrganization, "Read the usage counts against the plan limits"},
	{"project.create", LevelOrganization, "Create projects"},
	{"project.read", LevelProject, "List and read projects"},
	{"project.update", LevelProject, "Change project settings"},
	{"project.delete", LevelProject, "Delete a project"},
	{"project.members.read", LevelProject, "List the members of a project"},
	{"project.members.manage", LevelProject, "Add, change and remove project members"},
	{"monitor.read", LevelProject, "Read monitors, their statistics and checks"},
	{"monitor.create", LevelProject, "Create monitors"},
	{"monitor.update", LevelProject, "Change, pause and resume monitors"},
	{"monitor.delete", LevelProject, "Delete monitors"},
	{"incident.read", LevelProject, "Read the incidents of a monitor"},
	{"heartbeat.manage", LevelProject, "Regenerate heartbeat tokens"},
	{"channel.read", LevelProject, "List and read notification channels"},
	{"channel.manage", LevelProject, "Create, change, test and delete notification channels"},
	{"statuspage.read", LevelProject, "Read the status page settings and link"},
	{"statuspage.manage", LevelProject, "Configure, protect and publish status pages"},
}

// roles is the one role table: each role is the set of permissions it holds, listed in full.
var grants = map[Role][]Permission{
	Owner: {
		"account.use", "organization.delete", "organization.domains.manage",
		"organization.members.read", "organization.members.manage", "usage.read", "project.create",
		"project.read", "project.update", "project.delete", "project.members.read", "project.members.manage",
		"monitor.read", "monitor.create", "monitor.update", "monitor.delete", "incident.read", "heartbeat.manage",
		"channel.read", "channel.manage", "statuspage.read", "statuspage.manage",
	},
	Maintainer: {
		"account.use",
		"organization.members.read", "organization.members.manage", "usage.read", "project.create",
		"project.read", "project.update", "project.delete", "project.members.read", "project.members.manage",
		"monitor.read", "monitor.create", "monitor.update", "monitor.delete", "incident.read", "heartbeat.manage",
		"channel.read", "channel.manage", "statuspage.read", "statuspage.manage",
	},
	Member: {
		"account.use",
		"organization.members.read",
		"project.read", "project.members.read",
		"monitor.read", "monitor.create", "monitor.update", "monitor.delete", "incident.read", "heartbeat.manage",
		"channel.read", "statuspage.read",
	},
	Viewer: {
		"account.use",
		"organization.members.read",
		"project.read", "project.members.read",
		"monitor.read", "incident.read",
		"channel.read", "statuspage.read",
	},
}

// Roles returns the roles, highest first.
func Roles() []Role { return []Role{Owner, Maintainer, Member, Viewer} }

// Has reports whether the role holds the permission. An unknown role holds nothing.
func Has(r Role, p Permission) bool { return slices.Contains(grants[r], p) }

// operations maps every operation id (lowercase) to the permission it needs. The platform admin
// operations carry PlatformAdmin: the flag, not a role, decides.
var operations = map[string]Permission{
	// No session.
	"gethealth":         NoSession,
	"getpublicconfig":   NoSession,
	"login":             NoSession,
	"register":          NoSession,
	"pingheartbeat":     NoSession,
	"pingheartbeatpost": NoSession,
	"lookupinvitation":  NoSession,
	// The emailed token is the credential.
	"verifyemail": NoSession,
	// Forgotten password: the emailed token is the credential.
	"requestpasswordreset": NoSession,
	"resetpassword":        NoSession,
	// OAuth sign-in: the start of a link checks the session and CSRF token itself.
	"getauthproviders": NoSession,
	"startoauth":       NoSession,
	"oauthcallback":    NoSession,

	// The session itself and the user's own organizations: any role.
	"logout":                  "account.use",
	"getme":                   "account.use",
	"resendemailverification": "account.use",
	"listorganizations":       "account.use",
	"switchorganization":      "account.use",
	"changepassword":          "account.use",
	"acceptinvitation":        "account.use",
	"leaveorganization":       "account.use",
	"listidentities":          "account.use",
	"unlinkidentity":          "account.use",

	// Reading the organization's data.
	"listmonitors":         "monitor.read",
	"getmonitor":           "monitor.read",
	"getmonitorstats":      "monitor.read",
	"listmonitorchecks":    "monitor.read",
	"listmonitorincidents": "incident.read",
	"listchannels":         "channel.read",
	"getchannel":           "channel.read",
	"listmembers":          "organization.members.read",
	// Projects: the list and reads show only the projects the actor sees.
	"listprojects":       "project.read",
	"getproject":         "project.read",
	"listprojectmembers": "project.members.read",

	// Monitors and heartbeats.
	"createmonitor":            "monitor.create",
	"updatemonitor":            "monitor.update",
	"deletemonitor":            "monitor.delete",
	"pausemonitor":             "monitor.update",
	"resumemonitor":            "monitor.update",
	"regenerateheartbeattoken": "heartbeat.manage",

	// Notification channels, members and invitations.
	"createchannel": "channel.manage",
	"updatechannel": "channel.manage",
	"deletechannel": "channel.manage",
	"testchannel":   "channel.manage",
	// Organization-wide usage counts: members and viewers learn about a reached limit
	// from the 409 of the create that hits it.
	"getusage":         "usage.read",
	"listinvitations":  "organization.members.manage",
	"createinvitation": "organization.members.manage",
	"revokeinvitation": "organization.members.manage",
	"updatemember":     "organization.members.manage",
	"removemember":     "organization.members.manage",

	// Projects and their members.
	"createproject":       "project.create",
	"updateproject":       "project.update",
	"deleteproject":       "project.delete",
	"putprojectmember":    "project.members.manage",
	"removeprojectmember": "project.members.manage",

	// The status page: every role sees its settings and link; the others configure and publish
	// it. Components and the preview name monitors of every project.
	"liststatuspages":          "statuspage.read",
	"getstatuspage":            "statuspage.read",
	"createstatuspage":         "statuspage.manage",
	"updatestatuspage":         "statuspage.manage",
	"deletestatuspage":         "statuspage.manage",
	"setstatuspagepassword":    "statuspage.manage",
	"clearstatuspagepassword":  "statuspage.manage",
	"liststatuspagecomponents": "statuspage.manage",
	"putstatuspagecomponents":  "statuspage.manage",
	"previewstatuspage":        "statuspage.manage",

	// The organization itself.
	"deleteorganization": "organization.delete",
	// Google Workspace sign-in domains.
	"listauthdomains":  "organization.domains.manage",
	"createauthdomain": "organization.domains.manage",
	"updateauthdomain": "organization.domains.manage",
	"deleteauthdomain": "organization.domains.manage",

	// Platform admin panel.
	"admingetstats":                        PlatformAdmin,
	"adminlistorganizations":               PlatformAdmin,
	"admingetorganization":                 PlatformAdmin,
	"adminupdateorganization":              PlatformAdmin,
	"admindeleteorganization":              PlatformAdmin,
	"adminlistorganizationusers":           PlatformAdmin,
	"adminresetuserpassword":               PlatformAdmin,
	"adminlistallusers":                    PlatformAdmin,
	"admingetuser":                         PlatformAdmin,
	"admindisableuser":                     PlatformAdmin,
	"adminenableuser":                      PlatformAdmin,
	"adminrevokeusersessions":              PlatformAdmin,
	"admingetemailsettings":                PlatformAdmin,
	"adminupdateemailsettings":             PlatformAdmin,
	"admindeleteemailsettings":             PlatformAdmin,
	"adminupdateemailfeatures":             PlatformAdmin,
	"adminsendtestemail":                   PlatformAdmin,
	"admingetregistration":                 PlatformAdmin,
	"adminupdateregistration":              PlatformAdmin,
	"adminlistorganizationmembers":         PlatformAdmin,
	"adminaddorganizationmember":           PlatformAdmin,
	"adminsetorganizationmember":           PlatformAdmin,
	"adminremoveorganizationmember":        PlatformAdmin,
	"adminlistorganizationinvitations":     PlatformAdmin,
	"admincreateorganizationinvitation":    PlatformAdmin,
	"adminrevokeorganizationinvitation":    PlatformAdmin,
	"adminlistorganizationprojects":        PlatformAdmin,
	"admincreateorganizationproject":       PlatformAdmin,
	"adminupdateorganizationproject":       PlatformAdmin,
	"admindeleteorganizationproject":       PlatformAdmin,
	"adminlistorganizationprojectmembers":  PlatformAdmin,
	"adminputorganizationprojectmember":    PlatformAdmin,
	"adminremoveorganizationprojectmember": PlatformAdmin,
}

// whileMustChangePassword lists the only operations a session may call while its user has to
// change the password; every other operation, present or future, is refused (deny by default).
var whileMustChangePassword = map[string]bool{
	"getme":          true,
	"logout":         true,
	"changepassword": true,
}

// AllowedWhileMustChangePassword reports whether a session of a user who has to change the
// password may call the operation.
func AllowedWhileMustChangePassword(operationID string) bool {
	return whileMustChangePassword[strings.ToLower(operationID)]
}

// PermissionOf returns the permission an operation needs (NoSession or PlatformAdmin for the
// operations no role decides) and whether the operation is in the table. Operation ids are
// compared case-insensitively: the generated server passes them as Go names.
func PermissionOf(operationID string) (Permission, bool) {
	p, ok := operations[strings.ToLower(operationID)]
	return p, ok
}

// MinRole returns the lowest role that holds the permission an operation needs, for messages.
// Public operations report Public; platform admin operations report Viewer because the flag,
// not a role, decides.
func MinRole(operationID string) (Role, bool) {
	p, ok := PermissionOf(operationID)
	if !ok {
		return "", false
	}
	switch p {
	case NoSession:
		return Public, true
	case PlatformAdmin:
		return Viewer, true
	}
	for i := len(Roles()) - 1; i >= 0; i-- {
		if Has(Roles()[i], p) {
			return Roles()[i], true
		}
	}
	return "", false
}

// Operations returns every operation id in the table (lowercase).
func Operations() []string {
	out := make([]string, 0, len(operations))
	for op := range operations {
		out = append(out, op)
	}
	return out
}

// Can reports whether the actor may call the operation. Unknown operations and unknown roles
// are denied; Public operations are allowed to anyone.
func Can(a Actor, operationID string) bool {
	p, ok := PermissionOf(operationID)
	if !ok {
		return false
	}
	switch p {
	case NoSession:
		return true
	case PlatformAdmin:
		return Valid(a.Role)
	}
	return Has(a.Role, p)
}

// CanManage reports whether the actor may change or remove a member whose role is target, and
// (for a role change) give it the role to (pass "" for a removal). Only maintainers and owners manage
// members; a maintainer never touches an owner and never grants owner: nobody grants a
// role above their own. Guard rails that need the database (the last owner, the platform admin)
// are checked by the caller.
func CanManage(a Actor, target, to Role) bool {
	if !a.Role.AtLeast(Maintainer) || !Valid(target) {
		return false
	}
	if rank(target) > rank(a.Role) {
		return false
	}
	if to != "" && (!Valid(to) || rank(to) > rank(a.Role)) {
		return false
	}
	return true
}

// CanInvite reports whether the actor may create an invitation with the role: maintainers and owners,
// for maintainer, member or viewer (owner is granted by a role change, never by a link).
func CanInvite(a Actor, role Role) bool {
	return a.Role.AtLeast(Maintainer) && role != Owner && Valid(role) && rank(role) <= rank(a.Role)
}
