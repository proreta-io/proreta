package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationProjectOnlyPeople covers the person without an organization role: it cannot reach
// any organization-level operation, it is still a member of the organization (the switcher,
// leaving, the member quota), and its role in the organization comes back when one is granted.
func TestIntegrationProjectOnlyPeople(t *testing.T) {
	w := newProjectWorld(t, "projectonly")
	c := w.c
	owner := w.hdr("owner")

	orgLevel := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/members", ""},
		{http.MethodPatch, "/api/v1/members/" + w.people["tgt"].id, `{"role":"viewer"}`},
		{http.MethodDelete, "/api/v1/members/" + w.people["tgt"].id, ""},
		{http.MethodGet, "/api/v1/invitations", ""},
		{http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`},
		{http.MethodGet, "/api/v1/usage", ""},
		{http.MethodPost, "/api/v1/projects", `{"name":"mine"}`},
		{http.MethodPost, "/api/v1/status-pages", `{"slug":"project-only","title":"x"}`},
		{http.MethodGet, "/api/v1/organization/auth-domains", ""},
		{http.MethodDelete, "/api/v1/organization", ""},
	}
	// Even an owner of every project they belong to holds no organization permission.
	for _, who := range []string{"aOwner", "aMaint", "aMember", "aViewer", "pmix", "none"} {
		for _, call := range orgLevel {
			if r := c.do(call.method, call.path, call.body, w.hdr(who)); r.code != http.StatusForbidden || r.body["type"] != "urn:proreta:problem:forbidden" {
				t.Errorf("%s %s %s: %d %s, want 403", who, call.method, call.path, r.code, r.raw)
			}
		}
	}

	// The account operations work: the session, the organization list with a null role, the switch.
	me := meOf(t, c, w.hdr("aOwner"))
	if me.code != http.StatusOK || me.body["role"] != nil {
		t.Fatalf("me of a project-only person: %d %s", me.code, me.raw)
	}
	orgs := c.do(http.MethodGet, "/api/v1/auth/organizations", "", w.hdr("aOwner"))
	items, _ := orgs.body["items"].([]any)
	if orgs.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["role"] != nil || items[0].(map[string]any)["current"] != true {
		t.Fatalf("organizations of a project-only person: %d %s", orgs.code, orgs.raw)
	}
	orgID := items[0].(map[string]any)["id"].(string)
	if r := c.do(http.MethodPost, "/api/v1/auth/switch-organization", `{"organizationId":"`+orgID+`"}`, w.hdr("aOwner")); r.code != http.StatusOK || r.body["role"] != nil {
		t.Fatalf("switch of a project-only person: %d %s", r.code, r.raw)
	}

	// The members list shows the null role; the people count as members.
	list := c.do(http.MethodGet, "/api/v1/members", "", owner)
	nulls := 0
	for _, it := range list.body["items"].([]any) {
		if it.(map[string]any)["role"] == nil {
			nulls++
		}
	}
	if list.code != http.StatusOK || nulls != 8 {
		t.Fatalf("members with no organization role: %d in %s", nulls, list.raw)
	}

	// Taking the role away mid-session applies on the next request.
	memberID := w.userIDs[authz.Member]
	if r := c.do(http.MethodPatch, "/api/v1/members/"+memberID, `{"role":null}`, owner); r.code != http.StatusOK || r.body["role"] != nil {
		t.Fatalf("member to project-only: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/members", "", w.hdr("member")); r.code != http.StatusForbidden {
		t.Fatalf("former member lists members: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/projects", "", w.hdr("member")); r.code != http.StatusForbidden {
		t.Fatalf("former member with no project lists projects: %d %s", r.code, r.raw)
	}
	// A project role makes the person visible in exactly that project.
	if r := c.do(http.MethodPut, "/api/v1/projects/"+w.gamma+"/members/"+memberID, `{"role":"viewer"}`, owner); r.code != http.StatusOK || r.body["organizationRole"] != nil {
		t.Fatalf("project role for a project-only person: %d %s", r.code, r.raw)
	}
	if got := w.names(t, "/api/v1/projects", w.hdr("member"), "id", w.projectNames); got != "Gamma" {
		t.Fatalf("former member sees %s", got)
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+w.gamma+`"}`, w.hdr("member")); r.code != http.StatusForbidden {
		t.Fatalf("viewer in gamma creates: %d %s", r.code, r.raw)
	}
	// An organization role comes back with the next PATCH and covers every project again, and the
	// viewer role in gamma does not lower it.
	if r := c.do(http.MethodPatch, "/api/v1/members/"+memberID, `{"role":"member"}`, owner); r.code != http.StatusOK || r.body["role"] != "member" {
		t.Fatalf("member role back: %d %s", r.code, r.raw)
	}
	if got := w.names(t, "/api/v1/projects", w.hdr("member"), "id", w.projectNames); got != "Alpha,Beta,Default,Gamma" {
		t.Fatalf("member sees %s", got)
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+w.gamma+`"}`, w.hdr("member")); r.code != http.StatusCreated {
		t.Fatalf("member creates in gamma: %d %s", r.code, r.raw)
	} else {
		c.do(http.MethodDelete, "/api/v1/monitors/"+r.body["id"].(string), "", owner)
	}

	// Leaving works without an organization role; the direct roles go with the membership.
	tgt := w.people["tgt"]
	if r := c.do(http.MethodPut, "/api/v1/projects/"+w.alpha+"/members/"+tgt.id, `{"role":"viewer"}`, owner); r.code != http.StatusOK {
		t.Fatalf("tgt into alpha: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/organization/leave", "", tgt.hdr); r.code != http.StatusNoContent {
		t.Fatalf("project-only person leaves: %d %s", r.code, r.raw)
	}
	if r := meOf(t, c, tgt.hdr); r.code != http.StatusUnauthorized {
		t.Fatalf("session after leaving: %d", r.code)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM project_members WHERE user_id = $1`, tgt.id); n != 0 {
		t.Fatalf("%d project roles survive the membership", n)
	}
	// A person with no role and no project can still see the organization and leave.
	if r := c.do(http.MethodPost, "/api/v1/organization/leave", "", w.people["none"].hdr); r.code != http.StatusNoContent {
		t.Fatalf("person without any role leaves: %d %s", r.code, r.raw)
	}
}

// TestIntegrationOrganizationMaintainerRules covers what an organization maintainer may and may not do.
func TestIntegrationOrganizationMaintainerRules(t *testing.T) {
	w := newProjectWorld(t, "orgmaintainer")
	c := w.c
	owner, maint := w.hdr("owner"), w.hdr("maintainer")
	ownerID := w.userIDs[authz.Owner]

	// The maintainer sees and manages everything below the organization ...
	if got := w.names(t, "/api/v1/projects", maint, "id", w.projectNames); got != "Alpha,Beta,Default,Gamma" {
		t.Fatalf("maintainer projects: %s", got)
	}
	r := c.do(http.MethodPost, "/api/v1/projects", `{"name":"Maintainers"}`, maint)
	if r.code != http.StatusCreated || r.body["role"] != "maintainer" {
		t.Fatalf("maintainer creates a project: %d %s", r.code, r.raw)
	}
	made := r.body["id"].(string)
	if r := c.do(http.MethodPatch, "/api/v1/projects/"+made, `{"name":"Renamed"}`, maint); r.code != http.StatusOK {
		t.Fatalf("maintainer renames: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, "/api/v1/projects/"+made+"/members/"+w.people["tgt"].id, `{"role":"maintainer"}`, maint); r.code != http.StatusOK {
		t.Fatalf("maintainer grants maintainer: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"wide","webhookUrl":"https://hooks.slack.com/services/T000/B000/wide","organizationWide":true}`, maint); r.code != http.StatusCreated {
		t.Fatalf("maintainer creates an organization-wide channel: %d %s", r.code, r.raw)
	} else {
		c.do(http.MethodDelete, "/api/v1/channels/"+r.body["id"].(string), "", owner)
	}
	// ... but deletes neither a project nor the organization, and grants no project owner.
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+made, "", maint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer deletes an empty project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/organization", "", maint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer deletes the organization: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, "/api/v1/projects/"+made+"/members/"+w.people["tgt"].id, `{"role":"owner"}`, maint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer grants project owner: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, "/api/v1/projects/"+made+"/members/"+ownerID, `{"role":"viewer"}`, maint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer changes the organization owner's project role: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+made, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("owner deletes the project: %d %s", r.code, r.raw)
	}

	// Organization roles: not an owner's, not a role above the maintainer's own.
	if r := c.do(http.MethodPatch, "/api/v1/members/"+ownerID, `{"role":null}`, maint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer takes the owner's role: %d %s", r.code, r.raw)
	}
	viewerOnly := w.people["aViewer"].id
	if r := c.do(http.MethodPatch, "/api/v1/members/"+viewerOnly, `{"role":"owner"}`, maint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer grants owner to a project-only person: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/members/"+viewerOnly, `{"role":"viewer"}`, maint); r.code != http.StatusOK || r.body["role"] != "viewer" {
		t.Fatalf("maintainer gives a project-only person a role: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/members/"+viewerOnly, `{"role":null}`, maint); r.code != http.StatusOK || r.body["role"] != nil {
		t.Fatalf("maintainer makes a viewer project-only: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/members/"+viewerOnly, `{"role":"root"}`, maint); r.code != http.StatusBadRequest {
		t.Fatalf("unknown role: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/members/"+w.userIDs[authz.Member], `{"role":null}`, w.hdr("member")); r.code != http.StatusForbidden {
		t.Fatalf("member changes roles: %d %s", r.code, r.raw)
	}
	// The project roles of the person that became project-only are still there.
	if r := c.do(http.MethodGet, "/api/v1/projects/"+w.alpha, "", w.hdr("aViewer")); r.code != http.StatusOK || r.body["role"] != "viewer" {
		t.Fatalf("project-only person reads alpha: %d %s", r.code, r.raw)
	}
}

// TestIntegrationLastOwnerWithoutRole is the last-owner rule of the organization with the new
// way to lose the role: taking it away counts like a demotion.
func TestIntegrationLastOwnerWithoutRole(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("lastownernull"))
	c := newRegistrationServer(t, env, membersOpts(), true, &lockedBuffer{})
	oCookie, oCSRF, _ := c.register("owner@example.com")
	owner := withCSRF(oCookie, oCSRF)
	ownerID := userIDOf(t, c, owner)
	tok, _ := invite(t, c, owner, "maintainer")
	second, _ := joinByRegister(t, c, tok, "second@example.com")
	secondID := userIDOf(t, c, second)

	for _, body := range []string{`{"role":"maintainer"}`, `{"role":null}`} {
		if r := c.do(http.MethodPatch, "/api/v1/members/"+ownerID, body, owner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:last-owner" {
			t.Fatalf("last owner %s: %d %s", body, r.code, r.raw)
		}
	}
	if r := c.do(http.MethodPatch, "/api/v1/members/"+secondID, `{"role":"owner"}`, owner); r.code != http.StatusOK {
		t.Fatalf("second owner: %d %s", r.code, r.raw)
	}
	// An owner steps down to project-only while another owner exists ...
	if r := c.do(http.MethodPatch, "/api/v1/members/"+secondID, `{"role":null}`, second); r.code != http.StatusOK || r.body["role"] != nil {
		t.Fatalf("an owner steps down to project-only: %d %s", r.code, r.raw)
	}
	// ... its session reads the new state on the next request ...
	if r := c.do(http.MethodGet, "/api/v1/members", "", second); r.code != http.StatusForbidden {
		t.Fatalf("stepped-down owner lists members: %d %s", r.code, r.raw)
	}
	// ... and the other owner is the last one.
	if r := c.do(http.MethodPatch, "/api/v1/members/"+ownerID, `{"role":null}`, owner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:last-owner" {
		t.Fatalf("new last owner: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/members/"+ownerID, "", owner); r.code != http.StatusConflict {
		t.Fatalf("remove the last owner: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/organization/leave", "", owner); r.code != http.StatusConflict {
		t.Fatalf("last owner leaves: %d %s", r.code, r.raw)
	}
	if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'member.role_changed' AND metadata->>'to' = 'none' AND metadata->>'from' = 'owner'`); n != 1 {
		t.Fatalf("%d audit rows with the role taken away", n)
	}
}

// TestIntegrationProjectRoleRules covers the rules inside one project: who deletes it, who grants
// and changes which direct role, and that a change applies on the next request.
func TestIntegrationProjectRoleRules(t *testing.T) {
	w := newProjectWorld(t, "projectrolerules")
	c := w.c
	owner := w.hdr("owner")
	aOwner, aMaint, aViewer := w.hdr("aOwner"), w.hdr("aMaint"), w.hdr("aViewer")
	tgt := w.people["tgt"].id
	put := func(hdr map[string]string, project, user, role string) response {
		return c.do(http.MethodPut, "/api/v1/projects/"+project+"/members/"+user, `{"role":"`+role+`"}`, hdr)
	}

	// A project owner deletes its own project only when it is empty; a maintainer never.
	r := c.do(http.MethodPost, "/api/v1/projects", `{"name":"Empty"}`, owner)
	empty := r.body["id"].(string)
	for _, who := range []struct{ name, role string }{{"aOwner", "owner"}, {"aMaint", "maintainer"}} {
		if r := put(owner, empty, w.people[who.name].id, who.role); r.code != http.StatusOK {
			t.Fatalf("%s into the empty project: %d %s", who.name, r.code, r.raw)
		}
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+empty, "", aMaint); r.code != http.StatusForbidden {
		t.Fatalf("project maintainer deletes: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.alpha, "", aOwner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:project-not-empty" {
		t.Fatalf("project owner deletes a project with monitors: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.beta, "", aOwner); r.code != http.StatusNotFound {
		t.Fatalf("project owner of alpha deletes beta: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+empty, "", aOwner); r.code != http.StatusNoContent {
		t.Fatalf("project owner deletes its empty project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/projects/"+empty, "", aOwner); r.code != http.StatusNotFound {
		t.Fatalf("deleted project: %d %s", r.code, r.raw)
	}
	// An organization member who owns beta can delete beta (not empty here), not another project.
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.beta, "", w.hdr("mixOwner")); r.code != http.StatusConflict {
		t.Fatalf("organization member owning beta deletes it: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.gamma, "", w.hdr("mixOwner")); r.code != http.StatusForbidden {
		t.Fatalf("organization member deletes gamma: %d %s", r.code, r.raw)
	}

	// A project maintainer grants up to maintainer, never owner, and never touches an owner.
	if r := put(aMaint, w.alpha, tgt, "maintainer"); r.code != http.StatusOK || r.body["effectiveRole"] != "maintainer" {
		t.Fatalf("maintainer grants maintainer: %d %s", r.code, r.raw)
	}
	if r := put(aMaint, w.alpha, tgt, "owner"); r.code != http.StatusForbidden {
		t.Fatalf("maintainer grants owner: %d %s", r.code, r.raw)
	}
	if r := put(aMaint, w.alpha, w.people["aOwner"].id, "viewer"); r.code != http.StatusForbidden {
		t.Fatalf("maintainer lowers the project owner: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.alpha+"/members/"+w.people["aOwner"].id, "", aMaint); r.code != http.StatusForbidden {
		t.Fatalf("maintainer removes the project owner: %d %s", r.code, r.raw)
	}
	if r := put(aMaint, w.alpha, w.userIDs[authz.Owner], "viewer"); r.code != http.StatusForbidden {
		t.Fatalf("project maintainer touches the organization owner: %d %s", r.code, r.raw)
	}
	// The new maintainer works at once; the owner grants owner.
	if r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"by tgt","webhookUrl":"https://hooks.slack.com/services/T000/B000/tgt","projectId":"`+w.alpha+`"}`, w.people["tgt"].hdr); r.code != http.StatusCreated {
		t.Fatalf("new project maintainer creates a channel: %d %s", r.code, r.raw)
	} else {
		c.do(http.MethodDelete, "/api/v1/channels/"+r.body["id"].(string), "", owner)
	}
	if r := put(aOwner, w.alpha, tgt, "owner"); r.code != http.StatusOK || r.body["effectiveRole"] != "owner" {
		t.Fatalf("owner grants owner: %d %s", r.code, r.raw)
	}
	if r := put(aMaint, w.alpha, tgt, "viewer"); r.code != http.StatusForbidden {
		t.Fatalf("maintainer lowers the new owner: %d %s", r.code, r.raw)
	}
	// An owner may step down (no last-owner rule inside a project: organization owners keep control).
	if r := put(w.people["tgt"].hdr, w.alpha, tgt, "viewer"); r.code != http.StatusOK || r.body["effectiveRole"] != "viewer" {
		t.Fatalf("project owner steps down: %d %s", r.code, r.raw)
	}
	// The organization maintainer is limited the same way; the organization owner is not.
	if r := put(w.hdr("maintainer"), w.alpha, tgt, "owner"); r.code != http.StatusForbidden {
		t.Fatalf("organization maintainer grants project owner: %d %s", r.code, r.raw)
	}
	if r := put(owner, w.alpha, tgt, "owner"); r.code != http.StatusOK {
		t.Fatalf("organization owner grants project owner: %d %s", r.code, r.raw)
	}
	if r := put(w.hdr("maintainer"), w.alpha, tgt, "viewer"); r.code != http.StatusForbidden {
		t.Fatalf("organization maintainer lowers a project owner: %d %s", r.code, r.raw)
	}
	// Members and viewers of a project manage nobody.
	if r := put(w.hdr("aMember"), w.alpha, tgt, "viewer"); r.code != http.StatusForbidden {
		t.Fatalf("project member manages people: %d %s", r.code, r.raw)
	}
	if r := put(aViewer, w.alpha, tgt, "viewer"); r.code != http.StatusForbidden {
		t.Fatalf("project viewer manages people: %d %s", r.code, r.raw)
	}
	if r := put(owner, w.alpha, tgt, "admin"); r.code != http.StatusBadRequest {
		t.Fatalf("unknown role: %d %s", r.code, r.raw)
	}
	if r := put(owner, w.alpha, "0192b9c4-7c5e-7000-8000-0000000000ee", "viewer"); r.code != http.StatusNotFound {
		t.Fatalf("person outside the organization: %d %s", r.code, r.raw)
	}

	// A role change applies on the next request, both ways.
	body := `{"name":"c","webhookUrl":"https://hooks.slack.com/services/T000/B000/c","projectId":"` + w.alpha + `"}`
	if r := c.do(http.MethodPost, "/api/v1/channels", body, aViewer); r.code != http.StatusForbidden {
		t.Fatalf("viewer creates a channel: %d %s", r.code, r.raw)
	}
	put(aOwner, w.alpha, w.people["aViewer"].id, "maintainer")
	if r := c.do(http.MethodPost, "/api/v1/channels", body, aViewer); r.code != http.StatusCreated {
		t.Fatalf("raised viewer creates a channel: %d %s", r.code, r.raw)
	} else {
		c.do(http.MethodDelete, "/api/v1/channels/"+r.body["id"].(string), "", owner)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.alpha+"/members/"+w.people["aViewer"].id, "", aOwner); r.code != http.StatusNoContent {
		t.Fatalf("remove the direct role: %d %s", r.code, r.raw)
	}
	// The person has no role anywhere now: nothing passes the table.
	if r := c.do(http.MethodGet, "/api/v1/projects/"+w.alpha, "", aViewer); r.code != http.StatusForbidden {
		t.Fatalf("person with no role reads a project: %d %s", r.code, r.raw)
	}
	// A project maintainer removes its own role and loses the project.
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+w.alpha+"/members/"+w.people["aMaint"].id, "", aMaint); r.code != http.StatusNoContent {
		t.Fatalf("self removal: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/monitors/"+w.mAlpha, "", aMaint); r.code != http.StatusForbidden && r.code != http.StatusNotFound {
		t.Fatalf("removed maintainer reads alpha: %d %s", r.code, r.raw)
	}
}

// TestIntegrationProjectChannelRules covers channels for project maintainers: they manage the
// channels of their project, never an organization-wide one, and cannot move one out of reach.
func TestIntegrationProjectChannelRules(t *testing.T) {
	w := newProjectWorld(t, "projectchannels")
	c := w.c
	aMaint := w.hdr("aMaint")
	hook := `"webhookUrl":"https://hooks.slack.com/services/T000/B000/x"`

	if r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"wide",`+hook+`,"organizationWide":true}`, aMaint); r.code != http.StatusForbidden {
		t.Fatalf("project maintainer creates an organization-wide channel: %d %s", r.code, r.raw)
	}
	for _, call := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPatch, "/api/v1/channels/" + w.chOrg, `{"name":"c-org"}`, http.StatusForbidden},
		{http.MethodDelete, "/api/v1/channels/" + w.chOrg, "", http.StatusForbidden},
		{http.MethodPatch, "/api/v1/channels/" + w.chAlpha, `{"organizationWide":true}`, http.StatusForbidden},
		{http.MethodPatch, "/api/v1/channels/" + w.chAlpha, `{"projectId":"` + w.beta + `"}`, http.StatusUnprocessableEntity},
	} {
		if r := c.do(call.method, call.path, call.body, aMaint); r.code != call.want {
			t.Errorf("project maintainer %s %s %s: %d %s, want %d", call.method, call.path, call.body, r.code, r.raw, call.want)
		}
	}
	// Created without a project id a channel goes to Default, which they cannot see.
	if r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"x",`+hook+`}`, aMaint); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("channel without a project: %d %s", r.code, r.raw)
	}
	r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"x",`+hook+`,"projectId":"`+w.alpha+`"}`, aMaint)
	if r.code != http.StatusCreated || r.body["projectId"] != w.alpha {
		t.Fatalf("project maintainer creates a channel: %d %s", r.code, r.raw)
	}
	if _, ok := r.body["webhookUrl"]; ok {
		t.Fatalf("the webhook URL is returned: %s", r.raw)
	}
	// The test message: another project's channel is 404 (and spends the rate limit), their own works.
	if r := c.do(http.MethodPost, "/api/v1/channels/"+w.chGamma+"/test", "", aMaint); r.code != http.StatusNotFound {
		t.Fatalf("test of a channel in another project: %d %s", r.code, r.raw)
	}
	time.Sleep(5200 * time.Millisecond)
	if r := c.do(http.MethodPost, "/api/v1/channels/"+w.chAlpha+"/test", "", aMaint); r.code != http.StatusNoContent {
		t.Fatalf("test of their own channel: %d %s", r.code, r.raw)
	}
	// An organization maintainer manages the organization-wide channel.
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chOrg, `{"name":"c-org"}`, w.hdr("maintainer")); r.code != http.StatusOK {
		t.Fatalf("organization maintainer updates the organization-wide channel: %d %s", r.code, r.raw)
	}
	// An organization viewer who is maintainer of alpha manages alpha's channels only.
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chBeta, `{"name":"c-beta"}`, w.hdr("raised")); r.code != http.StatusForbidden {
		t.Fatalf("raised viewer updates beta's channel: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chAlpha, `{"name":"c-alpha"}`, w.hdr("raised")); r.code != http.StatusOK {
		t.Fatalf("raised viewer updates alpha's channel: %d %s", r.code, r.raw)
	}
}

// TestIntegrationProjectOnlyCountsAgainstMembers checks that a person without an organization role
// still uses a place of the plan's member limit.
func TestIntegrationProjectOnlyCountsAgainstMembers(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("projectonlyquota"))
	c := newRegistrationServer(t, env, membersOpts(), true, &lockedBuffer{})
	oCookie, oCSRF, _ := c.register("owner@example.com")
	owner := withCSRF(oCookie, oCSRF)

	tok, _ := invite(t, c, owner, "viewer")
	viewer, _ := joinByRegister(t, c, tok, "viewer@example.com")
	viewerID := userIDOf(t, c, viewer)
	invite(t, c, owner, "viewer")
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, owner); r.code != http.StatusConflict || r.body["limit"] != "members" {
		t.Fatalf("at the limit: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/members/"+viewerID, `{"role":null}`, owner); r.code != http.StatusOK {
		t.Fatalf("viewer to project-only: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, owner); r.code != http.StatusConflict || r.body["limit"] != "members" {
		t.Fatalf("project-only person still counts: %d %s", r.code, r.raw)
	}
	if u := c.do(http.MethodGet, "/api/v1/usage", "", owner); !strings.Contains(u.raw, `"members":3`) {
		t.Fatalf("usage: %s", u.raw)
	}
	// Removing the person frees the place.
	if r := c.do(http.MethodDelete, "/api/v1/members/"+viewerID, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, owner); r.code != http.StatusCreated {
		t.Fatalf("after the removal: %d %s", r.code, r.raw)
	}
}
