package api_test

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// inviteInto creates an invitation into a project and returns the response; on 201 also the
// token and the invitation id.
func inviteInto(t *testing.T, c *client, hdr map[string]string, project, role string) (r response, token, id string) {
	t.Helper()
	r = c.do(http.MethodPost, "/api/v1/invitations", `{"role":"`+role+`","projectId":"`+project+`"}`, hdr)
	if r.code == http.StatusCreated {
		link, _ := r.body["inviteUrl"].(string)
		_, token, _ = strings.Cut(link, "#")
		id, _ = r.body["invitation"].(map[string]any)["id"].(string)
	}
	return r, token, id
}

func memberIDs(t *testing.T, c *client, path string, hdr map[string]string) (ids []string, r response) {
	t.Helper()
	r = c.do(http.MethodGet, path, "", hdr)
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		ids = append(ids, it.(map[string]any)["userId"].(string))
	}
	sort.Strings(ids)
	return ids, r
}

// TestIntegrationProjectInvitationPermissions: who may invite into which project with which role,
// who sees and revokes which invitation.
func TestIntegrationProjectInvitationPermissions(t *testing.T) {
	w := newProjectWorld(t, "projinvperm")
	c := w.c
	owner := w.hdr("owner")

	type try struct {
		who, project, role string
		want               int
	}
	for _, tc := range []try{
		// A maintainer of alpha only: all roles up to its own, nothing else.
		{"aMaint", w.alpha, "viewer", http.StatusCreated},
		{"aMaint", w.alpha, "member", http.StatusCreated},
		{"aMaint", w.alpha, "maintainer", http.StatusCreated},
		{"aMaint", w.alpha, "owner", http.StatusForbidden},
		{"aMaint", w.beta, "viewer", http.StatusNotFound},
		{"aMaint", w.gamma, "viewer", http.StatusNotFound},
		{"aMaint", w.def, "viewer", http.StatusNotFound},
		{"aMaint", "0192b9c4-7c5e-7000-8000-0000000000ee", "viewer", http.StatusNotFound},
		// An owner of alpha only.
		{"aOwner", w.alpha, "owner", http.StatusCreated},
		{"aOwner", w.beta, "viewer", http.StatusNotFound},
		// Members and viewers of a project cannot invite.
		{"aMember", w.alpha, "viewer", http.StatusForbidden},
		{"aViewer", w.alpha, "viewer", http.StatusForbidden},
		{"bMember", w.beta, "viewer", http.StatusForbidden},
		{"none", w.alpha, "viewer", http.StatusForbidden},
		// An organization viewer who maintains alpha: alpha only; the other projects are visible to
		// it through its organization role but not its to manage.
		{"raised", w.alpha, "member", http.StatusCreated},
		{"raised", w.beta, "member", http.StatusForbidden},
		// An organization member who owns beta.
		{"mixOwner", w.beta, "owner", http.StatusCreated},
		{"mixOwner", w.alpha, "viewer", http.StatusForbidden},
		// A person with a plain role in two projects.
		{"pmix", w.beta, "viewer", http.StatusForbidden},
		// Organization roles: a maintainer works in every project but grants no owner; an owner may.
		{"maintainer", w.beta, "maintainer", http.StatusCreated},
		{"maintainer", w.gamma, "owner", http.StatusForbidden},
		{"owner", w.gamma, "owner", http.StatusCreated},
		{"member", w.alpha, "viewer", http.StatusForbidden},
		{"viewer", w.alpha, "viewer", http.StatusForbidden},
	} {
		if r, _, _ := inviteInto(t, c, w.hdr(tc.who), tc.project, tc.role); r.code != tc.want {
			t.Errorf("%s invites a %s into %s: %d %s, want %d", tc.who, tc.role, w.projectNames[tc.project], r.code, r.raw, tc.want)
		}
	}

	// Organization invitations stay with organization owners and maintainers.
	for who, want := range map[string]int{"aMaint": http.StatusForbidden, "aOwner": http.StatusForbidden, "raised": http.StatusForbidden,
		"mixOwner": http.StatusForbidden, "aMember": http.StatusForbidden, "member": http.StatusForbidden, "maintainer": http.StatusCreated} {
		if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, w.hdr(who)); r.code != want {
			t.Errorf("%s creates an organization invitation: %d %s, want %d", who, r.code, r.raw, want)
		}
	}
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"owner"}`, owner); r.code != http.StatusBadRequest {
		t.Errorf("organization invitation for owner: %d %s", r.code, r.raw)
	}

	// The invitation says which project it is for.
	r, _, _ := inviteInto(t, c, w.hdr("aMaint"), w.alpha, "viewer")
	inv, _ := r.body["invitation"].(map[string]any)
	if r.code != http.StatusCreated || inv["projectId"] != w.alpha || inv["projectName"] != "Alpha" || inv["role"] != "viewer" {
		t.Fatalf("project invitation: %d %s", r.code, r.raw)
	}

	// Lists: an organization owner or maintainer sees everything, a project owner or maintainer only
	// the invitations of their projects, everybody else is refused.
	projectsOf := func(hdr map[string]string, query string) (string, response) {
		r := c.do(http.MethodGet, "/api/v1/invitations"+query, "", hdr)
		items, _ := r.body["items"].([]any)
		seen := map[string]bool{}
		for _, it := range items {
			p, _ := it.(map[string]any)["projectName"].(string)
			if p == "" {
				p = "-"
			}
			seen[p] = true
		}
		var out []string
		for p := range seen {
			out = append(out, p)
		}
		sort.Strings(out)
		return strings.Join(out, ","), r
	}
	for who, want := range map[string]string{"owner": "-,Alpha,Beta,Gamma", "maintainer": "-,Alpha,Beta,Gamma", "aMaint": "Alpha", "aOwner": "Alpha",
		"raised": "Alpha", "mixOwner": "Beta"} {
		if got, r := projectsOf(w.hdr(who), ""); r.code != http.StatusOK || got != want {
			t.Errorf("%s lists %q (%d), want %q", who, got, r.code, want)
		}
	}
	if got, r := projectsOf(owner, "?projectId="+w.beta); r.code != http.StatusOK || got != "Beta" {
		t.Errorf("filter by project: %q %d %s", got, r.code, r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/invitations?projectId="+w.beta, "", w.hdr("aMaint")); r.code != http.StatusNotFound {
		t.Errorf("filter by a project the caller has no role in: %d %s", r.code, r.raw)
	}
	for _, who := range []string{"aMember", "aViewer", "member", "viewer", "none", "bMember"} {
		if r := c.do(http.MethodGet, "/api/v1/invitations", "", w.hdr(who)); r.code != http.StatusForbidden {
			t.Errorf("%s lists invitations: %d %s", who, r.code, r.raw)
		}
	}

	// Revoking follows the same scope.
	_, _, alphaOwnerInv := inviteInto(t, c, w.hdr("aOwner"), w.alpha, "owner")
	_, _, alphaViewerInv := inviteInto(t, c, w.hdr("aOwner"), w.alpha, "viewer")
	_, _, betaInv := inviteInto(t, c, owner, w.beta, "viewer")
	_, orgInv := invite(t, c, owner, "viewer")
	revoke := func(who, id string) int {
		return c.do(http.MethodPost, "/api/v1/invitations/"+id+"/revoke", "", w.hdr(who)).code
	}
	for _, tc := range []struct {
		who, id string
		want    int
	}{
		{"aMaint", betaInv, http.StatusNotFound},
		{"aMaint", orgInv, http.StatusNotFound},
		{"aMaint", alphaOwnerInv, http.StatusForbidden},
		{"aMember", alphaViewerInv, http.StatusForbidden},
		{"mixOwner", alphaViewerInv, http.StatusNotFound},
		{"aMaint", alphaViewerInv, http.StatusNoContent},
		{"aMaint", alphaViewerInv, http.StatusNoContent},
		{"maintainer", betaInv, http.StatusNoContent},
		{"maintainer", orgInv, http.StatusNoContent},
		{"aOwner", alphaOwnerInv, http.StatusNoContent},
	} {
		if got := revoke(tc.who, tc.id); got != tc.want {
			t.Errorf("%s revokes %s: %d, want %d", tc.who, tc.id, got, tc.want)
		}
	}

	// Audit rows carry the project.
	if n := countRows(t, w.env, `SELECT count(*) FROM audit_log WHERE action = 'member.invited' AND metadata->>'projectId' = $1`, w.alpha); n == 0 {
		t.Error("no member.invited row with the project")
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM audit_log WHERE action = 'invitation.revoked' AND metadata->>'projectId' = $1`, w.beta); n != 1 {
		t.Errorf("invitation.revoked rows with the project: %d", n)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM audit_log WHERE action = 'invitation.revoked' AND entity_id = $1 AND metadata ? 'projectId'`, orgInv); n != 0 {
		t.Errorf("an organization invitation carries a project: %d", n)
	}
}

// TestIntegrationProjectInvitationAcceptance: the ways to accept an invitation into a project.
func TestIntegrationProjectInvitationAcceptance(t *testing.T) {
	w := newProjectWorld(t, "projinvaccept")
	c := w.c
	owner := w.hdr("owner")
	lookup := func(token string) response {
		return c.do(http.MethodPost, "/api/v1/invitations/lookup", `{"token":"`+token+`"}`, nil)
	}
	register := func(email, token string) response {
		b, _ := json.Marshal(map[string]string{"email": email, "password": ownerPassword, "inviteToken": token})
		return c.do(http.MethodPost, "/api/v1/auth/register", string(b), map[string]string{"X-Forwarded-For": "198.51.100.40"})
	}
	accept := func(hdr map[string]string, token string) response {
		return c.do(http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+token+`"}`, hdr)
	}

	// The link tells the project and the project role.
	_, token, _ := inviteInto(t, c, owner, w.gamma, "maintainer")
	if lk := lookup(token); lk.code != http.StatusOK || lk.body["projectName"] != "Gamma" || lk.body["role"] != "maintainer" || lk.body["organizationName"] == "" {
		t.Fatalf("lookup: %d %s", lk.code, lk.raw)
	}
	orgToken, _ := invite(t, c, owner, "viewer")
	if lk := lookup(orgToken); lk.code != http.StatusOK || lk.body["projectName"] != nil {
		t.Fatalf("organization lookup: %d %s", lk.code, lk.raw)
	}

	// A new person registers through the link: no organization role, the project role.
	reg := register("newcomer@example.com", token)
	if reg.code != http.StatusCreated || reg.body["role"] != nil {
		t.Fatalf("register through a project link: %d %s", reg.code, reg.raw)
	}
	cookie, csrf := session(t, reg)
	newcomer := withCSRF(cookie, csrf)
	newID := userIDOf(t, c, newcomer)
	if got := w.names(t, "/api/v1/projects", newcomer, "id", w.projectNames); got != "Gamma" {
		t.Fatalf("projects of the newcomer: %s", got)
	}
	r := c.do(http.MethodGet, "/api/v1/projects/"+w.gamma+"/members", "", owner)
	found := false
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if m["userId"] == newID {
			found = m["organizationRole"] == nil && m["projectRole"] == "maintainer" && m["effectiveRole"] == "maintainer"
		}
	}
	if !found {
		t.Fatalf("newcomer in gamma: %s", r.raw)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM memberships WHERE user_id = $1 AND role IS NULL`, newID); n != 1 {
		t.Fatalf("memberships of the newcomer with no role: %d", n)
	}
	// The newcomer maintains gamma: monitors there, nothing outside.
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+w.gamma+`"}`, newcomer); r.code != http.StatusCreated {
		t.Fatalf("newcomer creates a monitor in gamma: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+w.alpha+`"}`, newcomer); r.code == http.StatusCreated {
		t.Fatalf("newcomer creates a monitor in alpha: %d %s", r.code, r.raw)
	}
	if lk := lookup(token); lk.code != http.StatusNotFound {
		t.Fatalf("a used link still opens: %d", lk.code)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM audit_log WHERE action = 'member.joined' AND entity_id = $1 AND metadata->>'projectId' = $2 AND metadata->>'role' = 'maintainer'`, newID, w.gamma); n != 1 {
		t.Fatalf("member.joined rows with the project: %d", n)
	}

	// An existing member of the organization gets only the project row and keeps the organization role.
	viewer := w.hdr("viewer")
	viewerID := w.userIDs[authz.Viewer]
	_, token, _ = inviteInto(t, c, owner, w.alpha, "member")
	acc := accept(viewer, token)
	if acc.code != http.StatusOK || acc.body["role"] != "viewer" {
		t.Fatalf("existing member accepts: %d %s", acc.code, acc.raw)
	}
	cookie, csrf = session(t, acc)
	viewer = withCSRF(cookie, csrf)
	if n := countRows(t, w.env, `SELECT count(*) FROM project_members WHERE user_id = $1 AND project_id = $2 AND role = 'member'`, viewerID, w.alpha); n != 1 {
		t.Fatalf("project row of the existing member: %d", n)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM memberships WHERE user_id = $1 AND role = 'viewer'`, viewerID); n != 1 {
		t.Fatalf("organization role of the existing member changed: %d", n)
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com/x"},"projectId":"`+w.alpha+`"}`, viewer); r.code != http.StatusCreated {
		t.Fatalf("raised viewer creates in alpha: %d %s", r.code, r.raw)
	}

	// An equal or higher role there already: nothing changes, the invitation is used up.
	_, token, id := inviteInto(t, c, owner, w.alpha, "viewer")
	acc = accept(viewer, token)
	if acc.code != http.StatusOK {
		t.Fatalf("accept a lower role: %d %s", acc.code, acc.raw)
	}
	cookie, csrf = session(t, acc)
	viewer = withCSRF(cookie, csrf)
	if n := countRows(t, w.env, `SELECT count(*) FROM project_members WHERE user_id = $1 AND project_id = $2 AND role = 'member'`, viewerID, w.alpha); n != 1 {
		t.Fatalf("a lower invitation changed the role: %d", n)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM invitations WHERE id = $1 AND accepted_by = $2`, id, viewerID); n != 1 {
		t.Fatalf("invitation not consumed: %d", n)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM audit_log WHERE action = 'member.joined' AND entity_id = $1 AND metadata->>'granted' = 'false'`, viewerID); n != 1 {
		t.Fatalf("member.joined without a grant: %d", n)
	}
	// An organization member is already viewer in beta through its role: no project row.
	_, token, _ = inviteInto(t, c, owner, w.beta, "viewer")
	if acc := accept(w.hdr("member"), token); acc.code != http.StatusOK || acc.body["role"] != "member" {
		t.Fatalf("organization member accepts a lower project role: %d %s", acc.code, acc.raw)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM project_members WHERE user_id = $1 AND project_id = $2`, w.userIDs[authz.Member], w.beta); n != 0 {
		t.Fatalf("a project row below the organization role: %d", n)
	}

	// A project-only person (no organization role) takes a role in another project.
	_, token, _ = inviteInto(t, c, owner, w.beta, "maintainer")
	if acc := accept(w.hdr("aMember"), token); acc.code != http.StatusOK || acc.body["role"] != nil {
		t.Fatalf("project-only person accepts: %d %s", acc.code, acc.raw)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM project_members WHERE user_id = $1 AND project_id = $2 AND role = 'maintainer'`, w.people["aMember"].id, w.beta); n != 1 {
		t.Fatalf("project row: %d", n)
	}

	// A user of another organization joins with an empty organization role plus the project role.
	otherCookie, otherCSRF := otherTenantSession(t, w.env)
	other := withCSRF(otherCookie, otherCSRF)
	_, token, _ = inviteInto(t, c, owner, w.gamma, "viewer")
	acc = accept(other, token)
	if acc.code != http.StatusOK || acc.body["role"] != nil || acc.body["organization"].(map[string]any)["slug"] != "default" {
		t.Fatalf("user of another organization accepts: %d %s", acc.code, acc.raw)
	}
	cookie, csrf = session(t, acc)
	other = withCSRF(cookie, csrf)
	if got := w.names(t, "/api/v1/projects", other, "id", w.projectNames); got != "Gamma" {
		t.Fatalf("projects of the joined user: %s", got)
	}
	if n := countRows(t, w.env, `SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id WHERE u.email = 'other@example.com'`); n != 2 {
		t.Fatalf("memberships of the joined user: %d", n)
	}

	// An organization invitation is still refused for somebody who belongs.
	again, _ := invite(t, c, owner, "viewer")
	if r := accept(viewer, again); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:already-member" {
		t.Fatalf("organization invitation for a member: %d %s", r.code, r.raw)
	}

	// Revoked, expired and deleted-project invitations are dead for lookup, registration and accept.
	_, revoked, revokedID := inviteInto(t, c, owner, w.alpha, "viewer")
	if r := c.do(http.MethodPost, "/api/v1/invitations/"+revokedID+"/revoke", "", owner); r.code != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.code)
	}
	_, expired, expiredID := inviteInto(t, c, owner, w.alpha, "viewer")
	pgtest.MustExec(t, w.env.AdminConn(t), `UPDATE invitations SET created_at = now() - interval '8 days', expires_at = now() - interval '1 day' WHERE id = '`+expiredID+`'`)
	tmp := c.do(http.MethodPost, "/api/v1/projects", `{"name":"Temporary"}`, owner)
	tmpID := tmp.body["id"].(string)
	_, gone, goneID := inviteInto(t, c, owner, tmpID, "member")
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+tmpID, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("delete project: %d %s", r.code, r.raw)
	}
	for name, tok := range map[string]string{"revoked": revoked, "expired": expired, "deleted project": gone} {
		if lk := lookup(tok); lk.code != http.StatusNotFound || lk.body["type"] != "urn:proreta:problem:invitation-invalid" {
			t.Errorf("%s lookup: %d %s", name, lk.code, lk.raw)
		}
		if r := accept(viewer, tok); r.code != http.StatusNotFound {
			t.Errorf("%s accept: %d %s", name, r.code, r.raw)
		}
		if r := register("late-"+strings.ReplaceAll(name, " ", "-")+"@example.com", tok); r.code != http.StatusNotFound {
			t.Errorf("%s register: %d %s", name, r.code, r.raw)
		}
	}
	list := c.do(http.MethodGet, "/api/v1/invitations", "", owner)
	seen := false
	for _, it := range list.body["items"].([]any) {
		if m := it.(map[string]any); m["id"] == goneID {
			seen = true
			if m["status"] != "revoked" {
				t.Errorf("invitation of a deleted project reads %v", m["status"])
			}
		}
	}
	if !seen {
		t.Error("invitation of a deleted project is not listed")
	}
}

// The system admin never joins another organization, with a project invitation either.
func TestIntegrationPlatformAdminCannotJoinProject(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("projinvadmin"))
	c := newRegistrationServer(t, env, membersOpts(), true, &lockedBuffer{})
	ownerCookie, ownerCSRF, _ := c.register("owner@example.com")
	ph := withCSRF(ownerCookie, ownerCSRF)
	pr := c.do(http.MethodPost, "/api/v1/projects", `{"name":"P"}`, ph)
	if pr.code != http.StatusCreated {
		t.Fatalf("project: %d %s", pr.code, pr.raw)
	}
	_, token, _ := inviteInto(t, c, ph, pr.body["id"].(string), "viewer")
	r := c.do(http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+token+`"}`, adminSession(c))
	if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:already-member" {
		t.Fatalf("platform admin accepts a project invitation: %d %s", r.code, r.raw)
	}
}

// TestIntegrationProjectInvitationQuota: an open project invitation takes a place like any other,
// and a project is not a way around the member limit.
func TestIntegrationProjectInvitationQuota(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("projinvquota"))
	c := newRegistrationServer(t, env, membersOpts(), true, &lockedBuffer{})
	oCookie, oCSRF, _ := c.register("owner@example.com")
	owner := withCSRF(oCookie, oCSRF)
	pr := c.do(http.MethodPost, "/api/v1/projects", `{"name":"P"}`, owner)
	project := pr.body["id"].(string)

	_, t1, _ := inviteInto(t, c, owner, project, "member")
	_, _, id2 := inviteInto(t, c, owner, project, "viewer")
	r, _, _ := inviteInto(t, c, owner, project, "viewer")
	if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached" || r.body["limit"] != "members" {
		t.Fatalf("over the limit: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, owner); r.code != http.StatusConflict {
		t.Fatalf("organization invitation over the limit: %d %s", r.code, r.raw)
	}
	if u := c.do(http.MethodGet, "/api/v1/usage", "", owner); !strings.Contains(u.raw, `"members":3`) {
		t.Fatalf("usage: %s", u.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/invitations/"+id2+"/revoke", "", owner); r.code != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.code)
	}
	if r, _, _ := inviteInto(t, c, owner, project, "viewer"); r.code != http.StatusCreated {
		t.Fatalf("after the revoke: %d %s", r.code, r.raw)
	}
	// Accepting keeps the count: the place the invitation held becomes the member.
	b, _ := json.Marshal(map[string]string{"email": "guest@example.com", "password": ownerPassword, "inviteToken": t1})
	if r := c.do(http.MethodPost, "/api/v1/auth/register", string(b), nil); r.code != http.StatusCreated || r.body["role"] != nil {
		t.Fatalf("register at the limit: %d %s", r.code, r.raw)
	}
	if u := c.do(http.MethodGet, "/api/v1/usage", "", owner); !strings.Contains(u.raw, `"members":3`) {
		t.Fatalf("usage after the join: %s", u.raw)
	}
	// Deleting the project kills its open invitation, which frees the place.
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, owner); r.code != http.StatusConflict {
		t.Fatalf("still at the limit: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+project, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("delete project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"viewer"}`, owner); r.code != http.StatusCreated {
		t.Fatalf("after the project is gone: %d %s", r.code, r.raw)
	}
}

// TestIntegrationProjectOnlyPeopleList: a person without an organization role sees only the people
// of their own projects, by name; organization roles see everybody as before.
func TestIntegrationProjectOnlyPeopleList(t *testing.T) {
	w := newProjectWorld(t, "projonlylist")
	c := w.c
	id := func(name string) string {
		if p, ok := w.people[name]; ok {
			return p.id
		}
		return w.userIDs[authz.Role(name)]
	}
	sorted := func(names []string) string {
		var out []string
		for _, n := range names {
			out = append(out, id(n))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	orgPeople := []string{"owner", "maintainer", "member", "viewer", "raised", "mixOwner"}
	inAlpha := append([]string{"aOwner", "aMaint", "aMember", "aViewer", "pmix"}, orgPeople...)
	for who, expected := range map[string][]string{
		"aMember": inAlpha,
		"aViewer": inAlpha,
		"aMaint":  inAlpha,
		"bMember": append([]string{"bMember", "pmix"}, orgPeople...),
		"pmix":    append([]string{"bMember", "aOwner", "aMaint", "aMember", "aViewer", "pmix"}, orgPeople...),
	} {
		got, r := memberIDs(t, c, "/api/v1/members", w.hdr(who))
		if r.code != http.StatusOK || strings.Join(got, ",") != sorted(expected) {
			t.Errorf("%s lists %d people (%d), want %d: %s", who, len(got), r.code, len(expected), r.raw)
		}
		for _, it := range r.body["items"].([]any) {
			if it.(map[string]any)["login"] != nil {
				t.Errorf("%s sees a login: %s", who, r.raw)
				break
			}
		}
	}
	// Nobody outside their projects shows up, by name either.
	r := c.do(http.MethodGet, "/api/v1/members", "", w.hdr("bMember"))
	for _, outsider := range []string{"p-amember", "p-tgt", "p-none"} {
		if strings.Contains(strings.ToLower(r.raw), outsider) {
			t.Errorf("bMember sees %s", outsider)
		}
	}
	// No role and no project: nothing to list.
	if r := c.do(http.MethodGet, "/api/v1/members", "", w.hdr("none")); r.code != http.StatusForbidden {
		t.Errorf("a person with no role and no project lists members: %d %s", r.code, r.raw)
	}
	// People with an organization role see everybody; logins only for owners and maintainers.
	for who, logins := range map[string]bool{"owner": true, "maintainer": true, "member": false, "viewer": false, "raised": false} {
		got, r := memberIDs(t, c, "/api/v1/members", w.hdr(who))
		shown := r.body["items"].([]any)[0].(map[string]any)["login"] != nil
		if r.code != http.StatusOK || len(got) != 14 || shown != logins {
			t.Errorf("%s lists %d people, logins shown %v: %d", who, len(got), shown, r.code)
		}
	}
	// A project-only maintainer sees no address in the project's list either: they invite by link.
	pm := c.do(http.MethodGet, "/api/v1/projects/"+w.alpha+"/members", "", w.hdr("aMaint"))
	if pm.code != http.StatusOK || strings.Contains(pm.raw, "@example.com") {
		t.Errorf("project-only maintainer sees addresses: %d %s", pm.code, pm.raw)
	}
}
