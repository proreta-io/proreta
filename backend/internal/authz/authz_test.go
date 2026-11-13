package authz

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

var roles = []Role{Owner, Maintainer, Member, Viewer}

func TestCanDeniesUnknown(t *testing.T) {
	for _, r := range roles {
		if Can(Actor{Role: r}, "noSuchOperation") {
			t.Errorf("%s may call an unknown operation", r)
		}
	}
	if Can(Actor{Role: "superuser"}, "listMonitors") {
		t.Error("unknown role may read monitors")
	}
	if Can(Actor{}, "getMe") {
		t.Error("empty role may call getMe")
	}
	if !Can(Actor{}, "getHealth") {
		t.Error("public operation refused")
	}
}

// expectedMinimum is written by hand, independently of the tables in authz.go: the lowest role
// that may call each operation.
var expectedMinimum = map[Role][]string{
	Viewer: {
		"logout", "getMe", "getPermissions", "resendEmailVerification", "listOrganizations", "switchOrganization",
		"changePassword", "acceptInvitation", "leaveOrganization", "listIdentities", "unlinkIdentity",
		"listMonitors", "getMonitor", "getMonitorStats", "listMonitorChecks", "listMonitorIncidents",
		"listChannels", "getChannel", "listMembers", "listProjects", "getProject", "listProjectMembers",
		"listStatusPages", "getStatusPage", "listStatusPageDomains", "listSpecula", "getSpecula",
	},
	Member: {
		"createMonitor", "updateMonitor", "deleteMonitor", "pauseMonitor", "resumeMonitor",
		"regenerateHeartbeatToken",
	},
	Maintainer: {
		"createChannel", "updateChannel", "deleteChannel", "testChannel", "previewChannelTemplate", "getUsage",
		"listInvitations", "createInvitation", "revokeInvitation", "updateMember", "removeMember",
		"createProject", "updateProject", "putProjectMember", "removeProjectMember",
		"createStatusPage", "updateStatusPage", "deleteStatusPage", "addStatusPageDomain", "verifyStatusPageDomain", "setPrimaryStatusPageDomain", "removeStatusPageDomain", "setStatusPagePassword",
		"clearStatusPagePassword", "listStatusPageComponents", "putStatusPageComponents", "previewStatusPage",
		"createSpecula", "updateSpecula", "revokeSpecula",
	},
	Owner: {
		"deleteProject", "deleteOrganization", "listAuthDomains", "createAuthDomain", "updateAuthDomain", "deleteAuthDomain",
	},
}

var expectedPublic = []string{
	"getHealth", "getPublicConfig", "login", "register", "pingHeartbeat", "pingHeartbeatPost",
	"lookupInvitation", "verifyEmail", "requestPasswordReset", "resetPassword", "getAuthProviders",
	"startOAuth", "oauthCallback",
}

func TestCanMatrix(t *testing.T) {
	seen := map[string]bool{}
	for min, ops := range expectedMinimum {
		for _, op := range ops {
			seen[strings.ToLower(op)] = true
			for _, r := range roles {
				want := rank(r) >= rank(min)
				if got := Can(Actor{Role: r}, op); got != want {
					t.Errorf("Can(%s, %s) = %v, want %v", r, op, got, want)
				}
			}
			if got, _ := MinRole(op); got != min {
				t.Errorf("MinRole(%s) = %s, want %s", op, got, min)
			}
		}
	}
	for _, op := range expectedPublic {
		seen[strings.ToLower(op)] = true
		if !Can(Actor{}, op) {
			t.Errorf("public operation %s refused", op)
		}
	}
	// The platform admin operations are decided by the flag: any role passes the table.
	for _, op := range Operations() {
		if seen[op] {
			continue
		}
		if !strings.HasPrefix(op, "admin") {
			t.Errorf("%s is in the table but not in the expected matrix", op)
			continue
		}
		for _, r := range roles {
			if !Can(Actor{Role: r}, op) {
				t.Errorf("Can(%s, %s) = false for a platform admin operation", r, op)
			}
		}
		if Can(Actor{}, op) {
			t.Errorf("%s allowed without a role", op)
		}
	}
	for _, min := range expectedMinimum {
		for _, op := range min {
			if _, ok := PermissionOf(op); !ok {
				t.Errorf("expected operation %s is not in the table", op)
			}
		}
	}
}

func TestEveryPermissionIsDefinedAndHeld(t *testing.T) {
	defined := map[Permission]bool{}
	for _, d := range Permissions() {
		if defined[d.Name] {
			t.Errorf("permission %s defined twice", d.Name)
		}
		defined[d.Name] = true
		if d.Level != LevelOrganization && d.Level != LevelProject && d.Level != LevelAccount {
			t.Errorf("permission %s has level %q", d.Name, d.Level)
		}
		held := false
		for _, r := range roles {
			held = held || Has(r, d.Name)
		}
		if !held {
			t.Errorf("permission %s belongs to no role", d.Name)
		}
	}
	for r, list := range grants {
		for _, p := range list {
			if !defined[p] {
				t.Errorf("role %s holds undefined permission %s", r, p)
			}
		}
	}
	for op, p := range operations {
		if p != NoSession && p != PlatformAdmin && !defined[p] {
			t.Errorf("operation %s needs undefined permission %s", op, p)
		}
	}
	// A role never holds less than the role below it.
	for i := 0; i+1 < len(roles); i++ {
		for _, p := range grants[roles[i+1]] {
			if !Has(roles[i], p) {
				t.Errorf("%s lacks %s held by %s", roles[i], p, roles[i+1])
			}
		}
	}
}

func TestCanManage(t *testing.T) {
	cases := []struct {
		actor, target, to Role
		want              bool
	}{
		{Owner, Owner, Maintainer, true},
		{Owner, Viewer, Owner, true},
		{Owner, Maintainer, "", true},
		{Owner, "", Member, true},              // give a project-only person an organization role
		{Owner, Member, "", true},              // make a member project-only
		{Maintainer, Owner, Maintainer, false}, // maintainers cannot touch owners
		{Maintainer, Owner, "", false},         // nor remove them
		{Maintainer, Member, Owner, false},     // nor grant owner
		{Maintainer, Member, Maintainer, true},
		{Maintainer, Maintainer, Viewer, true},
		{Maintainer, Viewer, "", true},
		{Maintainer, "", Viewer, true},
		{Maintainer, "", Owner, false},
		{Member, Viewer, Member, false},
		{Viewer, Viewer, "", false},
		{"", Viewer, "", false}, // no organization role manages nobody
		{Owner, Member, "root", false},
		{Owner, "root", Member, false},
	}
	for _, c := range cases {
		if got := CanManage(Actor{Role: c.actor}, c.target, c.to); got != c.want {
			t.Errorf("CanManage(%q, %q -> %q) = %v, want %v", c.actor, c.target, c.to, got, c.want)
		}
	}
	// A direct project role does not help: member management is an organization permission.
	p := uuid.New()
	if CanManage(Actor{Projects: map[uuid.UUID]Role{p: Owner}}, Viewer, "") {
		t.Error("a project owner manages organization members")
	}
}

func TestCanInvite(t *testing.T) {
	cases := []struct {
		actor, role Role
		want        bool
	}{
		{Owner, Maintainer, true},
		{Owner, Owner, false},
		{Maintainer, Maintainer, true},
		{Maintainer, Viewer, true},
		{Member, Viewer, false},
		{Viewer, Viewer, false},
		{Owner, "root", false},
	}
	for _, c := range cases {
		if got := CanInvite(Actor{Role: c.actor}, c.role); got != c.want {
			t.Errorf("CanInvite(%s, %s) = %v, want %v", c.actor, c.role, got, c.want)
		}
	}
}

func TestRoleAtLeast(t *testing.T) {
	if !Owner.AtLeast(Viewer) || Viewer.AtLeast(Member) || Role("").AtLeast(Viewer) {
		t.Fatal("AtLeast ordering")
	}
}

func TestHigher(t *testing.T) {
	cases := []struct{ a, b, want Role }{
		{Viewer, Maintainer, Maintainer},
		{Maintainer, Viewer, Maintainer},
		{Owner, Owner, Owner},
		{"", Viewer, Viewer},
		{Member, "", Member},
		{"", "", ""},
		{"root", "", ""},
		{"root", Viewer, Viewer},
	}
	for _, c := range cases {
		if got := Higher(c.a, c.b); got != c.want {
			t.Errorf("Higher(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// project permissions written by hand: which role may do what inside a project, independent of
// the tables in authz.go.
var expectedInProject = map[Permission][]Role{
	"project.read":           {Owner, Maintainer, Member, Viewer},
	"project.members.read":   {Owner, Maintainer, Member, Viewer},
	"specula.read":           {Owner, Maintainer, Member, Viewer},
	"monitor.read":           {Owner, Maintainer, Member, Viewer},
	"incident.read":          {Owner, Maintainer, Member, Viewer},
	"channel.read":           {Owner, Maintainer, Member, Viewer},
	"monitor.create":         {Owner, Maintainer, Member},
	"monitor.update":         {Owner, Maintainer, Member},
	"monitor.delete":         {Owner, Maintainer, Member},
	"heartbeat.manage":       {Owner, Maintainer, Member},
	"project.update":         {Owner, Maintainer},
	"project.members.manage": {Owner, Maintainer},
	"channel.manage":         {Owner, Maintainer},
	"statuspage.read":        {Owner, Maintainer, Member, Viewer},
	"statuspage.manage":      {Owner, Maintainer},
	"project.delete":         {Owner},
}

// organization permissions written by hand.
var expectedInOrganization = map[Permission][]Role{
	"organization.members.read":       {Owner, Maintainer, Member, Viewer},
	"organization.statuspages.read":   {Owner, Maintainer, Member, Viewer},
	"usage.read":                      {Owner, Maintainer},
	"project.create":                  {Owner, Maintainer},
	"organization.members.manage":     {Owner, Maintainer},
	"organization.statuspages.manage": {Owner, Maintainer},
	"specula.manage":                  {Owner, Maintainer},
	"organization.delete":             {Owner},
	"organization.domains.manage":     {Owner},
}

func contains(list []Role, r Role) bool {
	for _, x := range list {
		if x == r {
			return true
		}
	}
	return false
}

func TestRoleMatrixByHand(t *testing.T) {
	covered := map[Permission]bool{"account.use": true}
	for p, who := range expectedInProject {
		covered[p] = true
		for _, r := range roles {
			if got := Has(r, p); got != contains(who, r) {
				t.Errorf("Has(%s, %s) = %v", r, p, got)
			}
		}
		if levelOf(p) != LevelProject {
			t.Errorf("%s should be a project permission", p)
		}
	}
	for p, who := range expectedInOrganization {
		covered[p] = true
		for _, r := range roles {
			if got := Has(r, p); got != contains(who, r) {
				t.Errorf("Has(%s, %s) = %v", r, p, got)
			}
		}
		if levelOf(p) != LevelOrganization {
			t.Errorf("%s should be an organization permission", p)
		}
	}
	for _, d := range Permissions() {
		if !covered[d.Name] {
			t.Errorf("permission %s is not in the hand-written matrix", d.Name)
		}
	}
	if levelOf("account.use") != LevelAccount || levelOf("nope") != "" {
		t.Error("levels of account.use or an unknown permission")
	}
}

func TestEffectiveRole(t *testing.T) {
	p1, p2 := uuid.New(), uuid.New()
	cases := []struct {
		name    string
		a       Actor
		project uuid.UUID
		want    Role
		ok      bool
	}{
		{"organization role only", Actor{Role: Member}, p1, Member, true},
		{"organization viewer sees every project", Actor{Role: Viewer}, p2, Viewer, true},
		{"direct role raises a viewer", Actor{Role: Viewer, Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, Maintainer, true},
		{"direct role does not reach other projects", Actor{Role: Viewer, Projects: map[uuid.UUID]Role{p1: Maintainer}}, p2, Viewer, true},
		{"direct role never lowers", Actor{Role: Maintainer, Projects: map[uuid.UUID]Role{p1: Viewer}}, p1, Maintainer, true},
		{"project-only", Actor{Projects: map[uuid.UUID]Role{p1: Member}}, p1, Member, true},
		{"project-only elsewhere", Actor{Projects: map[uuid.UUID]Role{p1: Member}}, p2, "", false},
		{"nothing at all", Actor{UserID: uuid.New()}, p1, "", false},
		{"unknown organization role", Actor{Role: "root"}, p1, "", false},
		{"unknown direct role", Actor{Projects: map[uuid.UUID]Role{p1: "root"}}, p1, "", false},
	}
	for _, c := range cases {
		got, ok := c.a.RoleIn(c.project)
		if got != c.want || ok != c.ok || c.a.Sees(c.project) != c.ok {
			t.Errorf("%s: RoleIn = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCanInProject(t *testing.T) {
	p1, p2 := uuid.New(), uuid.New()
	// organization viewer, project maintainer of p1
	a := Actor{Role: Viewer, Projects: map[uuid.UUID]Role{p1: Maintainer}}
	for _, c := range []struct {
		project uuid.UUID
		perm    Permission
		want    bool
	}{
		{p1, "channel.manage", true},
		{p1, "project.members.manage", true},
		{p1, "monitor.update", true},
		{p1, "project.delete", false}, // maintainers never delete projects
		{p2, "channel.manage", false},
		{p2, "monitor.update", false},
		{p2, "monitor.read", true},
		{p1, "organization.delete", false}, // organization scope is never held through a project
		{p1, "project.create", false},
		{p1, "nope", false},
	} {
		if got := a.CanIn(c.project, c.perm); got != c.want {
			t.Errorf("CanIn(%v, %s) = %v, want %v", c.project == p1, c.perm, got, c.want)
		}
	}
	// A project owner holds no organization permission, however high the project role.
	owner := Actor{Projects: map[uuid.UUID]Role{p1: Owner}}
	for _, p := range []Permission{"organization.delete", "organization.members.manage", "project.create", "usage.read",
		"organization.domains.manage", "organization.statuspages.manage", "organization.statuspages.read", "organization.members.read"} {
		if owner.CanIn(p1, p) || owner.Holds(p) || owner.HoldsAnywhere(p) {
			t.Errorf("a project owner holds %s", p)
		}
	}
	if !owner.CanIn(p1, "project.delete") || owner.CanIn(p2, "project.delete") {
		t.Error("project owner deletes its own project only")
	}
	// An organization maintainer holds project permissions everywhere but cannot delete.
	m := Actor{Role: Maintainer}
	if !m.CanIn(p2, "channel.manage") || m.CanIn(p2, "project.delete") || m.Holds("organization.delete") {
		t.Error("organization maintainer rights")
	}
	// An organization maintainer who is also a project owner can delete that project.
	mo := Actor{Role: Maintainer, Projects: map[uuid.UUID]Role{p1: Owner}}
	if !mo.CanIn(p1, "project.delete") || mo.CanIn(p2, "project.delete") {
		t.Error("raising by a direct owner role")
	}
}

func TestProjectOnlyReachesOperations(t *testing.T) {
	p1 := uuid.New()
	for _, r := range roles {
		a := Actor{UserID: uuid.New(), Projects: map[uuid.UUID]Role{p1: r}}
		for _, op := range []string{"getMe", "listOrganizations", "switchOrganization", "leaveOrganization", "logout"} {
			if !Can(a, op) {
				t.Errorf("project-only %s cannot call %s", r, op)
			}
		}
		// Operations of the organization level stay closed, whatever the project role.
		for _, op := range []string{"updateMember", "removeMember",
			"createProject", "getUsage", "deleteOrganization", "listAuthDomains"} {
			if Can(a, op) {
				t.Errorf("project-only %s can call %s", r, op)
			}
		}
		// Project operations pass the table exactly when the role holds the permission.
		for op, p := range map[string]Permission{"listMonitors": "monitor.read", "createMonitor": "monitor.create",
			"createChannel": "channel.manage", "previewChannelTemplate": "channel.manage", "putProjectMember": "project.members.manage", "deleteProject": "project.delete",
			"updateProject": "project.update", "getProject": "project.read",
			// People: the list is narrowed to the person's projects by the service, invitations
			// into a project follow the project role (organization ones are refused by the service).
			"listMembers": "project.members.read", "listInvitations": "project.members.manage",
			"createInvitation": "project.members.manage", "revokeInvitation": "project.members.manage",
			"listStatusPages": "statuspage.read", "createStatusPage": "statuspage.manage"} {
			if got := Can(a, op); got != Has(r, p) {
				t.Errorf("project-only %s: Can(%s) = %v", r, op, got)
			}
		}
	}
	// Someone with no role anywhere still holds the account permission (switch, leave).
	if !Can(Actor{UserID: uuid.New()}, "leaveOrganization") || Can(Actor{UserID: uuid.New()}, "listMonitors") {
		t.Error("an actor without any role")
	}
}

func TestScopes(t *testing.T) {
	p1, p2, p3 := uuid.New(), uuid.New(), uuid.New()
	for _, r := range roles {
		a := Actor{Role: r}
		if sc := a.Scope(); !sc.All || !sc.Includes(p3) {
			t.Errorf("organization %s does not read every project", r)
		}
	}
	po := Actor{Projects: map[uuid.UUID]Role{p1: Member, p2: Viewer}}
	sc := po.Scope()
	if sc.All || !sc.Includes(p1) || !sc.Includes(p2) || sc.Includes(p3) {
		t.Errorf("project-only scope %+v", sc)
	}
	if w := po.ScopeFor("channel.manage"); w.All || len(w.IDs) != 0 {
		t.Errorf("scope for channel.manage of member and viewer = %+v", w)
	}
	if w := po.ScopeFor("monitor.update"); w.All || len(w.IDs) != 1 || !w.Includes(p1) {
		t.Errorf("scope for monitor.update = %+v", w)
	}
	if w := (Actor{Role: Viewer}).ScopeFor("monitor.update"); w.All || len(w.IDs) != 0 {
		t.Errorf("organization viewer edits nowhere: %+v", w)
	}
	if w := (Actor{Role: Owner}).ScopeFor("organization.delete"); w.All || len(w.IDs) != 0 {
		t.Errorf("an organization permission has no project scope: %+v", w)
	}
	if n := sc.Narrow(p3); n.All || len(n.IDs) != 0 {
		t.Errorf("narrow to an unseen project = %+v", n)
	}
	if n := (Scope{All: true}).Narrow(p3); n.All || len(n.IDs) != 1 {
		t.Errorf("narrow all = %+v", n)
	}
}

func TestCanManageIn(t *testing.T) {
	p1, p2 := uuid.New(), uuid.New()
	cases := []struct {
		name       string
		actor      Actor
		project    uuid.UUID
		target, to Role
		want       bool
	}{
		{"project owner grants owner", Actor{Projects: map[uuid.UUID]Role{p1: Owner}}, p1, Member, Owner, true},
		{"project owner changes an owner", Actor{Projects: map[uuid.UUID]Role{p1: Owner}}, p1, Owner, Viewer, true},
		{"project maintainer grants maintainer", Actor{Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, "", Maintainer, true},
		{"project maintainer cannot grant owner", Actor{Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, Member, Owner, false},
		{"project maintainer cannot touch an owner", Actor{Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, Owner, Member, false},
		{"project maintainer cannot remove an owner", Actor{Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, Owner, "", false},
		{"project maintainer removes a member", Actor{Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, Member, "", true},
		{"other project", Actor{Projects: map[uuid.UUID]Role{p1: Maintainer}}, p2, Member, "", false},
		{"project member manages nobody", Actor{Projects: map[uuid.UUID]Role{p1: Member}}, p1, Viewer, "", false},
		{"organization maintainer everywhere", Actor{Role: Maintainer}, p2, Member, Maintainer, true},
		{"organization maintainer cannot grant owner", Actor{Role: Maintainer}, p2, Member, Owner, false},
		{"organization maintainer cannot touch a project owner", Actor{Role: Maintainer}, p2, Owner, Member, false},
		{"organization owner anything", Actor{Role: Owner}, p2, Owner, Viewer, true},
		{"organization viewer raised to project maintainer", Actor{Role: Viewer, Projects: map[uuid.UUID]Role{p1: Maintainer}}, p1, Member, Viewer, true},
		{"unknown target role", Actor{Role: Owner}, p1, "root", Member, false},
		{"unknown new role", Actor{Role: Owner}, p1, Member, "root", false},
	}
	for _, c := range cases {
		if got := CanManageIn(c.actor, c.project, c.target, c.to); got != c.want {
			t.Errorf("%s: CanManageIn = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHeldAndRolesWith(t *testing.T) {
	if got := (Actor{UserID: uuid.New()}).Held(); len(got) != 1 || got[0] != "account.use" {
		t.Errorf("person without a role holds %v, want account.use only", got)
	}
	if got := (Actor{}).Held(); len(got) != 0 {
		t.Errorf("nobody holds %v", got)
	}
	if got := (Actor{UserID: uuid.New(), Role: Member}).Held(); len(got) != 14 {
		t.Errorf("member holds %v, want 14 permissions", got)
	}
	if got := ProjectPermissions("nobody"); len(got) != 0 {
		t.Errorf("unknown role holds %v in a project", got)
	}
	if got := RolesWith("project.delete"); len(got) != 1 || got[0] != Owner {
		t.Errorf("project.delete roles = %v", got)
	}
	if got := RolesWith("monitor.read"); len(got) != 4 || got[0] != Owner || got[3] != Viewer {
		t.Errorf("monitor.read roles = %v", got)
	}
}
