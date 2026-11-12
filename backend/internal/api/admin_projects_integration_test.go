package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationAdminOrganizationProjects(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiadminprojects"))
	c := newAdminServer(t, env, &lockedBuffer{})
	ctx := context.Background()
	ah := func() map[string]string { cookie, csrf := c.login(); return withCSRF(cookie, csrf) }()

	ownerCookie, ownerCSRF, reg := c.register("owner@alpha.example")
	oh := withCSRF(ownerCookie, ownerCSRF)
	tenant := reg.body["organization"].(map[string]any)["id"].(string)
	_, _, regV := c.register("viewer@beta.example")
	viewer := regV.body["user"].(map[string]any)["id"].(string)
	_, _, regM := c.register("member@gamma.example")
	member := regM.body["user"].(map[string]any)["id"].(string)
	_, _, regO := c.register("outsider@delta.example")
	outsider := regO.body["user"].(map[string]any)["id"].(string)
	var adminID string
	if err := env.AdminConn(t).QueryRow(ctx, `SELECT id::text FROM users WHERE email = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, env.AdminConn(t), fmt.Sprintf(
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ('%[1]s', '%[2]s', 'viewer'), ('%[1]s', '%[3]s', 'member')`, tenant, viewer, member))

	base := "/api/v1/admin/organizations/" + tenant + "/projects"
	adminAudit := func(action string) int {
		return countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = $1 AND tenant_id = $2 AND actor_user_id = $3 AND entity_type = 'project'`, action, tenant, adminID)
	}
	customerAudit := func() int {
		return countRows(t, env, `SELECT count(*) FROM audit_log WHERE action LIKE 'project.%'`)
	}

	var defaultID string
	t.Run("list", func(t *testing.T) {
		r := c.do(http.MethodGet, base, "", ah)
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 {
			t.Fatalf("list: %d %s", r.code, r.raw)
		}
		d := items[0].(map[string]any)
		defaultID = d["id"].(string)
		if d["isDefault"] != true || d["allMembers"] != true || d["memberCount"] != float64(3) || d["monitorCount"] != float64(0) {
			t.Fatalf("default: %v", d)
		}
		if r := c.do(http.MethodGet, "/api/v1/admin/organizations/0192b9c4-7c5e-7000-8000-000000000999/projects", "", ah); r.code != http.StatusNotFound {
			t.Fatalf("unknown org: %d", r.code)
		}
	})

	var projectID string
	t.Run("create, rename, slug conflict", func(t *testing.T) {
		r := c.do(http.MethodPost, base, `{"name":"Payments"}`, ah)
		if r.code != http.StatusCreated || r.body["slug"] != "payments" || r.body["memberCount"] != float64(3) || r.body["isDefault"] != false {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		projectID = r.body["id"].(string)
		if r := c.do(http.MethodPost, base, `{"name":"Other","slug":"payments"}`, ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:slug-taken" {
			t.Fatalf("slug taken: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, base, `{"name":""}`, ah); r.code != http.StatusBadRequest && r.code != http.StatusUnprocessableEntity {
			t.Fatalf("empty name: %d", r.code)
		}
		r = c.do(http.MethodPatch, base+"/"+projectID, `{"name":"Billing","slug":"billing"}`, ah)
		if r.code != http.StatusOK || r.body["name"] != "Billing" || r.body["slug"] != "billing" {
			t.Fatalf("rename: %d %s", r.code, r.raw)
		}
		// Nothing changes, nothing is audited.
		c.do(http.MethodPatch, base+"/"+projectID, `{"name":"Billing"}`, ah)
		if r := c.do(http.MethodPatch, base+"/"+projectID, `{"slug":"default"}`, ah); r.code != http.StatusConflict {
			t.Fatalf("rename to a used slug: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPatch, base+"/0192b9c4-7c5e-7000-8000-000000000999", `{"name":"x"}`, ah); r.code != http.StatusNotFound {
			t.Fatalf("unknown project: %d", r.code)
		}
		// The organization's owner sees the same project through the customer-facing api.
		if r := c.do(http.MethodGet, "/api/v1/projects/"+projectID, "", oh); r.code != http.StatusOK || r.body["name"] != "Billing" {
			t.Fatalf("owner view: %d %s", r.code, r.raw)
		}
		if adminAudit("admin.project.created") != 1 || adminAudit("admin.project.updated") != 1 {
			t.Fatalf("audit created/updated = %d/%d", adminAudit("admin.project.created"), adminAudit("admin.project.updated"))
		}
		var meta string
		if err := env.AdminConn(t).QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'admin.project.updated'`).Scan(&meta); err != nil || !strings.Contains(meta, `"changed"`) || !strings.Contains(meta, `"name"`) || !strings.Contains(meta, `"slug"`) {
			t.Fatalf("updated metadata %q: %v", meta, err)
		}
	})

	t.Run("members", func(t *testing.T) {
		mp := base + "/" + projectID + "/members"
		r := c.do(http.MethodGet, mp, "", ah)
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 3 {
			t.Fatalf("initial members (everyone with an organization role): %d %s", r.code, r.raw)
		}
		// A direct role never lowers the organization role and raises it where it is higher.
		r = c.do(http.MethodPut, mp+"/"+member, `{"role":"viewer"}`, ah)
		if r.code != http.StatusOK || r.body["explicit"] != true || r.body["effectiveRole"] != "member" || r.body["projectRole"] != "viewer" {
			t.Fatalf("add: %d %s", r.code, r.raw)
		}
		// Saving the same thing again writes nothing.
		c.do(http.MethodPut, mp+"/"+member, `{"role":"viewer"}`, ah)
		r = c.do(http.MethodPut, mp+"/"+member, `{"role":"maintainer"}`, ah)
		if r.code != http.StatusOK || r.body["projectRole"] != "maintainer" || r.body["effectiveRole"] != "maintainer" || r.body["organizationRole"] != "member" {
			t.Fatalf("raise: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodPut, mp+"/"+viewer, `{"role":"owner"}`, ah)
		if r.code != http.StatusOK || r.body["effectiveRole"] != "owner" || r.body["allProjects"] != true {
			t.Fatalf("owner on a viewer: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodPut, mp+"/"+outsider, `{"role":"member"}`, ah)
		fe, _ := r.body["fieldErrors"].([]any)
		if r.code != http.StatusUnprocessableEntity || len(fe) == 0 || fe[0].(map[string]any)["field"] != "userId" {
			t.Fatalf("non-member: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPut, mp+"/"+member, `{"role":"admin"}`, ah); r.code != http.StatusBadRequest {
			t.Fatalf("bad role: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPut, mp+"/"+member, `{}`, ah); r.code != http.StatusBadRequest {
			t.Fatalf("missing role: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodGet, mp, "", ah)
		items, _ = r.body["items"].([]any)
		if len(items) != 3 {
			t.Fatalf("members after adds: %s", r.raw)
		}
		if r := c.do(http.MethodDelete, mp+"/"+outsider, "", ah); r.code != http.StatusNotFound {
			t.Fatalf("remove unlisted: %d", r.code)
		}
		if r := c.do(http.MethodDelete, mp+"/"+viewer, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("remove: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, mp+"/"+viewer, "", ah); r.code != http.StatusNotFound {
			t.Fatalf("remove the direct role twice: %d", r.code)
		}
		if r := c.do(http.MethodGet, base+"/0192b9c4-7c5e-7000-8000-000000000999/members", "", ah); r.code != http.StatusNotFound {
			t.Fatalf("members of unknown project: %d", r.code)
		}
		// member added, viewer added; member raised; viewer removed.
		if adminAudit("admin.project_member.added") != 2 || adminAudit("admin.project_member.updated") != 1 || adminAudit("admin.project_member.removed") != 1 {
			t.Fatalf("member audit = %d/%d/%d", adminAudit("admin.project_member.added"), adminAudit("admin.project_member.updated"), adminAudit("admin.project_member.removed"))
		}
		var meta string
		if err := env.AdminConn(t).QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'admin.project_member.updated'`).Scan(&meta); err != nil ||
			!strings.Contains(meta, `"from": "viewer"`) || !strings.Contains(meta, `"to": "maintainer"`) {
			t.Fatalf("updated metadata %q: %v", meta, err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if r := c.do(http.MethodDelete, base+"/"+defaultID, "", ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:default-project" {
			t.Fatalf("default: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"Site","projectId":"`+projectID+`","http":{"url":"https://example.com"}}`, oh); r.code != http.StatusCreated {
			t.Fatalf("monitor: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, base+"/"+projectID, "", ah); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:project-not-empty" {
			t.Fatalf("non-empty: %d %s", r.code, r.raw)
		}
		r := c.do(http.MethodGet, base, "", ah)
		for _, it := range r.body["items"].([]any) {
			if it.(map[string]any)["id"] == projectID && it.(map[string]any)["monitorCount"] != float64(1) {
				t.Fatalf("monitor count: %s", r.raw)
			}
		}
		pgtest.MustExec(t, env.AdminConn(t), `UPDATE monitors SET deleted_at = now() WHERE project_id = '`+projectID+`'`)
		if r := c.do(http.MethodDelete, base+"/"+projectID, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, base+"/"+projectID, "", ah); r.code != http.StatusNotFound {
			t.Fatalf("delete again: %d", r.code)
		}
		if adminAudit("admin.project.deleted") != 1 {
			t.Fatal("no deleted audit row")
		}
	})

	t.Run("cap", func(t *testing.T) {
		pgtest.MustExec(t, env.AdminConn(t), `UPDATE plans SET limits = jsonb_set(limits, '{max_projects}', '3') WHERE key = (SELECT plan_key FROM tenants WHERE id = '`+tenant+`')`)
		for i := 0; i < 5; i++ {
			r := c.do(http.MethodPost, base, fmt.Sprintf(`{"name":"P%d"}`, i), ah)
			if i < 2 && r.code != http.StatusCreated {
				t.Fatalf("create %d: %d %s", i, r.code, r.raw)
			}
			if i == 2 && (r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached") {
				t.Fatalf("cap: %d %s", r.code, r.raw)
			}
		}
	})

	t.Run("no customer audit rows and deleted organization", func(t *testing.T) {
		if n := customerAudit(); n != 0 {
			t.Fatalf("%d customer-facing project audit rows from admin calls", n)
		}
		if r := c.do(http.MethodDelete, "/api/v1/admin/organizations/"+tenant, "", ah); r.code != http.StatusNoContent {
			t.Fatalf("delete org: %d %s", r.code, r.raw)
		}
		for _, call := range []struct{ method, path, body string }{
			{http.MethodPost, base, `{"name":"Late"}`},
			{http.MethodPatch, base + "/" + defaultID, `{"name":"Late"}`},
			{http.MethodDelete, base + "/" + defaultID, ""},
			{http.MethodPut, base + "/" + defaultID + "/members/" + member, `{"role":"member"}`},
			{http.MethodDelete, base + "/" + defaultID + "/members/" + member, ""},
		} {
			r := c.do(call.method, call.path, call.body, ah)
			if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:organization-deleted" {
				t.Errorf("%s %s on a deleted organization: %d %s", call.method, call.path, r.code, r.raw)
			}
		}
		if r := c.do(http.MethodGet, base, "", ah); r.code != http.StatusOK {
			t.Fatalf("list of a deleted organization: %d", r.code)
		}
	})
}
