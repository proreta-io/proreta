package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	unknownOrg  = "0192b9c4-7c5e-7000-8000-000000000999"
	unknownUser = "0192b9c4-7c5e-7000-8000-000000000998"
)

func TestIntegrationAdminMembers(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiadminmembers"))
	logs := &lockedBuffer{}
	c := newAdminServer(t, env, logs)
	adminCookie, adminCSRF := c.login()
	ah := withCSRF(adminCookie, adminCSRF)

	aliceCookie, aliceCSRF, regA := c.register("alice@alpha.example")
	alice := withCSRF(aliceCookie, aliceCSRF)
	orgA := regA.body["organization"].(map[string]any)["id"].(string)
	aliceID := regA.body["user"].(map[string]any)["id"].(string)
	bobCookie, bobCSRF, regB := c.register("bob@beta.example")
	bob := withCSRF(bobCookie, bobCSRF)
	orgB := regB.body["organization"].(map[string]any)["id"].(string)
	bobID := regB.body["user"].(map[string]any)["id"].(string)
	_, _, regC := c.register("carol@gamma.example")
	carolID := regC.body["user"].(map[string]any)["id"].(string)
	_, _, regD := c.register("dave@delta.example")
	daveID := regD.body["user"].(map[string]any)["id"].(string)

	var adminID, defaultOrg string
	if err := env.AdminConn(t).QueryRow(t.Context(), `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := env.AdminConn(t).QueryRow(t.Context(), `SELECT id::text FROM tenants WHERE slug = 'default'`).Scan(&defaultOrg); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/admin/organizations/"
	audits := func(action string) int {
		return countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = $1`, action)
	}
	role := func(org, user string) string {
		var r string
		_ = env.AdminConn(t).QueryRow(t.Context(), `SELECT role FROM memberships WHERE tenant_id = $1 AND user_id = $2`, org, user).Scan(&r)
		return r
	}

	t.Run("list shows logins, status and role", func(t *testing.T) {
		r := c.do(http.MethodGet, base+orgA+"/members", "", ah)
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 {
			t.Fatalf("list: %d %s", r.code, r.raw)
		}
		m := items[0].(map[string]any)
		if m["login"] != "alice@alpha.example" || m["role"] != "owner" || m["status"] != "active" || m["userId"] != aliceID || m["isPlatformAdmin"] != false || m["joinedAt"] == nil {
			t.Fatalf("member: %v", m)
		}
		if r := c.do(http.MethodGet, base+unknownOrg+"/members", "", ah); r.code != http.StatusNotFound {
			t.Fatalf("unknown organization: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, base+"nope/members", "", ah); r.code != http.StatusBadRequest {
			t.Fatalf("bad organization id: %d", r.code)
		}
	})

	t.Run("add an existing user, change the role, idempotence", func(t *testing.T) {
		r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"viewer"}`, ah)
		if r.code != http.StatusOK || r.body["role"] != "viewer" || r.body["login"] != "bob@beta.example" {
			t.Fatalf("add: %d %s", r.code, r.raw)
		}
		if audits("admin.member.added") != 1 {
			t.Fatalf("added audit rows: %d", audits("admin.member.added"))
		}
		var actor, entity, ip string
		var meta string
		if err := env.AdminConn(t).QueryRow(t.Context(), `SELECT actor_user_id::text, entity_type || ':' || entity_id, COALESCE(host(ip), ''), metadata::text
			FROM audit_log WHERE action = 'admin.member.added' AND tenant_id = $1`, orgA).Scan(&actor, &entity, &ip, &meta); err != nil {
			t.Fatal(err)
		}
		if actor != adminID || entity != "user:"+bobID || ip == "" || meta != `{"role": "viewer"}` {
			t.Fatalf("audit row: actor=%s entity=%s ip=%q meta=%s", actor, entity, ip, meta)
		}

		// Same role again: nothing changes, nothing is audited.
		if r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"viewer"}`, ah); r.code != http.StatusOK || r.body["role"] != "viewer" {
			t.Fatalf("repeat: %d %s", r.code, r.raw)
		}
		if audits("admin.member.added")+audits("admin.member.role_changed") != 1 {
			t.Fatal("a repeated role wrote an audit row")
		}

		// Owner is allowed here, and a role change is audited with from and to.
		r = c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"owner"}`, ah)
		if r.code != http.StatusOK || r.body["role"] != "owner" {
			t.Fatalf("promote: %d %s", r.code, r.raw)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.member.role_changed' AND tenant_id = $1
			AND metadata = '{"from":"viewer","to":"owner"}' AND entity_id = $2`, orgA, bobID); n != 1 {
			t.Fatalf("role_changed audit rows: %d", n)
		}
		if r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"boss"}`, ah); r.code != http.StatusBadRequest {
			t.Fatalf("unknown role: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPut, base+orgA+"/members/"+unknownUser, `{"role":"member"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("unknown user: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPut, base+unknownOrg+"/members/"+bobID, `{"role":"member"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("unknown organization: %d %s", r.code, r.raw)
		}
	})

	t.Run("an added member can use the organization", func(t *testing.T) {
		orgs := c.do(http.MethodGet, "/api/v1/auth/organizations", "", bob)
		if items, _ := orgs.body["items"].([]any); orgs.code != http.StatusOK || len(items) != 2 {
			t.Fatalf("organizations: %d %s", orgs.code, orgs.raw)
		}
		sw := c.do(http.MethodPost, "/api/v1/auth/switch-organization", `{"organizationId":"`+orgA+`"}`, bob)
		if sw.code != http.StatusOK || sw.body["role"] != "owner" || sw.body["organization"].(map[string]any)["id"] != orgA {
			t.Fatalf("switch: %d %s", sw.code, sw.raw)
		}
		cookie, csrf := session(t, sw)
		bob = withCSRF(cookie, csrf)
		if r := c.do(http.MethodGet, "/api/v1/members", "", bob); r.code != http.StatusOK {
			t.Fatalf("members as bob in A: %d %s", r.code, r.raw)
		}
		// A role change applies on the next request.
		if r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"viewer"}`, ah); r.code != http.StatusOK {
			t.Fatalf("demote: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/members", "", bob); r.code != http.StatusOK {
			t.Fatalf("viewer lists members: %d", r.code)
		}
		if r := c.do(http.MethodPost, "/api/v1/invitations", `{"role":"member"}`, bob); r.code != http.StatusForbidden {
			t.Fatalf("viewer invites: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"owner"}`, ah); r.code != http.StatusOK {
			t.Fatalf("promote again: %d %s", r.code, r.raw)
		}
	})

	t.Run("last owner", func(t *testing.T) {
		for _, step := range []struct{ method, path, body string }{
			{http.MethodPut, aliceID, `{"role":"maintainer"}`}, // bob is an owner too
		} {
			if r := c.do(step.method, base+orgA+"/members/"+step.path, step.body, ah); r.code != http.StatusOK {
				t.Fatalf("demote alice: %d %s", r.code, r.raw)
			}
		}
		for _, step := range []struct{ method, body string }{{http.MethodPut, `{"role":"member"}`}, {http.MethodDelete, ""}} {
			r := c.do(step.method, base+orgA+"/members/"+bobID, step.body, ah)
			if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:last-owner" {
				t.Fatalf("%s the last owner: %d %s", step.method, r.code, r.raw)
			}
		}
		if role(orgA, bobID) != "owner" {
			t.Fatal("the last owner lost the role")
		}
		if r := c.do(http.MethodPut, base+orgA+"/members/"+aliceID, `{"role":"owner"}`, ah); r.code != http.StatusOK {
			t.Fatalf("restore alice: %d %s", r.code, r.raw)
		}
	})

	t.Run("two concurrent demotions keep one owner", func(t *testing.T) {
		for range 4 {
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i, id := range []string{aliceID, bobID} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes[i] = c.do(http.MethodPut, base+orgA+"/members/"+id, `{"role":"maintainer"}`, ah).code
				}()
			}
			wg.Wait()
			if n := countRows(t, env, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND role = 'owner'`, orgA); n != 1 {
				t.Fatalf("owners after concurrent demotion: %d (codes %v)", n, codes)
			}
			pgtest.MustExec(t, env.AdminConn(t), `UPDATE memberships SET role = 'owner' WHERE tenant_id = '`+orgA+`'`)
		}
	})

	t.Run("system admin", func(t *testing.T) {
		for _, step := range []struct{ method, org, body string }{
			{http.MethodPut, orgA, `{"role":"member"}`},       // never added to another organization
			{http.MethodPut, defaultOrg, `{"role":"member"}`}, // never changed in default
			{http.MethodDelete, defaultOrg, ""},               // never removed from default
		} {
			r := c.do(step.method, base+step.org+"/members/"+adminID, step.body, ah)
			if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:system-admin" {
				t.Errorf("%s %s: %d %s", step.method, step.org, r.code, r.raw)
			}
		}
		if n := countRows(t, env, `SELECT count(*) FROM memberships WHERE user_id = $1`, adminID); n != 1 {
			t.Fatalf("admin memberships: %d", n)
		}
		// Setting the role it already has is a no-op, like for any member.
		if r := c.do(http.MethodPut, base+defaultOrg+"/members/"+adminID, `{"role":"owner"}`, ah); r.code != http.StatusOK || r.body["isPlatformAdmin"] != true {
			t.Fatalf("unchanged role of the admin: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+orgA+"/members", `{"email":"admin","role":"member"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("admin by login: %d %s", r.code, r.raw)
		}
	})

	t.Run("add by email", func(t *testing.T) {
		r := c.do(http.MethodPost, base+orgA+"/members", `{"email":"nobody@nowhere.example","role":"member"}`, ah)
		if r.code != http.StatusNotFound || !strings.Contains(fmt.Sprint(r.body["detail"]), "Invite") {
			t.Fatalf("no account: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+orgA+"/members", `{"email":"not an address","role":"member"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("malformed address: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodPost, base+orgA+"/members", `{"email":" Carol@Gamma.example ","role":"member"}`, ah)
		if r.code != http.StatusOK || r.body["userId"] != carolID || r.body["role"] != "member" {
			t.Fatalf("add carol: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+unknownOrg+"/members", `{"email":"carol@gamma.example","role":"member"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("unknown organization: %d", r.code)
		}
		if r := c.do(http.MethodPost, base+orgA+"/members", `{"role":"member"}`, ah); r.code != http.StatusBadRequest {
			t.Fatalf("missing email: %d", r.code)
		}
	})

	t.Run("plan limit", func(t *testing.T) {
		// Free: three members. alice, bob and carol fill it; an open invitation also holds a place.
		r := c.do(http.MethodPut, base+orgA+"/members/"+daveID, `{"role":"member"}`, ah)
		if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached" {
			t.Fatalf("add over the limit: %d %s", r.code, r.raw)
		}
		if role(orgA, daveID) != "" {
			t.Fatal("dave was added over the limit")
		}
		// A role change of a member needs no free place.
		if r := c.do(http.MethodPut, base+orgA+"/members/"+carolID, `{"role":"viewer"}`, ah); r.code != http.StatusOK {
			t.Fatalf("role change at the limit: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodPost, base+orgA+"/invitations", `{"role":"member"}`, ah)
		if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached" {
			t.Fatalf("invite over the limit: %d %s", r.code, r.raw)
		}
	})

	t.Run("remove a member and their sessions", func(t *testing.T) {
		if r := c.do(http.MethodGet, "/api/v1/members", "", bob); r.code != http.StatusOK {
			t.Fatalf("bob before removal: %d", r.code)
		}
		if r := c.do(http.MethodDelete, base+orgA+"/members/"+bobID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("remove: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, "/api/v1/members", "", bob); r.code != http.StatusUnauthorized {
			t.Fatalf("session of a removed member: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, "/api/v1/auth/switch-organization", `{"organizationId":"`+orgA+`"}`, bob); r.code == http.StatusOK {
			t.Fatal("a removed member switched into the organization")
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.member.removed' AND tenant_id = $1 AND metadata = '{"role":"owner"}' AND entity_id = $2`, orgA, bobID); n != 1 {
			t.Fatalf("removed audit rows: %d", n)
		}
		// Another organization of the same user is untouched.
		if role(orgB, bobID) != "owner" {
			t.Fatal("the removal touched another organization")
		}
		if r := c.do(http.MethodDelete, base+orgA+"/members/"+bobID, "", ah); r.code != http.StatusNotFound {
			t.Fatalf("remove again: %d %s", r.code, r.raw)
		}
		// The place is free again.
		if r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"maintainer"}`, ah); r.code != http.StatusOK {
			t.Fatalf("re-add: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, base+orgA+"/members/"+bobID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("remove again: %d %s", r.code, r.raw)
		}
	})

	t.Run("a disabled user can be added but cannot sign in", func(t *testing.T) {
		pgtest.MustExec(t, env.AdminConn(t), `UPDATE users SET status = 'disabled' WHERE id = '`+bobID+`'`)
		r := c.do(http.MethodPut, base+orgA+"/members/"+bobID, `{"role":"member"}`, ah)
		if r.code != http.StatusOK || r.body["status"] != "disabled" {
			t.Fatalf("add disabled: %d %s", r.code, r.raw)
		}
		if l := c.do(http.MethodPost, "/api/v1/auth/login", `{"login":"bob@beta.example","password":"`+ownerPassword+`"}`, nil); l.code == http.StatusOK {
			t.Fatalf("a disabled user signed in: %s", l.raw)
		}
		if r := c.do(http.MethodDelete, base+orgA+"/members/"+bobID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("remove: %d", r.code)
		}
		pgtest.MustExec(t, env.AdminConn(t), `UPDATE users SET status = 'active' WHERE id = '`+bobID+`'`)
	})

	t.Run("invitations", func(t *testing.T) {
		// Make room: carol leaves.
		if r := c.do(http.MethodDelete, base+orgA+"/members/"+carolID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("remove carol: %d %s", r.code, r.raw)
		}
		r := c.do(http.MethodPost, base+orgA+"/invitations", `{"role":"member","label":"Eve"}`, ah)
		link, _ := r.body["inviteUrl"].(string)
		_, token, ok := strings.Cut(link, "#")
		inv, _ := r.body["invitation"].(map[string]any)
		if r.code != http.StatusCreated || !ok || len(token) != 43 || r.body["emailQueued"] != false || inv["role"] != "member" || inv["label"] != "Eve" || inv["status"] != "open" {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		invID := inv["id"].(string)
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'admin.invitation.created' AND tenant_id = $1 AND entity_type = 'invitation'
			AND entity_id = $2 AND actor_user_id = $3 AND metadata = '{"role":"member","emailed":false}'`, orgA, invID, adminID); n != 1 {
			t.Fatalf("created audit rows: %d", n)
		}
		if strings.Contains(logs.String(), token) {
			t.Fatal("the invitation token is in the logs")
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE metadata::text LIKE '%' || $1 || '%'`, token); n != 0 {
			t.Fatal("the invitation token is in an audit row")
		}

		l := c.do(http.MethodGet, base+orgA+"/invitations", "", ah)
		if items, _ := l.body["items"].([]any); l.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["id"] != invID || strings.Contains(l.raw, token) {
			t.Fatalf("list: %d %s", l.code, l.raw)
		}

		// It reserves a place and the link works.
		if r := c.do(http.MethodPut, base+orgA+"/members/"+carolID, `{"role":"member"}`, ah); r.code != http.StatusOK {
			t.Fatalf("add carol: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+orgA+"/invitations", `{"role":"member"}`, ah); r.code != http.StatusConflict {
			t.Fatalf("invite over the limit: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, base+orgA+"/members/"+carolID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("remove carol: %d", r.code)
		}

		for body, wantCode := range map[string]int{
			`{"role":"owner"}`:                                            http.StatusBadRequest,
			`{"role":"member","email":"x@example.org"}`:                   http.StatusUnprocessableEntity,
			`{"role":"member","sendEmail":true}`:                          http.StatusUnprocessableEntity,
			`{"role":"member","label":"` + strings.Repeat("x", 81) + `"}`: http.StatusBadRequest,
		} {
			if r := c.do(http.MethodPost, base+orgA+"/invitations", body, ah); r.code != wantCode {
				t.Errorf("%s: %d %s, want %d", body, r.code, r.raw, wantCode)
			}
		}
		if r := c.do(http.MethodPost, base+unknownOrg+"/invitations", `{"role":"member"}`, ah); r.code != http.StatusNotFound {
			t.Errorf("unknown organization: %d", r.code)
		}

		if r := c.do(http.MethodPost, base+orgA+"/invitations/"+invID+"/revoke", "", ah); r.code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base+orgA+"/invitations/"+invID+"/revoke", "", ah); r.code != http.StatusNoContent {
			t.Fatalf("revoke again: %d %s", r.code, r.raw)
		}
		if audits("admin.invitation.revoked") != 1 {
			t.Fatalf("revoked audit rows: %d", audits("admin.invitation.revoked"))
		}
		if r := c.do(http.MethodPost, base+orgA+"/invitations/"+unknownUser+"/revoke", "", ah); r.code != http.StatusNotFound {
			t.Fatalf("revoke unknown: %d", r.code)
		}
		if r := registerWithInvite(c, "eve@example.org", ownerPassword, token, "198.51.100.40"); r.code == http.StatusCreated {
			t.Fatal("a revoked link worked")
		}

		// A fresh link is accepted by a new account; accepting it makes the invitation accepted.
		r = c.do(http.MethodPost, base+orgA+"/invitations", `{"role":"viewer"}`, ah)
		_, token2, _ := strings.Cut(r.body["inviteUrl"].(string), "#")
		if reg := registerWithInvite(c, "eve@example.org", ownerPassword, token2, "198.51.100.41"); reg.code != http.StatusCreated || reg.body["role"] != "viewer" {
			t.Fatalf("register with the link: %d %s", reg.code, reg.raw)
		}
		l = c.do(http.MethodGet, base+orgA+"/invitations", "", ah)
		if !strings.Contains(l.raw, `"status":"accepted"`) || !strings.Contains(l.raw, `"acceptedBy":"eve@example.org"`) {
			t.Fatalf("accepted state: %s", l.raw)
		}
		accepted := r.body["invitation"].(map[string]any)["id"].(string)
		if r := c.do(http.MethodPost, base+orgA+"/invitations/"+accepted+"/revoke", "", ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:invitation-accepted" {
			t.Fatalf("revoke accepted: %d %s", r.code, r.raw)
		}
	})

	t.Run("a deleted organization cannot be changed", func(t *testing.T) {
		if r := c.do(http.MethodDelete, base+orgB, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("delete organization: %d %s", r.code, r.raw)
		}
		for _, step := range []struct{ method, path, body string }{
			{http.MethodPut, "/members/" + aliceID, `{"role":"member"}`},
			{http.MethodPost, "/members", `{"email":"alice@alpha.example","role":"member"}`},
			{http.MethodDelete, "/members/" + bobID, ""},
			{http.MethodPost, "/invitations", `{"role":"member"}`},
			{http.MethodPost, "/invitations/" + unknownUser + "/revoke", ""},
		} {
			r := c.do(step.method, base+orgB+step.path, step.body, ah)
			if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:organization-deleted" {
				t.Errorf("%s %s: %d %s", step.method, step.path, r.code, r.raw)
			}
		}
		r := c.do(http.MethodGet, base+orgB+"/members", "", ah)
		if items, _ := r.body["items"].([]any); r.code != http.StatusOK || len(items) != 1 {
			t.Fatalf("members of a deleted organization: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodGet, base+orgB+"/invitations", "", ah); r.code != http.StatusOK {
			t.Fatalf("invitations of a deleted organization: %d", r.code)
		}
	})

	t.Run("one audit row per change", func(t *testing.T) {
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action IN ('member.invited', 'member.role_changed', 'member.removed', 'invitation.revoked')`); n != 0 {
			t.Fatalf("%d customer-facing audit rows next to the admin ones", n)
		}
		if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action LIKE 'admin.member.%' OR action LIKE 'admin.invitation.%'`); n < 10 {
			t.Fatalf("only %d admin member audit rows", n)
		}
		// The organization's own users can neither see nor use any of it.
		if r := c.do(http.MethodGet, base+orgA+"/members", "", alice); r.code != http.StatusForbidden {
			t.Fatalf("owner on the admin operation: %d", r.code)
		}
	})
}
