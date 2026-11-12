package api_test

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// person is a signed-in user of a test organization.
type person struct {
	hdr map[string]string
	id  string
}

// projectWorld is an organization with four projects and people in them:
//   - four people with an organization role (owner, maintainer, member, viewer), who all see every
//     project, and people without one (project-only) with direct roles: aOwner, aMaint, aMember and
//     aViewer in alpha, bMember in beta, pmix (viewer in alpha, member in beta), raised (an
//     organization viewer who is maintainer of alpha), mixOwner (an organization member who owns
//     beta), tgt (in no project, the target of member changes) and none (in no project);
//   - one monitor and one heartbeat per project, one channel per project (the Default one is
//     c-default) and an organization-wide channel.
type projectWorld struct {
	c                         *client
	env                       *pgtest.Env
	sessions                  map[authz.Role]map[string]string
	userIDs                   map[authz.Role]string
	people                    map[string]person
	def, alpha, beta, gamma   string
	mDef, mAlpha, mBeta, mGam string
	hbDef, hbAlpha, hbBeta    string
	hbGamma                   string
	chDef, chAlpha, chBeta    string
	chGamma, chOrg            string
	monitorName, channelName  map[string]string
	projectNames              map[string]string
}

// hdr returns the session of an organization role or of a named person.
func (w *projectWorld) hdr(name string) map[string]string {
	if p, ok := w.people[name]; ok {
		return p.hdr
	}
	return w.sessions[authz.Role(name)]
}

func newProjectWorld(t *testing.T, name string) *projectWorld {
	t.Helper()
	env := pgtest.New(t, pgtest.DBName(name))
	c := newRegistrationServer(t, env, membersOpts(), true, &lockedBuffer{})
	w := &projectWorld{c: c, env: env, sessions: map[authz.Role]map[string]string{}, userIDs: map[authz.Role]string{},
		people:      map[string]person{},
		monitorName: map[string]string{}, channelName: map[string]string{}, projectNames: map[string]string{}}
	owner := adminSession(c)
	w.sessions[authz.Owner] = owner
	w.userIDs[authz.Owner] = userIDOf(t, c, owner)
	for _, role := range []authz.Role{authz.Maintainer, authz.Member, authz.Viewer} {
		token, _ := invite(t, c, owner, string(role))
		w.sessions[role], _ = joinByRegister(t, c, token, "p-"+string(role)+"@example.com")
		w.userIDs[role] = userIDOf(t, c, w.sessions[role])
	}
	list := c.do(http.MethodGet, "/api/v1/projects", "", owner)
	items, _ := list.body["items"].([]any)
	if list.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("initial projects: %d %s", list.code, list.raw)
	}
	w.def = items[0].(map[string]any)["id"].(string)
	w.projectNames[w.def] = "Default"
	mk := func(name string) string {
		r := c.do(http.MethodPost, "/api/v1/projects", `{"name":"`+name+`"}`, owner)
		if r.code != http.StatusCreated {
			t.Fatalf("create project %s: %d %s", name, r.code, r.raw)
		}
		id := r.body["id"].(string)
		w.projectNames[id] = name
		return id
	}
	w.alpha, w.beta, w.gamma = mk("Alpha"), mk("Beta"), mk("Gamma")

	// A person joins with an organization role; projectOnly then takes the role away.
	join := func(name, orgRole string, projectOnly bool) {
		token, _ := invite(t, c, owner, orgRole)
		hdr, _ := joinByRegister(t, c, token, "p-"+strings.ToLower(name)+"@example.com")
		p := person{hdr: hdr, id: userIDOf(t, c, hdr)}
		if projectOnly {
			if r := c.do(http.MethodPatch, "/api/v1/members/"+p.id, `{"role":null}`, owner); r.code != http.StatusOK || r.body["role"] != nil {
				t.Fatalf("make %s project-only: %d %s", name, r.code, r.raw)
			}
		}
		w.people[name] = p
	}
	put := func(project, who string, role string) {
		id := w.people[who].id
		if id == "" {
			id = w.userIDs[authz.Role(who)]
		}
		r := c.do(http.MethodPut, "/api/v1/projects/"+project+"/members/"+id, `{"role":"`+role+`"}`, owner)
		if r.code != http.StatusOK {
			t.Fatalf("put %s in %s: %d %s", who, project, r.code, r.raw)
		}
	}
	for _, n := range []string{"aOwner", "aMaint", "aMember", "aViewer", "bMember", "pmix", "tgt", "none"} {
		join(n, "viewer", true)
	}
	join("raised", "viewer", false)
	join("mixOwner", "member", false)
	put(w.alpha, "aOwner", "owner")
	put(w.alpha, "aMaint", "maintainer")
	put(w.alpha, "aMember", "member")
	put(w.alpha, "aViewer", "viewer")
	put(w.beta, "bMember", "member")
	put(w.alpha, "pmix", "viewer")
	put(w.beta, "pmix", "member")
	put(w.alpha, "raised", "maintainer")
	put(w.beta, "mixOwner", "owner")

	monitor := func(project, name, extra string) string {
		r := c.do(http.MethodPost, "/api/v1/monitors", `{"name":"`+name+`","projectId":"`+project+`"`+extra+`}`, owner)
		if r.code != http.StatusCreated || r.body["projectId"] != project {
			t.Fatalf("create monitor %s: %d %s", name, r.code, r.raw)
		}
		id := r.body["id"].(string)
		w.monitorName[id] = name
		return id
	}
	w.mDef = monitor(w.def, "m-default", `,"type":"http","http":{"url":"https://example.com/d"}`)
	w.mAlpha = monitor(w.alpha, "m-alpha", `,"type":"http","http":{"url":"https://example.com/a"}`)
	w.mBeta = monitor(w.beta, "m-beta", `,"type":"http","http":{"url":"https://example.com/b"}`)
	w.mGam = monitor(w.gamma, "m-gamma", `,"type":"http","http":{"url":"https://example.com/g"}`)
	w.hbDef = monitor(w.def, "hb-default", `,"type":"heartbeat","heartbeat":{}`)
	w.hbAlpha = monitor(w.alpha, "hb-alpha", `,"type":"heartbeat","heartbeat":{}`)
	w.hbBeta = monitor(w.beta, "hb-beta", `,"type":"heartbeat","heartbeat":{}`)
	w.hbGamma = monitor(w.gamma, "hb-gamma", `,"type":"heartbeat","heartbeat":{}`)

	channel := func(name, scope string) string {
		r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"`+name+`","webhookUrl":"https://hooks.slack.com/services/T000/B000/`+name+`"`+scope+`}`, owner)
		if r.code != http.StatusCreated {
			t.Fatalf("create channel %s: %d %s", name, r.code, r.raw)
		}
		id := r.body["id"].(string)
		w.channelName[id] = name
		return id
	}
	w.chDef = channel("c-default", "")
	w.chAlpha = channel("c-alpha", `,"projectId":"`+w.alpha+`"`)
	w.chBeta = channel("c-beta", `,"projectId":"`+w.beta+`"`)
	w.chGamma = channel("c-gamma", `,"projectId":"`+w.gamma+`"`)
	w.chOrg = channel("c-org", `,"organizationWide":true`)
	return w
}

func (w *projectWorld) names(t *testing.T, path string, hdr map[string]string, field string, dict map[string]string) string {
	t.Helper()
	r := w.c.do(http.MethodGet, path, "", hdr)
	if r.code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	var out []string
	for _, it := range items {
		id := it.(map[string]any)[field].(string)
		n, ok := dict[id]
		if !ok {
			n = id
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// TestIntegrationProjectsLifecycle covers projects CRUD, members, the project cap, and how
// channels follow moves and deletes.
func TestIntegrationProjectsLifecycle(t *testing.T) {
	w := newProjectWorld(t, "projectlife")
	c := w.c
	owner := w.sessions[authz.Owner]
	admin := w.sessions[authz.Maintainer]

	// Slugs: derived, suffixed when taken, explicit ones must be free.
	r := c.do(http.MethodPost, "/api/v1/projects", `{"name":"  Alpha  "}`, admin)
	if r.code != http.StatusCreated || r.body["slug"] != "alpha-2" || r.body["name"] != "Alpha" || r.body["role"] != "maintainer" {
		t.Fatalf("second Alpha: %d %s", r.code, r.raw)
	}
	second := r.body["id"].(string)
	if r := c.do(http.MethodPost, "/api/v1/projects", `{"name":"X","slug":"beta"}`, admin); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:slug-taken" {
		t.Fatalf("taken slug: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/projects/"+second, `{"name":"Alpha two","slug":"alpha-two"}`, admin); r.code != http.StatusOK || r.body["slug"] != "alpha-two" {
		t.Fatalf("rename: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/projects/"+second, `{"name":"   "}`, admin); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("blank name: %d %s", r.code, r.raw)
	}

	// Delete: the Default project never, a project with monitors or channels not, an empty one yes.
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.def, "", owner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:default-project" {
		t.Fatalf("delete Default: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.gamma, "", owner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:project-not-empty" {
		t.Fatalf("delete gamma: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+second, "", admin); r.code != http.StatusForbidden {
		t.Fatalf("maintainer deletes an empty project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+second, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("delete empty: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/projects/"+second, "", owner); r.code != http.StatusNotFound {
		t.Fatalf("deleted project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+second+`"}`, owner); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("monitor into a deleted project: %d %s", r.code, r.raw)
	}

	// Members of a project: everybody with an organization role (it applies to every project) and
	// everybody with a direct role.
	r = c.do(http.MethodGet, "/api/v1/projects/"+w.beta+"/members", "", owner)
	if items, _ := r.body["items"].([]any); r.code != http.StatusOK || len(items) != 8 {
		t.Fatalf("beta members (6 with an organization role, 2 project-only): %d %s", r.code, r.raw)
	}
	r = c.do(http.MethodGet, "/api/v1/projects/"+w.def+"/members", "", w.sessions[authz.Viewer])
	if items, _ := r.body["items"].([]any); r.code != http.StatusOK || len(items) != 6 {
		t.Fatalf("Default members (organization roles only): %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, "/api/v1/projects/"+w.beta+"/members/0192b9c4-7c5e-7000-8000-0000000000ee", `{"role":"member"}`, owner); r.code != http.StatusNotFound {
		t.Fatalf("non-member into a project: %d %s", r.code, r.raw)
	}
	// A direct role raises the organization role inside the project only.
	createIn := func(project string, hdr map[string]string) response {
		r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+project+`"}`, hdr)
		if id, _ := r.body["id"].(string); id != "" {
			c.do(http.MethodDelete, "/api/v1/monitors/"+id, "", owner)
		}
		return r
	}
	if r := createIn(w.beta, w.sessions[authz.Viewer]); r.code != http.StatusForbidden {
		t.Fatalf("viewer creates: %d %s", r.code, r.raw)
	}
	r = c.do(http.MethodPut, "/api/v1/projects/"+w.beta+"/members/"+w.userIDs[authz.Viewer], `{"role":"member"}`, owner)
	if r.code != http.StatusOK || r.body["effectiveRole"] != "member" || r.body["organizationRole"] != "viewer" {
		t.Fatalf("viewer with member role: %d %s", r.code, r.raw)
	}
	if r := createIn(w.beta, w.sessions[authz.Viewer]); r.code != http.StatusCreated {
		t.Fatalf("viewer raised to member creates in beta: %d %s", r.code, r.raw)
	}
	if r := createIn(w.alpha, w.sessions[authz.Viewer]); r.code != http.StatusForbidden {
		t.Fatalf("the raise stays in beta: %d %s", r.code, r.raw)
	}
	// ... and never lowers it.
	r = c.do(http.MethodPut, "/api/v1/projects/"+w.beta+"/members/"+w.userIDs[authz.Member], `{"role":"viewer"}`, owner)
	if r.code != http.StatusOK || r.body["effectiveRole"] != "member" || r.body["projectRole"] != "viewer" {
		t.Fatalf("member with viewer role: %d %s", r.code, r.raw)
	}
	if r := createIn(w.beta, w.sessions[authz.Member]); r.code != http.StatusCreated {
		t.Fatalf("a viewer role lowered an organization member: %d %s", r.code, r.raw)
	}

	// Channel moves and deletes keep selections inside the project. m-alpha selects c-alpha and
	// c-org; moving c-alpha to beta drops it from m-alpha.
	if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"channelIds":["`+w.chAlpha+`","`+w.chOrg+`"]}`, owner); r.code != http.StatusOK {
		t.Fatalf("select: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chAlpha, `{"projectId":"`+w.beta+`","organizationWide":true}`, owner); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("both project and organization-wide: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chAlpha, `{"projectId":"`+w.beta+`"}`, owner); r.code != http.StatusOK || r.body["projectId"] != w.beta {
		t.Fatalf("move channel: %d %s", r.code, r.raw)
	}
	selected := func(monitor string) string {
		r := c.do(http.MethodGet, "/api/v1/monitors/"+monitor, "", owner)
		var out []string
		for _, id := range r.body["channelIds"].([]any) {
			out = append(out, w.channelName[id.(string)])
		}
		return strings.Join(out, ",")
	}
	if got := selected(w.mAlpha); got != "c-org" {
		t.Fatalf("after moving c-alpha: %s", got)
	}
	// Moving the monitor to gamma keeps the organization-wide channel; deleting it leaves the
	// monitor without a selection, which now means gamma's channels only.
	if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"projectId":"`+w.gamma+`","channelIds":["`+w.chOrg+`","`+w.chGamma+`"]}`, owner); r.code != http.StatusOK || r.body["projectId"] != w.gamma {
		t.Fatalf("move monitor: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chOrg, `{"organizationWide":true}`, owner); r.code != http.StatusOK || r.body["projectId"] != nil {
		t.Fatalf("org-wide stays: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chGamma, `{"organizationWide":true}`, owner); r.code != http.StatusOK || r.body["projectId"] != nil {
		t.Fatalf("make org-wide: %d %s", r.code, r.raw)
	}
	if got := selected(w.mAlpha); got != "c-gamma,c-org" && got != "c-org,c-gamma" {
		t.Fatalf("org-wide move keeps links: %s", got)
	}
	for _, ch := range []string{w.chOrg, w.chGamma} {
		if r := c.do(http.MethodDelete, "/api/v1/channels/"+ch, "", owner); r.code != http.StatusNoContent {
			t.Fatalf("delete channel: %d %s", r.code, r.raw)
		}
	}
	if got := selected(w.mAlpha); got != "" {
		t.Fatalf("after deletes: %s", got)
	}

	// The project cap (max_projects through the plans mechanism): 409 plan-limit-reached.
	r = c.do(http.MethodGet, "/api/v1/usage", "", owner)
	if l, _ := r.body["limits"].(map[string]any); l["projects"] != float64(50) {
		t.Fatalf("project limit: %s", r.raw)
	}
	for i := 0; i < 46; i++ {
		if r := c.do(http.MethodPost, "/api/v1/projects", fmt.Sprintf(`{"name":"P %d"}`, i), owner); r.code != http.StatusCreated {
			t.Fatalf("project %d: %d %s", i, r.code, r.raw)
		}
	}
	if r := c.do(http.MethodPost, "/api/v1/projects", `{"name":"one too many"}`, owner); r.code != http.StatusConflict ||
		r.body["type"] != "urn:proreta:problem:plan-limit-reached" || r.body["limit"] != "projects" || r.body["max"] != float64(50) {
		t.Fatalf("51st project: %d %s", r.code, r.raw)
	}
}

// TestIntegrationProjectMoves checks that the monitor detail shows only channels routing honours
// (a stray link to another project's channel is hidden), and that concurrent monitor and channel
// moves serialise (no deadlock, no stray link left).
func TestIntegrationProjectMoves(t *testing.T) {
	w := newProjectWorld(t, "projectmoves")
	c := w.c
	owner := w.sessions[authz.Owner]
	admin := w.env.AdminConn(t)

	pgtest.MustExec(t, admin, `INSERT INTO monitor_channels (tenant_id, monitor_id, channel_id)
		SELECT tenant_id, '`+w.mAlpha+`', id FROM notification_channels WHERE id IN ('`+w.chBeta+`', '`+w.chOrg+`')`)
	r := c.do(http.MethodGet, "/api/v1/monitors/"+w.mAlpha, "", owner)
	if ids, _ := r.body["channelIds"].([]any); len(ids) != 1 || ids[0] != w.chOrg {
		t.Fatalf("detail channelIds with a stray link: %s", r.raw)
	}
	pgtest.MustExec(t, admin, `DELETE FROM monitor_channels WHERE monitor_id = '`+w.mAlpha+`'`)

	// m-alpha selects c-alpha; at the same time the monitor and the channel move. Whatever the
	// order, both succeed and no monitor keeps a channel of another project.
	for i := 0; i < 10; i++ {
		monProject, chProject := w.beta, w.gamma
		if i%2 == 1 {
			monProject, chProject = w.gamma, w.beta
		}
		if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chAlpha, `{"projectId":"`+w.alpha+`"}`, owner); r.code != http.StatusOK {
			t.Fatalf("reset channel: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"projectId":"`+w.alpha+`","channelIds":["`+w.chAlpha+`"]}`, owner); r.code != http.StatusOK {
			t.Fatalf("reset monitor: %d %s", r.code, r.raw)
		}
		var wg sync.WaitGroup
		var codes [2]int
		var raws [2]string
		wg.Add(2)
		go func() {
			defer wg.Done()
			r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"projectId":"`+monProject+`"}`, owner)
			codes[0], raws[0] = r.code, r.raw
		}()
		go func() {
			defer wg.Done()
			r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chAlpha, `{"projectId":"`+chProject+`"}`, owner)
			codes[1], raws[1] = r.code, r.raw
		}()
		wg.Wait()
		if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
			t.Fatalf("round %d: monitor move %d %s, channel move %d %s", i, codes[0], raws[0], codes[1], raws[1])
		}
		var stray int
		if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM monitor_channels mc
			JOIN monitors m ON m.id = mc.monitor_id JOIN notification_channels ch ON ch.id = mc.channel_id
			WHERE ch.project_id IS NOT NULL AND ch.project_id <> m.project_id`).Scan(&stray); err != nil || stray != 0 {
			t.Fatalf("round %d: %d stray links (%v)", i, stray, err)
		}
	}
}
