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
		"logout", "getMe", "resendEmailVerification", "listOrganizations", "switchOrganization",
		"changePassword", "acceptInvitation", "leaveOrganization", "listIdentities", "unlinkIdentity",
		"listMonitors", "getMonitor", "getMonitorStats", "listMonitorChecks", "listMonitorIncidents",
		"listChannels", "getChannel", "listMembers", "listProjects", "getProject", "listProjectMembers",
		"listStatusPages", "getStatusPage",
	},
	Member: {
		"createMonitor", "updateMonitor", "deleteMonitor", "pauseMonitor", "resumeMonitor",
		"regenerateHeartbeatToken",
	},
	Maintainer: {
		"createChannel", "updateChannel", "deleteChannel", "testChannel", "getUsage",
		"listInvitations", "createInvitation", "revokeInvitation", "updateMember", "removeMember",
		"createProject", "updateProject", "deleteProject", "putProjectMember", "removeProjectMember",
		"createStatusPage", "updateStatusPage", "deleteStatusPage", "setStatusPagePassword",
		"clearStatusPagePassword", "listStatusPageComponents", "putStatusPageComponents", "previewStatusPage",
	},
	Owner: {
		"deleteOrganization", "listAuthDomains", "createAuthDomain", "updateAuthDomain", "deleteAuthDomain",
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
		if d.Level != LevelOrganization && d.Level != LevelProject {
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
		{Maintainer, Owner, Maintainer, false}, // maintainers cannot touch owners
		{Maintainer, Owner, "", false},         // nor remove them
		{Maintainer, Member, Owner, false},     // nor grant owner
		{Maintainer, Member, Maintainer, true},
		{Maintainer, Maintainer, Viewer, true},
		{Maintainer, Viewer, "", true},
		{Member, Viewer, Member, false},
		{Viewer, Viewer, "", false},
		{Owner, Member, "root", false},
		{Owner, "root", Member, false},
	}
	for _, c := range cases {
		if got := CanManage(Actor{Role: c.actor}, c.target, c.to); got != c.want {
			t.Errorf("CanManage(%s, %s -> %q) = %v, want %v", c.actor, c.target, c.to, got, c.want)
		}
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

func TestProjectAccess(t *testing.T) {
	p1, p2, p3 := uuid.New(), uuid.New(), uuid.New()
	viewer := Viewer
	member := Member
	if got := ProjectRole(Member, &viewer); got != Viewer {
		t.Errorf("member with viewer override = %s", got)
	}
	if got := ProjectRole(Viewer, &member); got != Viewer {
		t.Errorf("an override raised a viewer to %s", got)
	}
	if got := ProjectRole(Member, nil); got != Member {
		t.Errorf("no override = %s", got)
	}

	m := Actor{Role: Member, Projects: map[uuid.UUID]Role{p1: Member, p2: Viewer}}
	for _, c := range []struct {
		project    uuid.UUID
		sees, edit bool
	}{{p1, true, true}, {p2, true, false}, {p3, false, false}} {
		if m.Sees(c.project) != c.sees || m.CanEditIn(c.project) != c.edit {
			t.Errorf("member in %v: sees=%v edit=%v", c.project, m.Sees(c.project), m.CanEditIn(c.project))
		}
	}
	// An entry above the organization role is capped.
	v := Actor{Role: Viewer, Projects: map[uuid.UUID]Role{p1: Owner}}
	if r, ok := v.RoleIn(p1); !ok || r != Viewer || v.CanEditIn(p1) {
		t.Errorf("viewer capped: %s %v", r, ok)
	}
	if (Actor{Role: "nobody", Projects: map[uuid.UUID]Role{p1: Member}}).Sees(p1) {
		t.Error("unknown role sees a project")
	}
	for _, r := range []Role{Owner, Maintainer} {
		a := Actor{Role: r}
		if !a.Sees(p3) || !a.CanEditIn(p3) || !a.Scope().All {
			t.Errorf("%s does not see every project", r)
		}
	}
	sc := m.Scope()
	if sc.All || !sc.Includes(p1) || !sc.Includes(p2) || sc.Includes(p3) {
		t.Errorf("member scope %+v", sc)
	}
	if n := sc.Narrow(p3); n.All || len(n.IDs) != 0 {
		t.Errorf("narrow to an unseen project = %+v", n)
	}
	if n := (Scope{All: true}).Narrow(p3); n.All || len(n.IDs) != 1 {
		t.Errorf("narrow all = %+v", n)
	}
}
