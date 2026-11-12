package api_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/authz"
)

// roleActor is a person of the project world with the effective role written down by hand for
// each project (absent = no role there). org is the organization role, "" for none.
type roleActor struct {
	name string
	org  authz.Role
	eff  map[string]authz.Role
}

// top is the highest role the person holds anywhere.
func (a roleActor) top() authz.Role {
	var best authz.Role
	for _, r := range a.eff {
		best = authz.Higher(best, r)
	}
	return authz.Higher(best, a.org)
}

func everywhere(r authz.Role) map[string]authz.Role {
	return map[string]authz.Role{"def": r, "alpha": r, "beta": r, "gamma": r}
}

// roleActors lists everybody of the project world. The effective roles are written out and not
// computed, so a bug in the inheritance code cannot hide in the test.
var roleActors = []roleActor{
	{"owner", authz.Owner, everywhere(authz.Owner)},
	{"maintainer", authz.Maintainer, everywhere(authz.Maintainer)},
	{"member", authz.Member, everywhere(authz.Member)},
	{"viewer", authz.Viewer, everywhere(authz.Viewer)},
	{"aOwner", "", map[string]authz.Role{"alpha": authz.Owner}},
	{"aMaint", "", map[string]authz.Role{"alpha": authz.Maintainer}},
	{"aMember", "", map[string]authz.Role{"alpha": authz.Member}},
	{"aViewer", "", map[string]authz.Role{"alpha": authz.Viewer}},
	{"bMember", "", map[string]authz.Role{"beta": authz.Member}},
	{"pmix", "", map[string]authz.Role{"alpha": authz.Viewer, "beta": authz.Member}},
	{"raised", authz.Viewer, map[string]authz.Role{"def": authz.Viewer, "alpha": authz.Maintainer, "beta": authz.Viewer, "gamma": authz.Viewer}},
	{"mixOwner", authz.Member, map[string]authz.Role{"def": authz.Member, "alpha": authz.Member, "beta": authz.Owner, "gamma": authz.Member}},
	{"none", "", nil},
}

// roleProject is the set of ids of one project of the world; the unknown project has none that exist.
type roleProject struct{ key, id, mon, hb, ch string }

type roleCall struct{ method, path, body string }

// roleOp is one operation on the data of a project. min is the lowest role that may do it (written
// by hand), invisible the status for a person with no role in the project and ok the status of a
// success. prep prepares what the call needs and returns a cleanup.
type roleOp struct {
	name      string
	min       authz.Role
	invisible int
	ok        int
	call      func(w *projectWorld, p roleProject) (roleCall, func(response))
}

func noCleanup(response) {}

// nameOr keeps a request valid for ids that do not exist.
func nameOr(name string) string {
	if name == "" {
		return "x"
	}
	return name
}

func roleOps() []roleOp {
	get := func(name, path string, min authz.Role) roleOp {
		return roleOp{name, min, http.StatusNotFound, http.StatusOK, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodGet, strings.NewReplacer("{p}", p.id, "{m}", p.mon, "{c}", p.ch).Replace(path), ""}, noCleanup
		}}
	}
	// Changes that keep every value: the call has to be refused or accepted, never to change the world.
	monitorWrite := func(name, method, path, body string, ok int) roleOp {
		return roleOp{name, authz.Member, http.StatusNotFound, ok, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{method, strings.NewReplacer("{m}", p.mon, "{hb}", p.hb).Replace(path), strings.ReplaceAll(body, "{name}", nameOr(w.monitorName[p.mon]))}, noCleanup
		}}
	}
	return []roleOp{
		get("get project", "/api/v1/projects/{p}", authz.Viewer),
		get("list project members", "/api/v1/projects/{p}/members", authz.Viewer),
		get("list monitors of project", "/api/v1/monitors?projectId={p}", authz.Viewer),
		get("list channels of project", "/api/v1/channels?projectId={p}", authz.Viewer),
		get("get monitor", "/api/v1/monitors/{m}", authz.Viewer),
		get("monitor stats", "/api/v1/monitors/{m}/stats", authz.Viewer),
		get("monitor checks", "/api/v1/monitors/{m}/checks", authz.Viewer),
		get("monitor incidents", "/api/v1/monitors/{m}/incidents", authz.Viewer),
		get("get channel", "/api/v1/channels/{c}", authz.Viewer),
		monitorWrite("update monitor", http.MethodPatch, "/api/v1/monitors/{m}", `{"name":"{name}"}`, http.StatusOK),
		monitorWrite("pause monitor", http.MethodPost, "/api/v1/monitors/{m}/pause", "", http.StatusOK),
		monitorWrite("resume monitor", http.MethodPost, "/api/v1/monitors/{m}/resume", "", http.StatusOK),
		monitorWrite("regenerate heartbeat token", http.MethodPost, "/api/v1/monitors/{hb}/heartbeat-token", "", http.StatusOK),
		{"create monitor", authz.Member, http.StatusUnprocessableEntity, http.StatusCreated, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"disposable","http":{"url":"https://example.com/x"},"projectId":"` + p.id + `"}`},
				func(r response) {
					if id, _ := r.body["id"].(string); id != "" {
						w.c.do(http.MethodDelete, "/api/v1/monitors/"+id, "", w.hdr("owner"))
					}
				}
		}},
		{"delete monitor", authz.Member, http.StatusNotFound, http.StatusNoContent, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			id := p.mon
			if p.key != "unknown" {
				r := w.c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"disposable","http":{"url":"https://example.com/x"},"projectId":"`+p.id+`"}`, w.hdr("owner"))
				id, _ = r.body["id"].(string)
			}
			return roleCall{http.MethodDelete, "/api/v1/monitors/" + id, ""}, func(response) {
				w.c.do(http.MethodDelete, "/api/v1/monitors/"+id, "", w.hdr("owner"))
			}
		}},
		{"create channel", authz.Maintainer, http.StatusUnprocessableEntity, http.StatusCreated, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodPost, "/api/v1/channels", `{"name":"disposable","webhookUrl":"https://hooks.slack.com/services/T000/B000/disposable","projectId":"` + p.id + `"}`},
				func(r response) {
					if id, _ := r.body["id"].(string); id != "" {
						w.c.do(http.MethodDelete, "/api/v1/channels/"+id, "", w.hdr("owner"))
					}
				}
		}},
		{"update channel", authz.Maintainer, http.StatusNotFound, http.StatusOK, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodPatch, "/api/v1/channels/" + p.ch, `{"name":"` + nameOr(w.channelName[p.ch]) + `"}`}, noCleanup
		}},
		{"delete channel", authz.Maintainer, http.StatusNotFound, http.StatusNoContent, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			id := p.ch
			if p.key != "unknown" {
				r := w.c.do(http.MethodPost, "/api/v1/channels", `{"name":"disposable","webhookUrl":"https://hooks.slack.com/services/T000/B000/disposable","projectId":"`+p.id+`"}`, w.hdr("owner"))
				id, _ = r.body["id"].(string)
			}
			return roleCall{http.MethodDelete, "/api/v1/channels/" + id, ""}, func(response) {
				w.c.do(http.MethodDelete, "/api/v1/channels/"+id, "", w.hdr("owner"))
			}
		}},
		{"put project member", authz.Maintainer, http.StatusNotFound, http.StatusOK, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodPut, "/api/v1/projects/" + p.id + "/members/" + w.people["tgt"].id, `{"role":"viewer"}`}, func(response) {
				w.c.do(http.MethodDelete, "/api/v1/projects/"+p.id+"/members/"+w.people["tgt"].id, "", w.hdr("owner"))
			}
		}},
		{"remove project member", authz.Maintainer, http.StatusNotFound, http.StatusNoContent, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			w.c.do(http.MethodPut, "/api/v1/projects/"+p.id+"/members/"+w.people["tgt"].id, `{"role":"viewer"}`, w.hdr("owner"))
			return roleCall{http.MethodDelete, "/api/v1/projects/" + p.id + "/members/" + w.people["tgt"].id, ""}, func(response) {
				w.c.do(http.MethodDelete, "/api/v1/projects/"+p.id+"/members/"+w.people["tgt"].id, "", w.hdr("owner"))
			}
		}},
		{"update project", authz.Maintainer, http.StatusNotFound, http.StatusOK, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodPatch, "/api/v1/projects/" + p.id, `{"name":"` + nameOr(w.projectNames[p.id]) + `"}`}, noCleanup
		}},
		// Every project of the world has content (or is the Default one), so a delete that is allowed is a 409.
		{"delete project", authz.Owner, http.StatusNotFound, http.StatusConflict, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodDelete, "/api/v1/projects/" + p.id, ""}, noCleanup
		}},
	}
}

// projectFirst names the operations whose service judges the role in the project before it looks
// for the project itself; for the others a monitor, channel or project that does not exist is a
// 404 whatever the role.
var projectFirst = map[string]bool{"update project": true, "delete project": true, "put project member": true, "remove project member": true}

// wantStatus is the status the hand-written rules give: no permission anywhere is 403 whatever the
// id (so it reveals nothing), no role in the project is the operation's invisible status, a role
// below the minimum is 403 and anything else succeeds.
func wantStatus(a roleActor, o roleOp, p roleProject) int {
	if !a.top().AtLeast(o.min) {
		return http.StatusForbidden
	}
	e := a.eff[p.key]
	if p.key == "unknown" {
		if !projectFirst[o.name] {
			return o.invisible
		}
		// The project does not exist: a person with an organization role is judged on that role.
		e = a.org
	}
	if e == "" {
		return o.invisible
	}
	if !e.AtLeast(o.min) {
		return http.StatusForbidden
	}
	if p.key == "unknown" {
		return o.invisible
	}
	return o.ok
}

// TestIntegrationTwoLevelRoleMatrix exercises every role at both levels against every
// project-scoped endpoint, with the ids of a project the person cannot see and ids that do not
// exist, and checks that the two get the same answer.
func TestIntegrationTwoLevelRoleMatrix(t *testing.T) {
	w := newProjectWorld(t, "twolevelmatrix")
	unknownID := "0192b9c4-7c5e-7000-8000-0000000000ee"
	projects := []roleProject{
		{"def", w.def, w.mDef, w.hbDef, w.chDef},
		{"alpha", w.alpha, w.mAlpha, w.hbAlpha, w.chAlpha},
		{"beta", w.beta, w.mBeta, w.hbBeta, w.chBeta},
		{"gamma", w.gamma, w.mGam, w.hbGamma, w.chGamma},
		{"unknown", unknownID, unknownID, unknownID, unknownID},
	}
	ops := roleOps()
	for _, a := range roleActors {
		hdr := w.hdr(a.name)
		for _, p := range projects {
			for _, o := range ops {
				call, cleanup := o.call(w, p)
				r := w.c.do(call.method, call.path, call.body, hdr)
				cleanup(r)
				want := wantStatus(a, o, p)
				if r.code != want {
					t.Errorf("%s: %s in %s: %d %s, want %d", a.name, o.name, p.key, r.code, r.raw, want)
					continue
				}
				switch want {
				case http.StatusNotFound:
					if r.body["type"] != "urn:proreta:problem:not-found" {
						t.Errorf("%s: %s in %s: 404 of type %v", a.name, o.name, p.key, r.body["type"])
					}
				case http.StatusForbidden:
					if r.body["type"] != "urn:proreta:problem:forbidden" {
						t.Errorf("%s: %s in %s: 403 of type %v", a.name, o.name, p.key, r.body["type"])
					}
				}
			}
		}
	}

	// Nothing the matrix did changed the data.
	owner := w.hdr("owner")
	if got := w.names(t, "/api/v1/monitors", owner, "id", w.monitorName); got != "hb-alpha,hb-beta,hb-default,hb-gamma,m-alpha,m-beta,m-default,m-gamma" {
		t.Errorf("monitors after the matrix: %s", got)
	}
	if got := w.names(t, "/api/v1/channels", owner, "id", w.channelName); got != "c-alpha,c-beta,c-default,c-gamma,c-org" {
		t.Errorf("channels after the matrix: %s", got)
	}
	if got := w.names(t, "/api/v1/projects", owner, "id", w.projectNames); got != "Alpha,Beta,Default,Gamma" {
		t.Errorf("projects after the matrix: %s", got)
	}
}

// TestIntegrationTwoLevelVisibility lists what each person sees: everybody with an organization
// role sees every project; a project-only person sees its projects, their monitors and channels
// plus the organization-wide channels, and nothing at all without a project.
func TestIntegrationTwoLevelVisibility(t *testing.T) {
	w := newProjectWorld(t, "twolevelvisibility")
	all := [3]string{"Alpha,Beta,Default,Gamma", "hb-alpha,hb-beta,hb-default,hb-gamma,m-alpha,m-beta,m-default,m-gamma", "c-alpha,c-beta,c-default,c-gamma,c-org"}
	alpha := [3]string{"Alpha", "hb-alpha,m-alpha", "c-alpha,c-org"}
	beta := [3]string{"Beta", "hb-beta,m-beta", "c-beta,c-org"}
	for name, want := range map[string][3]string{
		"owner": all, "maintainer": all, "member": all, "viewer": all, "raised": all, "mixOwner": all,
		"aOwner": alpha, "aMaint": alpha, "aMember": alpha, "aViewer": alpha,
		"bMember": beta,
		"pmix":    {"Alpha,Beta", "hb-alpha,hb-beta,m-alpha,m-beta", "c-alpha,c-beta,c-org"},
	} {
		hdr := w.hdr(name)
		if got := w.names(t, "/api/v1/projects", hdr, "id", w.projectNames); got != want[0] {
			t.Errorf("%s projects = %s, want %s", name, got, want[0])
		}
		if got := w.names(t, "/api/v1/monitors", hdr, "id", w.monitorName); got != want[1] {
			t.Errorf("%s monitors = %s, want %s", name, got, want[1])
		}
		if got := w.names(t, "/api/v1/channels", hdr, "id", w.channelName); got != want[2] {
			t.Errorf("%s channels = %s, want %s", name, got, want[2])
		}
	}
	// A person with no role anywhere is refused the project lists (403) and nothing leaks.
	for _, path := range []string{"/api/v1/projects", "/api/v1/monitors", "/api/v1/channels"} {
		if r := w.c.do(http.MethodGet, path, "", w.hdr("none")); r.code != http.StatusForbidden {
			t.Errorf("none GET %s: %d %s", path, r.code, r.raw)
		}
	}
	// The summary of the monitor list counts only visible monitors.
	if r := w.c.do(http.MethodGet, "/api/v1/monitors", "", w.hdr("aViewer")); fmt.Sprint(r.body["summary"]) != "map[down:0 paused:0 unknown:2 up:0]" {
		t.Errorf("aViewer summary: %s", r.raw)
	}
	// The channel picker of a project offers its own channels and the organization-wide ones.
	if got := w.names(t, "/api/v1/channels?projectId="+w.alpha, w.hdr("member"), "id", w.channelName); got != "c-alpha,c-org" {
		t.Errorf("member channel picker for alpha = %s", got)
	}
	// Usage is per organization and for organization owners and maintainers only.
	for _, a := range roleActors {
		r := w.c.do(http.MethodGet, "/api/v1/usage", "", w.hdr(a.name))
		if a.org.AtLeast(authz.Maintainer) {
			u, _ := r.body["usage"].(map[string]any)
			if r.code != http.StatusOK || u["monitors"] != float64(4) || u["heartbeats"] != float64(4) || u["projects"] != float64(4) {
				t.Errorf("%s usage: %d %s", a.name, r.code, r.raw)
			}
		} else if r.code != http.StatusForbidden {
			t.Errorf("%s usage: %d %s, want 403", a.name, r.code, r.raw)
		}
	}
	// Project members as the owner sees them: organization role, direct role, effective role.
	got := map[string]string{}
	r := w.c.do(http.MethodGet, "/api/v1/projects/"+w.alpha+"/members", "", w.hdr("owner"))
	ids := map[string]string{}
	for name, p := range w.people {
		ids[p.id] = name
	}
	for role, id := range w.userIDs {
		ids[id] = string(role)
	}
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		got[ids[m["userId"].(string)]] = fmt.Sprintf("%v/%v/%v/%v", m["organizationRole"], m["projectRole"], m["effectiveRole"], m["explicit"])
	}
	want := map[string]string{
		"owner": "owner/<nil>/owner/false", "maintainer": "maintainer/<nil>/maintainer/false",
		"member": "member/<nil>/member/false", "viewer": "viewer/<nil>/viewer/false",
		"raised": "viewer/maintainer/maintainer/true", "mixOwner": "member/<nil>/member/false",
		"aOwner": "<nil>/owner/owner/true", "aMaint": "<nil>/maintainer/maintainer/true",
		"aMember": "<nil>/member/member/true", "aViewer": "<nil>/viewer/viewer/true", "pmix": "<nil>/viewer/viewer/true",
	}
	var diff []string
	for k, v := range want {
		if got[k] != v {
			diff = append(diff, fmt.Sprintf("%s: %q, want %q", k, got[k], v))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			diff = append(diff, "unexpected member "+k)
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("alpha members: %s", strings.Join(diff, "; "))
	}
}
