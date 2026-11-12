package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationMonitorRequest(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apimonitorreq"))
	c := newServer(t, env, auth.Options{})
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)
	admin := env.AdminConn(t)
	ctx := context.Background()

	const body = `{"ping":"SECRET-LOOKING-BODY"}`
	r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"Hook","http":{"url":"https://example.com/hook","method":"POST","requestBody":"{\"ping\":\"SECRET-LOOKING-BODY\"}"}}`, unsafe)
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	h, _ := r.body["http"].(map[string]any)
	if h["method"] != "POST" || h["requestBody"] != body || h["contentType"] != "application/json" || h["hasRequestBody"] != true {
		t.Fatalf("create http: %v", h)
	}

	// The list shows that there is a body, not the body.
	list := c.do(http.MethodGet, "/api/v1/monitors", "", cookie)
	item := list.body["items"].([]any)[0].(map[string]any)["http"].(map[string]any)
	if _, ok := item["requestBody"]; ok || item["hasRequestBody"] != true || item["method"] != "POST" || strings.Contains(list.raw, "SECRET-LOOKING") {
		t.Fatalf("list item: %v", item)
	}

	// The audit trail names the changed fields, never the body.
	p := c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"method":"PUT","requestBody":"a=b","contentType":"application/x-www-form-urlencoded"}}`, unsafe)
	ph, _ := p.body["http"].(map[string]any)
	if p.code != http.StatusOK || ph["method"] != "PUT" || ph["requestBody"] != "a=b" || ph["contentType"] != "application/x-www-form-urlencoded" {
		t.Fatalf("patch: %d %s", p.code, p.raw)
	}
	var meta string
	if err := admin.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'monitor.updated' AND entity_id = $1`, id).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"http.method", "http.requestBody", "http.contentType"} {
		if !strings.Contains(meta, want) {
			t.Errorf("audit metadata %s lacks %s", meta, want)
		}
	}
	for _, leak := range []string{"a=b", "SECRET-LOOKING", "application/x-www-form"} {
		if strings.Contains(meta, leak) {
			t.Errorf("audit metadata %s contains %q", meta, leak)
		}
	}

	// Pausing keeps the request.
	if pz := c.do(http.MethodPost, "/api/v1/monitors/"+id+"/pause", "", unsafe); pz.code != http.StatusOK ||
		pz.body["http"].(map[string]any)["requestBody"] != "a=b" {
		t.Fatalf("pause: %d %s", pz.code, pz.raw)
	}

	// Clearing the body clears the content type; the method stays.
	cl := c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"requestBody":""}}`, unsafe)
	ch, _ := cl.body["http"].(map[string]any)
	if cl.code != http.StatusOK || ch["method"] != "PUT" || ch["contentType"] != "" || ch["hasRequestBody"] != false {
		t.Fatalf("clear: %d %s", cl.code, cl.raw)
	}

	long := strings.Repeat("a", 16385)
	for _, tc := range []struct{ name, method, path, payload, field string }{
		{"body with GET", http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com","requestBody":"x"}}`, "http.requestBody"},
		{"body with HEAD", http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"x","http":{"url":"https://example.com","method":"HEAD","requestBody":"x"}}`, "http.requestBody"},
		{"update to GET keeps a body", http.MethodPatch, "/api/v1/monitors/" + id, `{"http":{"requestBody":"x"}}`, ""},
		{"too large on update", http.MethodPatch, "/api/v1/monitors/" + id, `{"http":{"method":"POST","requestBody":"` + long + `"}}`, "http.requestBody"},
	} {
		r := c.do(tc.method, tc.path, tc.payload, unsafe)
		if tc.name == "update to GET keeps a body" {
			// The monitor is PUT: a body is fine; this just checks it works after the clear.
			if r.code != http.StatusOK {
				t.Errorf("%s: %d %s", tc.name, r.code, r.raw)
			}
			continue
		}
		fe, _ := r.body["fieldErrors"].([]any)
		if r.code != http.StatusUnprocessableEntity || len(fe) == 0 || fe[0].(map[string]any)["field"] != tc.field {
			t.Errorf("%s: %d %s", tc.name, r.code, r.raw)
		}
	}
	// Switching to GET while a body is set is refused; so is an unknown method or content type.
	for payload, field := range map[string]string{
		`{"http":{"method":"GET"}}`:                    "http.requestBody",
		`{"http":{"contentType":"text/html"}}`:         "",
		`{"http":{"method":"TRACE"}}`:                  "",
		`{"http":{"method":"POST","requestBody":"x"}}`: "ok",
	} {
		r := c.do(http.MethodPatch, "/api/v1/monitors/"+id, payload, unsafe)
		switch field {
		case "ok":
			if r.code != http.StatusOK {
				t.Errorf("%s: %d %s", payload, r.code, r.raw)
			}
		case "":
			if r.code != http.StatusBadRequest && r.code != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d %s", payload, r.code, r.raw)
			}
		default:
			fe, _ := r.body["fieldErrors"].([]any)
			if r.code != http.StatusUnprocessableEntity || len(fe) == 0 || fe[0].(map[string]any)["field"] != field {
				t.Errorf("%s: %d %s", payload, r.code, r.raw)
			}
		}
	}

	// A heartbeat has none of these.
	hb := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"heartbeat","name":"hb","heartbeat":{}}`, unsafe)
	if hb.code != http.StatusCreated {
		t.Fatalf("heartbeat: %d %s", hb.code, hb.raw)
	}
	if _, ok := hb.body["http"]; ok {
		t.Fatalf("heartbeat has http settings: %s", hb.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"heartbeat","name":"hb2","heartbeat":{},"http":{"url":"https://example.com","method":"POST"}}`, unsafe); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("heartbeat with http: %d %s", r.code, r.raw)
	}
}
