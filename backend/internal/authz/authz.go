// Package authz is the one place that decides what a role may do. It is pure: the
// api resolves the actor (user, organization role and direct project roles) per request and asks
// this package; services ask it again for decisions that depend on the target (a project, a
// person). The same four roles exist on the organization and on a project; the role in a project is
// the higher of the two, and organization-scope permissions are held only through an organization role.
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

// Actor is the user of a request: the role of its membership in the session's organization (empty
// when the person belongs to the organization only through projects) and the direct roles it
// holds in projects.
type Actor struct {
	UserID uuid.UUID
	// Role is the organization role; it applies to every project of the organization.
	Role Role
	// Projects are the direct roles in projects, not yet combined with Role.
	Projects map[uuid.UUID]Role
}

// ErrProjectForbidden means the actor sees the project but its role there does not allow the
// change (a member or viewer where a maintainer is needed, a viewer editing a monitor).
var ErrProjectForbidden = errors.New("authz: forbidden in this project")

// Higher returns the higher of two roles; an invalid role ranks below every valid one.
func Higher(a, b Role) Role {
	if rank(b) > rank(a) {
		return b
	}
	if rank(a) == 0 {
		return ""
	}
	return a
}

// Belongs reports whether the actor is a person of the organization at all, with or without an
// organization role.
func (a Actor) Belongs() bool { return Valid(a.Role) || a.UserID != uuid.Nil || len(a.Projects) > 0 }

// AllProjects reports whether the actor holds an organization role, which covers every project.
func (a Actor) AllProjects() bool { return Valid(a.Role) }

// RoleIn returns the actor's effective role in a project (the higher of the organization role and
// the direct project role) and whether it has any role there. An organization role covers any
// project id, existing or not: callers resolve the project itself.
func (a Actor) RoleIn(project uuid.UUID) (Role, bool) {
	r := Higher(a.Role, a.Projects[project])
	return r, Valid(r)
}

// Sees reports whether the actor may read a project and what belongs to it.
func (a Actor) Sees(project uuid.UUID) bool {
	_, ok := a.RoleIn(project)
	return ok
}

// CanIn reports whether the actor holds a project-scope permission in one project. An
// organization-scope permission is never held through a project role, so it is false here.
func (a Actor) CanIn(project uuid.UUID, p Permission) bool {
	if levelOf(p) != LevelProject {
		return false
	}
	r, ok := a.RoleIn(project)
	return ok && Has(r, p)
}

// CanEditIn reports whether the actor may create, change, pause and delete monitors in a project.
func (a Actor) CanEditIn(project uuid.UUID) bool { return a.CanIn(project, "monitor.update") }

// Holds reports whether the actor holds the permission through its organization role. For a
// project-scope permission that means in every project.
func (a Actor) Holds(p Permission) bool { return Has(a.Role, p) }

// HoldsAnywhere reports whether the actor may reach an operation that needs p: an organization-
// scope permission only through the organization role, a project-scope one through the
// organization role or in at least one project; the service then checks the concrete project.
func (a Actor) HoldsAnywhere(p Permission) bool {
	switch levelOf(p) {
	case LevelAccount:
		return a.Belongs()
	case LevelOrganization:
		return a.Holds(p)
	case LevelProject:
		if a.Holds(p) {
			return true
		}
		for _, r := range a.Projects {
			if Has(r, p) {
				return true
			}
		}
	}
	return false
}

// Scope is the set of projects whose data a list may show: every project, or the listed ones.
type Scope struct {
	All bool
	IDs []uuid.UUID
}

// Scope returns the projects the actor may read.
func (a Actor) Scope() Scope { return a.ScopeFor("project.read") }

// ScopeFor returns the projects in which the actor holds a project-scope permission.
func (a Actor) ScopeFor(p Permission) Scope {
	if levelOf(p) != LevelProject {
		return Scope{IDs: []uuid.UUID{}}
	}
	if a.Holds(p) {
		return Scope{All: true}
	}
	ids := make([]uuid.UUID, 0, len(a.Projects))
	for id, r := range a.Projects {
		if Has(r, p) {
			ids = append(ids, id)
		}
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
	// LevelAccount permissions concern the person's own account and are held by everybody who
	// belongs to the organization, with or without an organization role.
	LevelAccount Level = "account"
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
	{"account.use", LevelAccount, "Use the session, switch and leave organizations, link sign-in identities"},
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
	{"statuspage.read", LevelOrganization, "Read the status page settings and link"},
	{"statuspage.manage", LevelOrganization, "Configure, protect and publish status pages"},
}

// grants is the one role table: each role is the set of permissions it holds, listed in full.
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
		"project.read", "project.update", "project.members.read", "project.members.manage",
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
	"adminsetplatformadmin":                PlatformAdmin,
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

// LevelOfOperation returns the level of the permission an operation needs; operations that no
// role decides (public, platform admin) and unknown ones have none.
func LevelOfOperation(operationID string) Level {
	p, ok := PermissionOf(operationID)
	if !ok {
		return ""
	}
	return levelOf(p)
}

// levelOf returns the level of a permission; an undefined permission has none.
func levelOf(p Permission) Level {
	for _, d := range permissions {
		if d.Name == p {
			return d.Level
		}
	}
	return ""
}

// Can reports whether the actor may call the operation at all: it holds the permission through its
// organization role or, for a project-scope permission, in at least one project. Operations that
// name a project are checked again for that project by the service (CanIn). Unknown operations
// and unknown roles are denied; Public operations are allowed to anyone.
func Can(a Actor, operationID string) bool {
	p, ok := PermissionOf(operationID)
	if !ok {
		return false
	}
	switch p {
	case NoSession:
		return true
	case PlatformAdmin:
		return a.Belongs()
	}
	return a.HoldsAnywhere(p)
}

// CanManage reports whether the actor may change or remove an organization member whose role is
// target ("" = no organization role), and (for a role change) give it the role to (pass "" for a
// removal or for taking the organization role away). Only maintainers and owners manage members;
// a maintainer never touches an owner and never grants owner: nobody grants a role above their
// own. Guard rails that need the database (the last owner, the platform admin) are checked by
// the caller.
func CanManage(a Actor, target, to Role) bool {
	if !a.Role.AtLeast(Maintainer) || (target != "" && !Valid(target)) {
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

// CanManageIn reports whether the actor may change or remove a person in one project: it needs
// project.members.manage there, never touches a person whose effective role in the project is
// above its own (target, "" for a person not yet in the project) and never grants a role above
// its own (to, "" for a removal). So only an owner of the project, or an organization owner,
// handles project owners. No last-owner rule exists at this level: organization owners always
// control every project.
func CanManageIn(a Actor, project uuid.UUID, target, to Role) bool {
	if !a.CanIn(project, "project.members.manage") {
		return false
	}
	mine, _ := a.RoleIn(project)
	if target != "" && !Valid(target) {
		return false
	}
	if rank(target) > rank(mine) {
		return false
	}
	return to == "" || (Valid(to) && rank(to) <= rank(mine))
}

// CanInvite reports whether the actor may create an invitation with the role: maintainers and owners,
// for maintainer, member or viewer (owner is granted by a role change, never by a link).
func CanInvite(a Actor, role Role) bool {
	return a.Role.AtLeast(Maintainer) && role != Owner && Valid(role) && rank(role) <= rank(a.Role)
}
