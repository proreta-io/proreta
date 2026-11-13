package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationSpeculaAPI(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apispecula"))
	c := newServer(t, env, auth.Options{})
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)
	admin := env.AdminConn(t)

	var defaultProject string
	r := c.do(http.MethodGet, "/api/v1/projects", "", cookie)
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		if m := it.(map[string]any); m["isDefault"] == true {
			defaultProject, _ = m["id"].(string)
		}
	}
	if defaultProject == "" {
		t.Fatalf("no default project: %s", r.raw)
	}

	r = c.do(http.MethodPost, "/api/v1/specula", `{"name":" Prod cluster "}`, unsafe)
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	token, _ := r.body["token"].(string)
	sp, _ := r.body["specula"].(map[string]any)
	id, _ := sp["id"].(string)
	if !strings.HasPrefix(token, "spc_") || sp["name"] != "Prod cluster" || sp["allProjects"] != true || sp["status"] != "pending" {
		t.Fatalf("created: %s", r.raw)
	}
	// The token is shown once: no read returns it.
	for _, path := range []string{"/api/v1/specula/" + id, "/api/v1/specula", "/api/v1/specula?includeRevoked=true"} {
		if got := c.do(http.MethodGet, path, "", cookie); got.code != http.StatusOK || strings.Contains(got.raw, token) {
			t.Fatalf("%s: %d %s", path, got.code, got.raw)
		}
	}
	// Unsafe operations need the CSRF token.
	if r := c.do(http.MethodPost, "/api/v1/specula", `{"name":"x"}`, cookie); r.code != http.StatusForbidden {
		t.Fatalf("create without CSRF: %d", r.code)
	}

	t.Run("validation", func(t *testing.T) {
		for body, field := range map[string]string{
			`{"name":"Prod cluster"}`:              "name",
			`{"name":"Other","allProjects":false}`: "projectIds",
			`{"name":"Stray","allProjects":false,"projectIds":["01920000-0000-7000-8000-0000000000ff"]}`: "projectIds",
		} {
			r := c.do(http.MethodPost, "/api/v1/specula", body, unsafe)
			fe, _ := r.body["fieldErrors"].([]any)
			if r.code != http.StatusUnprocessableEntity || len(fe) != 1 || fe[0].(map[string]any)["field"] != field {
				t.Errorf("%s: %d %s", body, r.code, r.raw)
			}
		}
		if r := c.do(http.MethodPost, "/api/v1/specula", `{"name":"","extra":1}`, unsafe); r.code != http.StatusBadRequest {
			t.Errorf("contract: %d", r.code)
		}
		if r := c.do(http.MethodGet, "/api/v1/specula/not-a-uuid", "", cookie); r.code != http.StatusBadRequest {
			t.Errorf("bad id: %d", r.code)
		}
	})

	t.Run("update and project filter", func(t *testing.T) {
		r := c.do(http.MethodPatch, "/api/v1/specula/"+id, `{"name":"Prod","allProjects":false,"projectIds":["`+defaultProject+`"]}`, unsafe)
		if r.code != http.StatusOK || r.body["name"] != "Prod" || r.body["allProjects"] != false {
			t.Fatalf("update: %d %s", r.code, r.raw)
		}
		ids, _ := r.body["projectIds"].([]any)
		if len(ids) != 1 || ids[0] != defaultProject {
			t.Fatalf("projectIds: %s", r.raw)
		}
		got := c.do(http.MethodGet, "/api/v1/specula?projectId="+defaultProject, "", cookie)
		if list, _ := got.body["items"].([]any); got.code != http.StatusOK || len(list) != 1 {
			t.Fatalf("filtered list: %d %s", got.code, got.raw)
		}
		if got := c.do(http.MethodGet, "/api/v1/specula?projectId=01920000-0000-7000-8000-0000000000ff", "", cookie); got.code != http.StatusNotFound {
			t.Fatalf("unknown project: %d", got.code)
		}
	})

	t.Run("a monitor runs at a Specula and moves back", func(t *testing.T) {
		r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"Orders","location":{"type":"specula","speculaId":"`+id+`"},"http":{"url":"http://orders.shop.svc:8080/health"}}`, unsafe)
		if r.code != http.StatusCreated {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		loc, _ := r.body["location"].(map[string]any)
		mid, _ := r.body["id"].(string)
		if loc["type"] != "specula" || loc["speculaId"] != id || loc["speculaName"] != "Prod" {
			t.Fatalf("location: %s", r.raw)
		}
		h, _ := r.body["http"].(map[string]any)
		if h["url"] != "http://orders.shop.svc:8080/health" {
			t.Fatalf("url: %s", r.raw)
		}
		list := c.do(http.MethodGet, "/api/v1/monitors", "", cookie)
		if !strings.Contains(list.raw, `"speculaName":"Prod"`) {
			t.Fatalf("list lacks the location: %s", list.raw)
		}
		// The private target is refused at the cloud location, and nothing changes.
		r = c.do(http.MethodPatch, "/api/v1/monitors/"+mid, `{"location":{"type":"cloud"}}`, unsafe)
		fe, _ := r.body["fieldErrors"].([]any)
		if r.code != http.StatusUnprocessableEntity || len(fe) != 1 || fe[0].(map[string]any)["field"] != "http.url" {
			t.Fatalf("move to cloud: %d %s", r.code, r.raw)
		}
		r = c.do(http.MethodPatch, "/api/v1/monitors/"+mid, `{"location":{"type":"cloud"},"http":{"url":"https://example.com/health"}}`, unsafe)
		if loc, _ := r.body["location"].(map[string]any); r.code != http.StatusOK || loc["type"] != "cloud" {
			t.Fatalf("move with a public url: %d %s", r.code, r.raw)
		}
		// Request shape rules.
		for body, field := range map[string]string{
			`{"location":{"type":"specula"}}`:                                                    "location.speculaId",
			`{"location":{"type":"cloud","speculaId":"` + id + `"}}`:                             "location.speculaId",
			`{"location":{"type":"specula","speculaId":"01920000-0000-7000-8000-0000000000ff"}}`: "location.speculaId",
		} {
			r := c.do(http.MethodPatch, "/api/v1/monitors/"+mid, body, unsafe)
			fe, _ := r.body["fieldErrors"].([]any)
			if r.code != http.StatusUnprocessableEntity || len(fe) != 1 || fe[0].(map[string]any)["field"] != field {
				t.Errorf("%s: %d %s", body, r.code, r.raw)
			}
		}
		// A heartbeat has no location.
		hb := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"heartbeat","name":"hb","location":{"type":"specula","speculaId":"`+id+`"},"heartbeat":{}}`, unsafe)
		if hb.code != http.StatusUnprocessableEntity {
			t.Fatalf("heartbeat with a location: %d %s", hb.code, hb.raw)
		}
		if loc := c.do(http.MethodGet, "/api/v1/monitors/"+mid, "", cookie); strings.Contains(loc.raw, `"speculaId"`) {
			t.Fatalf("a cloud monitor shows a Specula: %s", loc.raw)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		if r := c.do(http.MethodDelete, "/api/v1/specula/"+id, "", cookie); r.code != http.StatusForbidden {
			t.Fatalf("revoke without CSRF: %d", r.code)
		}
		if r := c.do(http.MethodDelete, "/api/v1/specula/"+id, "", unsafe); r.code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", r.code, r.raw)
		}
		if r := c.do(http.MethodDelete, "/api/v1/specula/"+id, "", unsafe); r.code != http.StatusNotFound {
			t.Fatalf("second revoke: %d", r.code)
		}
		if got := c.do(http.MethodGet, "/api/v1/specula", "", cookie); strings.Contains(got.raw, id) {
			t.Fatalf("revoked Specula is listed: %s", got.raw)
		}
		got := c.do(http.MethodGet, "/api/v1/specula?includeRevoked=true", "", cookie)
		if !strings.Contains(got.raw, `"status":"revoked"`) {
			t.Fatalf("includeRevoked: %s", got.raw)
		}
		if r := c.do(http.MethodPatch, "/api/v1/specula/"+id, `{"name":"x"}`, unsafe); r.code != http.StatusNotFound {
			t.Fatalf("update revoked: %d", r.code)
		}
		var actions int
		if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE entity_type = 'specula' AND action IN ('specula.created','specula.updated','specula.revoked')`).Scan(&actions); err != nil || actions < 3 {
			t.Fatalf("audit rows %d %v", actions, err)
		}
	})
}

// The plan limit is a 409 plan-limit-reached problem naming `specula`, and revoking frees a slot.
func TestIntegrationSpeculaQuotaAPI(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apispeculaquota"))
	c := newServer(t, env, auth.Options{})
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)
	pgtest.MustExec(t, env.AdminConn(t), `UPDATE tenants SET plan_key = 'free' WHERE slug = 'default'`)

	r := c.do(http.MethodPost, "/api/v1/specula", `{"name":"one"}`, unsafe)
	if r.code != http.StatusCreated {
		t.Fatalf("first: %d %s", r.code, r.raw)
	}
	sp, _ := r.body["specula"].(map[string]any)
	id, _ := sp["id"].(string)
	over := c.do(http.MethodPost, "/api/v1/specula", `{"name":"two"}`, unsafe)
	if over.code != http.StatusConflict || over.body["type"] != "urn:proreta:problem:plan-limit-reached" ||
		over.body["limit"] != "specula" || over.body["max"] != float64(1) {
		t.Fatalf("second: %d %s", over.code, over.raw)
	}
	if u := c.do(http.MethodGet, "/api/v1/usage", "", cookie); !strings.Contains(u.raw, `"usage":{"channels":0,"heartbeats":0,"members":1,"monitors":0,"projects":1,"specula":1,`) ||
		!strings.Contains(u.raw, `"specula":1,"statusPages":1}`) {
		t.Fatalf("usage: %s", u.raw)
	}
	if d := c.do(http.MethodDelete, "/api/v1/specula/"+id, "", unsafe); d.code >= 300 {
		t.Fatalf("revoke: %d %s", d.code, d.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/specula", `{"name":"two"}`, unsafe); r.code != http.StatusCreated {
		t.Fatalf("after revoke: %d %s", r.code, r.raw)
	}
}
