package api_test

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/authz"
)

// Written by hand per role, not computed from the tables in authz.
const (
	permsProjectViewer     = "channel.read,incident.read,monitor.read,project.members.read,project.read,specula.read,statuspage.read"
	permsProjectMember     = permsProjectViewer + ",heartbeat.manage,monitor.create,monitor.delete,monitor.update"
	permsProjectMaintainer = permsProjectMember + ",channel.manage,project.members.manage,project.update,statuspage.manage"
	permsProjectOwner      = permsProjectMaintainer + ",project.delete"
)

var permsProject = map[authz.Role]string{
	authz.Viewer: permsProjectViewer, authz.Member: permsProjectMember,
	authz.Maintainer: permsProjectMaintainer, authz.Owner: permsProjectOwner,
}

// Held through the organization role: the account and organization levels, then every project
// permission of the role (it applies in every project).
var permsOrganization = map[authz.Role]string{
	"":               "account.use",
	authz.Viewer:     "account.use,organization.members.read,organization.statuspages.read," + permsProjectViewer,
	authz.Member:     "account.use,organization.members.read,organization.statuspages.read," + permsProjectMember,
	authz.Maintainer: "account.use,organization.members.manage,organization.members.read,organization.statuspages.manage,organization.statuspages.read,project.create,specula.manage,usage.read," + permsProjectMaintainer,
	authz.Owner:      "account.use,organization.delete,organization.domains.manage,organization.members.manage,organization.members.read,organization.statuspages.manage,organization.statuspages.read,project.create,specula.manage,usage.read," + permsProjectOwner,
}

func sortedList(v any) string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, it := range list {
		out[i], _ = it.(string)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// TestIntegrationPermissionsEndpoint checks the permissions of every person of the project
// world against sets written out by hand.
func TestIntegrationPermissionsEndpoint(t *testing.T) {
	w := newProjectWorld(t, "permissionsendpoint")
	keys := map[string]string{"def": w.def, "alpha": w.alpha, "beta": w.beta, "gamma": w.gamma}
	for _, a := range roleActors {
		r := w.c.do(http.MethodGet, "/api/v1/permissions", "", w.hdr(a.name))
		if r.code != http.StatusOK {
			t.Errorf("%s: %d %s", a.name, r.code, r.raw)
			continue
		}
		var org any
		if a.org != "" {
			org = string(a.org)
		}
		if r.body["organizationRole"] != org {
			t.Errorf("%s organizationRole = %v, want %v", a.name, r.body["organizationRole"], org)
		}
		if got := sortedList(r.body["organization"]); got != sortedWords(permsOrganization[a.org]) {
			t.Errorf("%s organization = %s, want %s", a.name, got, sortedWords(permsOrganization[a.org]))
		}
		got := map[string]string{}
		items, _ := r.body["projects"].([]any)
		for _, it := range items {
			m := it.(map[string]any)
			got[m["projectId"].(string)] = m["name"].(string) + "/" + m["role"].(string) + "/" + sortedList(m["permissions"])
		}
		want := map[string]string{}
		for key, role := range a.eff {
			id := keys[key]
			want[id] = w.projectNames[id] + "/" + string(role) + "/" + sortedWords(permsProject[role])
		}
		if len(got) != len(want) {
			t.Errorf("%s sees %d projects, want %d: %s", a.name, len(got), len(want), r.raw)
		}
		for id, v := range want {
			if got[id] != v {
				t.Errorf("%s project %s = %s, want %s", a.name, w.projectNames[id], got[id], v)
			}
		}
	}

	r := w.c.do(http.MethodGet, "/api/v1/permissions", "", w.hdr("viewer"))
	cat, _ := r.body["catalogue"].([]any)
	if len(cat) != len(authz.Permissions()) {
		t.Fatalf("catalogue has %d entries, want %d", len(cat), len(authz.Permissions()))
	}
	byName := map[string]map[string]any{}
	for _, it := range cat {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if m := byName["channel.manage"]; m["level"] != "project" || sortedList(m["roles"]) != "maintainer,owner" || m["description"] == "" {
		t.Errorf("channel.manage = %v", m)
	}
	if m := byName["organization.delete"]; m["level"] != "organization" || sortedList(m["roles"]) != "owner" {
		t.Errorf("organization.delete = %v", m)
	}
	if m := byName["account.use"]; m["level"] != "account" || sortedList(m["roles"]) != "maintainer,member,owner,viewer" {
		t.Errorf("account.use = %v", m)
	}
	if r := w.c.do(http.MethodGet, "/api/v1/permissions", "", nil); r.code != http.StatusUnauthorized {
		t.Errorf("without a session: %d %s", r.code, r.raw)
	}
}

func sortedWords(csv string) string {
	words := strings.Split(csv, ",")
	sort.Strings(words)
	return strings.Join(words, ",")
}
