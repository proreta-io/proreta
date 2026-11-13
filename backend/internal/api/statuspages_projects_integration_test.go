package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// statusPages holds the ids of the status pages added to a project world: one per project and an
// organization-level one.
type statusPages struct {
	byProject map[string]string
	org       string
	names     map[string]string
}

var pageSlugs atomic.Int64

func nextSlug() string { return fmt.Sprintf("pg-test-%d", pageSlugs.Add(1)) }

// addStatusPages raises the plan limit and adds the pages; it is not part of newProjectWorld
// because other tests rely on the one-page default limit.
func (w *projectWorld) addStatusPages(t *testing.T) *statusPages {
	t.Helper()
	pgtest.MustExec(t, w.env.AdminConn(t), `UPDATE tenants SET limits = '{"max_status_pages": 50}' WHERE slug = 'default'`)
	sp := &statusPages{byProject: map[string]string{}, names: map[string]string{}}
	mk := func(key, project string) string {
		body := `{"slug":"` + nextSlug() + `","title":"page-` + key + `"`
		if project != "" {
			body += `,"projectId":"` + project + `"`
		}
		r := w.c.do(http.MethodPost, "/api/v1/status-pages", body+`}`, w.hdr("owner"))
		if r.code != http.StatusCreated {
			t.Fatalf("create page %s: %d %s", key, r.code, r.raw)
		}
		if project == "" && r.body["projectId"] != nil || project != "" && r.body["projectId"] != project {
			t.Fatalf("page %s project: %s", key, r.raw)
		}
		id := r.body["id"].(string)
		sp.names[id] = "page-" + key
		return id
	}
	for key, project := range map[string]string{"def": w.def, "alpha": w.alpha, "beta": w.beta, "gamma": w.gamma} {
		sp.byProject[key] = mk(key, project)
	}
	sp.org = mk("org", "")
	return sp
}

// statusPageOps are the operations on the status pages of a project, for the role matrix.
func statusPageOps(sp *statusPages) []roleOp {
	page := func(p roleProject) string {
		if p.key == "unknown" {
			return p.id
		}
		return sp.byProject[p.key]
	}
	op := func(name string, min authz.Role, invisible, ok int, method, path, body string) roleOp {
		return roleOp{name, min, invisible, ok, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{method, strings.NewReplacer("{p}", p.id, "{sp}", page(p)).Replace(path), body}, noCleanup
		}}
	}
	disposable := func(w *projectWorld, p roleProject) string {
		if p.key == "unknown" {
			return p.id
		}
		r := w.c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"disposable","projectId":"`+p.id+`"}`, w.hdr("owner"))
		id, _ := r.body["id"].(string)
		return id
	}
	return []roleOp{
		op("get status page", authz.Viewer, http.StatusNotFound, http.StatusOK, http.MethodGet, "/api/v1/status-pages/{sp}", ""),
		op("list status pages of project", authz.Viewer, http.StatusNotFound, http.StatusOK, http.MethodGet, "/api/v1/status-pages?projectId={p}", ""),
		op("update status page", authz.Maintainer, http.StatusNotFound, http.StatusOK, http.MethodPatch, "/api/v1/status-pages/{sp}", `{"description":""}`),
		op("clear status page password", authz.Maintainer, http.StatusNotFound, http.StatusNoContent, http.MethodDelete, "/api/v1/status-pages/{sp}/password", ""),
		op("list status page components", authz.Maintainer, http.StatusNotFound, http.StatusOK, http.MethodGet, "/api/v1/status-pages/{sp}/components", ""),
		op("put status page components", authz.Maintainer, http.StatusNotFound, http.StatusOK, http.MethodPut, "/api/v1/status-pages/{sp}/components", `{"items":[]}`),
		op("preview status page", authz.Maintainer, http.StatusNotFound, http.StatusOK, http.MethodGet, "/api/v1/status-pages/{sp}/preview", ""),
		{"create status page", authz.Maintainer, http.StatusUnprocessableEntity, http.StatusCreated, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			return roleCall{http.MethodPost, "/api/v1/status-pages", `{"slug":"` + nextSlug() + `","title":"disposable","projectId":"` + p.id + `"}`},
				func(r response) {
					if id, _ := r.body["id"].(string); id != "" {
						w.c.do(http.MethodDelete, "/api/v1/status-pages/"+id, "", w.hdr("owner"))
					}
				}
		}},
		{"delete status page", authz.Maintainer, http.StatusNotFound, http.StatusNoContent, func(w *projectWorld, p roleProject) (roleCall, func(response)) {
			id := disposable(w, p)
			return roleCall{http.MethodDelete, "/api/v1/status-pages/" + id, ""}, func(response) {
				w.c.do(http.MethodDelete, "/api/v1/status-pages/"+id, "", w.hdr("owner"))
			}
		}},
	}
}

// TestIntegrationStatusPageOrganizationLevel covers the pages without a project: organization
// owners and maintainers manage them, other organization roles read them, and people who only
// hold project roles neither see nor create them.
func TestIntegrationStatusPageOrganizationLevel(t *testing.T) {
	w := newProjectWorld(t, "statuspageorglevel")
	sp := w.addStatusPages(t)
	page := "/api/v1/status-pages/" + sp.org
	for _, a := range roleActors {
		hdr := w.hdr(a.name)
		list := w.c.do(http.MethodGet, "/api/v1/status-pages", "", hdr)
		get := w.c.do(http.MethodGet, page, "", hdr)
		patch := w.c.do(http.MethodPatch, page, `{"description":""}`, hdr)
		comps := w.c.do(http.MethodGet, page+"/components", "", hdr)
		create := w.c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"org page"}`, hdr)
		if create.code == http.StatusCreated {
			w.c.do(http.MethodDelete, "/api/v1/status-pages/"+create.body["id"].(string), "", w.hdr("owner"))
		}

		// The table lets an operation through when the permission is held anywhere (a project
		// maintainer passes for a page it cannot see); the service then judges the page.
		passes := a.top().AtLeast(authz.Maintainer)
		wantList, wantGet, wantPatch, wantCreate := http.StatusOK, http.StatusOK, http.StatusOK, http.StatusForbidden
		switch {
		case a.top() == "":
			wantList, wantGet = http.StatusForbidden, http.StatusForbidden
		case a.org == "":
			wantGet = http.StatusNotFound
		}
		switch {
		case !passes:
			wantPatch = http.StatusForbidden
		case a.org == "":
			wantPatch = http.StatusNotFound
		case !a.org.AtLeast(authz.Maintainer):
			wantPatch = http.StatusForbidden
		}
		if a.org.AtLeast(authz.Maintainer) {
			wantCreate = http.StatusCreated
		}
		wantComps := wantPatch
		for _, c := range []struct {
			what      string
			got, want int
		}{{"list", list.code, wantList}, {"get", get.code, wantGet}, {"patch", patch.code, wantPatch}, {"components", comps.code, wantComps}, {"create", create.code, wantCreate}} {
			if c.got != c.want {
				t.Errorf("%s: %s of the organization-level page: %d, want %d", a.name, c.what, c.got, c.want)
			}
		}
		// The list shows the organization-level page exactly to the people with an organization role.
		if list.code == http.StatusOK {
			has := strings.Contains(list.raw, sp.org)
			if has != (a.org != "") {
				t.Errorf("%s: list shows the organization-level page = %v", a.name, has)
			}
		}
	}
}

// TestIntegrationStatusPageVisibility lists the pages every person sees.
func TestIntegrationStatusPageVisibility(t *testing.T) {
	w := newProjectWorld(t, "statuspagevisibility")
	sp := w.addStatusPages(t)
	all := "page-alpha,page-beta,page-def,page-gamma,page-org"
	for name, want := range map[string]string{
		"owner": all, "maintainer": all, "member": all, "viewer": all, "raised": all, "mixOwner": all,
		"aOwner": "page-alpha", "aMaint": "page-alpha", "aMember": "page-alpha", "aViewer": "page-alpha",
		"bMember": "page-beta", "pmix": "page-alpha,page-beta",
	} {
		got := strings.Split(w.names(t, "/api/v1/status-pages", w.hdr(name), "id", sp.names), ",")
		if strings.Join(sortedStrings(got), ",") != strings.Join(sortedStrings(strings.Split(want, ",")), ",") {
			t.Errorf("%s sees pages %v, want %s", name, got, want)
		}
	}
	// The filter narrows to a project and is 404 for one the caller does not see.
	if got := w.names(t, "/api/v1/status-pages?projectId="+w.alpha, w.hdr("owner"), "id", sp.names); got != "page-alpha" {
		t.Errorf("filter alpha: %s", got)
	}
	if r := w.c.do(http.MethodGet, "/api/v1/status-pages?projectId="+w.beta, "", w.hdr("aMaint")); r.code != http.StatusNotFound {
		t.Errorf("filter on an invisible project: %d %s", r.code, r.raw)
	}
	// Pages of an invisible project are 404, not 403, for read and write alike.
	for _, call := range [][2]string{{http.MethodGet, ""}, {http.MethodPatch, `{"description":""}`}, {http.MethodDelete, ""}} {
		if r := w.c.do(call[0], "/api/v1/status-pages/"+sp.byProject["beta"], call[1], w.hdr("aMaint")); r.code != http.StatusNotFound {
			t.Errorf("%s of a page of an invisible project: %d %s", call[0], r.code, r.raw)
		}
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestIntegrationStatusPageProjectRules covers what a project maintainer does in its own project,
// the components of a project page, a monitor moved to another project, and deleting a project.
func TestIntegrationStatusPageProjectRules(t *testing.T) {
	w := newProjectWorld(t, "statuspageprojectrules")
	sp := w.addStatusPages(t)
	c := w.c
	aMaint, owner := w.hdr("aMaint"), w.hdr("owner")
	ctx := context.Background()
	public := w.env.Conn(t, w.env.StatuspageURL)
	published := func(slug string) bool {
		var payload string
		err := public.QueryRow(ctx, `SELECT payload::text FROM status_page_snapshot($1)`, slug).Scan(&payload)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return err == nil
	}

	// A project maintainer creates, publishes and protects a page of its project.
	slug := nextSlug()
	r := c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+slug+`","title":"Alpha status","projectId":"`+w.alpha+`"}`, aMaint)
	if r.code != http.StatusCreated || r.body["projectId"] != w.alpha {
		t.Fatalf("create in own project: %d %s", r.code, r.raw)
	}
	mine := "/api/v1/status-pages/" + r.body["id"].(string)
	// Not in a project it does not see, nor without a project.
	if r := c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"x","projectId":"`+w.beta+`"}`, aMaint); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "projectId") {
		t.Errorf("create in an invisible project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"x"}`, aMaint); r.code != http.StatusForbidden {
		t.Errorf("project maintainer creates an organization-level page: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"x","projectId":null}`, aMaint); r.code != http.StatusForbidden {
		t.Errorf("null project is organization-level: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, mine+"/password", `{"password":"correct horse battery"}`, aMaint); r.code != http.StatusNoContent {
		t.Fatalf("set password: %d %s", r.code, r.raw)
	}

	// Components: only monitors of the page's project, for a project maintainer and for an
	// organization owner alike; nothing is stored when one is refused.
	own := `{"monitorId":"` + w.mAlpha + `","publicName":"Site"},{"monitorId":"` + w.hbAlpha + `","publicName":"Cron"}`
	if r := c.do(http.MethodPut, mine+"/components", `{"items":[`+own+`]}`, aMaint); r.code != http.StatusOK {
		t.Fatalf("own monitors: %d %s", r.code, r.raw)
	}
	for _, who := range []string{"aMaint", "owner", "maintainer"} {
		r := c.do(http.MethodPut, mine+"/components", `{"items":[`+own+`,{"monitorId":"`+w.mBeta+`","publicName":"Other"}]}`, w.hdr(who))
		if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "items.2.monitorId") {
			t.Errorf("%s: monitor of another project: %d %s", who, r.code, r.raw)
		}
	}
	if r := c.do(http.MethodGet, mine+"/components", "", aMaint); len(r.body["items"].([]any)) != 2 {
		t.Errorf("components after a refused change: %s", r.raw)
	}
	// An organization-level page may mix projects, as before.
	mixed := `{"items":[{"monitorId":"` + w.mAlpha + `","publicName":"A"},{"monitorId":"` + w.mBeta + `","publicName":"B"}]}`
	if r := c.do(http.MethodPut, "/api/v1/status-pages/"+sp.org+"/components", mixed, owner); r.code != http.StatusOK {
		t.Fatalf("mixed components on the organization-level page: %d %s", r.code, r.raw)
	}

	// Publishing works for the renderer, which reads snapshots only.
	if r := c.do(http.MethodPatch, mine, `{"published":true}`, aMaint); r.code != http.StatusOK {
		t.Fatalf("publish: %d %s", r.code, r.raw)
	}
	if !published(slug) {
		t.Fatal("the page of a project has no public snapshot")
	}

	// A monitor moved to another project is dropped from the pages of its old project, which are
	// refreshed; organization-level pages keep it.
	if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"projectId":"`+w.gamma+`"}`, owner); r.code != http.StatusOK {
		t.Fatalf("move monitor: %d %s", r.code, r.raw)
	}
	r = c.do(http.MethodGet, mine+"/components", "", aMaint)
	items := r.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["monitorId"] != w.hbAlpha {
		t.Errorf("components of the old project's page after the move: %s", r.raw)
	}
	if r := c.do(http.MethodGet, "/api/v1/status-pages/"+sp.org+"/components", "", owner); len(r.body["items"].([]any)) != 2 {
		t.Errorf("organization-level page after the move: %s", r.raw)
	}
	if r := c.do(http.MethodGet, mine, "", aMaint); r.body["componentCount"] != float64(1) {
		t.Errorf("component count after the move: %s", r.raw)
	}
	// The page of the new project did not gain it.
	if r := c.do(http.MethodGet, "/api/v1/status-pages/"+sp.byProject["gamma"]+"/components", "", owner); len(r.body["items"].([]any)) != 0 {
		t.Errorf("page of the target project: %s", r.raw)
	}
	// The monitor can now be shown on the page of its new project, and no longer on the old one.
	if r := c.do(http.MethodPut, "/api/v1/status-pages/"+sp.byProject["gamma"]+"/components", `{"items":[{"monitorId":"`+w.mAlpha+`","publicName":"Moved"}]}`, owner); r.code != http.StatusOK {
		t.Errorf("moved monitor on the new project's page: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPut, mine+"/components", `{"items":[{"monitorId":"`+w.mAlpha+`","publicName":"Moved"}]}`, owner); r.code != http.StatusUnprocessableEntity {
		t.Errorf("moved monitor on the old project's page: %d %s", r.code, r.raw)
	}

	// A project that still has a status page is not empty.
	r = c.do(http.MethodPost, "/api/v1/projects", `{"name":"Delta"}`, owner)
	delta := r.body["id"].(string)
	r = c.do(http.MethodPost, "/api/v1/status-pages", `{"slug":"`+nextSlug()+`","title":"Delta","projectId":"`+delta+`"}`, owner)
	deltaPage := r.body["id"].(string)
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+delta, "", owner); r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:project-not-empty" {
		t.Errorf("delete a project with a status page: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/status-pages/"+deltaPage, "", owner); r.code != http.StatusNoContent {
		t.Fatalf("delete the page: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodDelete, "/api/v1/projects/"+delta, "", owner); r.code != http.StatusNoContent {
		t.Errorf("delete the empty project: %d %s", r.code, r.raw)
	}

	// Reading is for every role of the project; changing is not.
	for _, who := range []string{"aMember", "aViewer"} {
		if r := c.do(http.MethodGet, mine, "", w.hdr(who)); r.code != http.StatusOK {
			t.Errorf("%s reads the project page: %d %s", who, r.code, r.raw)
		}
		if r := c.do(http.MethodPatch, mine, `{"title":"Hijack"}`, w.hdr(who)); r.code != http.StatusForbidden {
			t.Errorf("%s changes the project page: %d %s", who, r.code, r.raw)
		}
	}
}

// TestIntegrationChannelSecretsFollowTheOrganization checks what people who hold only project
// roles may do with an organization-wide channel: pick it for their monitors and see its name,
// never its settings, and never change, delete or test it.
func TestIntegrationChannelSecretsFollowTheOrganization(t *testing.T) {
	w := newProjectWorld(t, "channelsecrets")
	c := w.c
	org := "/api/v1/channels/" + w.chOrg
	for _, who := range []string{"aMaint", "aOwner", "aMember", "aViewer", "member", "viewer", "raised", "mixOwner"} {
		hdr := w.hdr(who)
		orgManager := false // an organization role below maintainer holds no channel permission
		r := c.do(http.MethodGet, "/api/v1/channels", "", hdr)
		var found map[string]any
		for _, it := range r.body["items"].([]any) {
			if m := it.(map[string]any); m["id"] == w.chOrg {
				found = m
			}
		}
		if found == nil {
			t.Errorf("%s: the organization-wide channel is not listed: %s", who, r.raw)
			continue
		}
		hidden := !orgManager
		events, _ := found["events"].([]any)
		mention, _ := found["mention"].([]any)
		if found["configHidden"] != hidden || len(events) != 0 || len(mention) != 0 || found["reminderMinutes"] != float64(0) || found["name"] != "c-org" || found["type"] != "slack_webhook" {
			t.Errorf("%s: organization-wide channel in the list: %v", who, found)
		}
		g := c.do(http.MethodGet, org, "", hdr)
		if g.code != http.StatusOK || g.body["configHidden"] != true || strings.Contains(g.raw, "hooks.slack.com") {
			t.Errorf("%s: get organization-wide channel: %d %s", who, g.code, g.raw)
		}
		// Project channels they can see keep their settings.
		for _, item := range r.body["items"].([]any) {
			m := item.(map[string]any)
			if m["projectId"] != nil && m["configHidden"] != false {
				t.Errorf("%s: project channel hidden: %v", who, m)
			}
		}
		if strings.Contains(r.raw, "hooks.slack.com") {
			t.Errorf("%s: a webhook URL in the list", who)
		}
		for _, call := range []roleCall{
			{http.MethodPatch, org, `{"name":"c-org"}`}, {http.MethodDelete, org, ""},
		} {
			if r := c.do(call.method, call.path, call.body, hdr); r.code != http.StatusForbidden {
				t.Errorf("%s: %s %s: %d %s, want 403", who, call.method, call.path, r.code, r.raw)
			}
		}
	}
	// The test send is rate limited per organization, whatever its outcome: one attempt is made.
	if r := c.do(http.MethodPost, org+"/test", "", w.hdr("aMaint")); r.code != http.StatusForbidden {
		t.Errorf("aMaint: test the organization-wide channel: %d %s, want 403", r.code, r.raw)
	}
	// Organization owners and maintainers see the settings and change the channel.
	for _, who := range []string{"owner", "maintainer"} {
		g := c.do(http.MethodGet, org, "", w.hdr(who))
		if g.code != http.StatusOK || g.body["configHidden"] != false || len(g.body["events"].([]any)) == 0 {
			t.Errorf("%s: organization-wide channel: %d %s", who, g.code, g.raw)
		}
		if r := c.do(http.MethodPatch, org, `{"name":"c-org","mention":["@here"]}`, w.hdr(who)); r.code != http.StatusOK || r.body["configHidden"] != false {
			t.Errorf("%s: change organization-wide channel: %d %s", who, r.code, r.raw)
		}
		c.do(http.MethodPatch, org, `{"mention":[]}`, w.hdr(who))
	}
	// A project maintainer selects it for a monitor of its project, but not a channel of another project.
	aMaint := w.hdr("aMaint")
	if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"channelIds":["`+w.chOrg+`"]}`, aMaint); r.code != http.StatusOK {
		t.Errorf("select the organization-wide channel: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"channelIds":["`+w.chBeta+`"]}`, aMaint); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, "channelIds") {
		t.Errorf("select a channel of another project: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/monitors/"+w.mAlpha, `{"channelIds":[]}`, aMaint); r.code != http.StatusOK {
		t.Errorf("clear the selection: %d %s", r.code, r.raw)
	}
	// Nor can it create an organization-wide channel or move its own channel there.
	if r := c.do(http.MethodPost, "/api/v1/channels", `{"name":"x","webhookUrl":"https://hooks.slack.com/services/T000/B000/x","organizationWide":true}`, aMaint); r.code != http.StatusForbidden {
		t.Errorf("create organization-wide channel: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+w.chAlpha, `{"organizationWide":true}`, aMaint); r.code != http.StatusForbidden {
		t.Errorf("make a project channel organization-wide: %d %s", r.code, r.raw)
	}
}

// TestIntegrationStatusPageLimitIsPerProject runs on the default Free limit (1): every project gets
// its own page and so does the organization, a full scope answers 409 naming the scope, the list can
// be filtered by project, and raising max_status_pages is all that allows a second page in a project.
func TestIntegrationStatusPageLimitIsPerProject(t *testing.T) {
	w := newProjectWorld(t, "statuspageperproject")
	owner := w.hdr("owner")
	create := func(project string) response {
		body := `{"slug":"` + nextSlug() + `","title":"t"`
		if project != "" {
			body += `,"projectId":"` + project + `"`
		}
		return w.c.do(http.MethodPost, "/api/v1/status-pages", body+`}`, owner)
	}
	for _, p := range []string{w.alpha, w.beta, ""} {
		if r := create(p); r.code != http.StatusCreated {
			t.Fatalf("first page of %q: %d %s", p, r.code, r.raw)
		}
	}
	for p, scope := range map[string]string{w.alpha: "Each project has its own allowance.", "": "pages that belong to no project"} {
		r := create(p)
		if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:plan-limit-reached" || r.body["limit"] != "statusPages" ||
			r.body["max"] != float64(1) || !strings.Contains(fmt.Sprint(r.body["detail"]), scope) {
			t.Fatalf("second page of %q: %d %s", p, r.code, r.raw)
		}
	}
	if r := w.c.do(http.MethodGet, "/api/v1/status-pages?projectId="+w.beta, "", owner); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("list of one project: %d %s", r.code, r.raw)
	}
	if u := w.c.do(http.MethodGet, "/api/v1/usage", "", owner); u.code != http.StatusOK || u.body["usage"].(map[string]any)["statusPages"] != float64(1) {
		t.Fatalf("usage: %d %s", u.code, u.raw)
	}
	pgtest.MustExec(t, w.env.AdminConn(t), `UPDATE tenants SET limits = '{"max_status_pages": 2}' WHERE slug = 'default'`)
	if r := create(w.alpha); r.code != http.StatusCreated {
		t.Fatalf("second page after raising the limit: %d %s", r.code, r.raw)
	}
	if r := w.c.do(http.MethodGet, "/api/v1/status-pages?projectId="+w.alpha, "", owner); len(r.body["items"].([]any)) != 2 {
		t.Fatalf("list after raising: %s", r.raw)
	}
}
