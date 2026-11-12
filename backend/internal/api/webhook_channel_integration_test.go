package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const hookEndpoint = "https://hooks.example.net/in/SECRETPATHTOKEN"

type hookSend struct{ url, contentType, body string }

type fakeWebhook struct {
	mu    sync.Mutex
	err   error
	sends []hookSend
}

func (f *fakeWebhook) Send(_ context.Context, u, contentType, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, hookSend{u, contentType, body})
	return f.err
}

func fieldErrorsOf(r response) map[string]string {
	out := map[string]string{}
	list, _ := r.body["fieldErrors"].([]any)
	for _, it := range list {
		m := it.(map[string]any)
		out[m["field"].(string)] = m["message"].(string)
	}
	return out
}

func TestIntegrationWebhookChannelAPI(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiwebhookchannel"))
	logs := &lockedBuffer{}
	hook := &fakeWebhook{}
	clk := clock.NewFake(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	c := newChannelServer(t, env, &fakeSlack{}, hook, logs, clk)
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)
	pgtest.MustExec(t, env.AdminConn(t), `UPDATE tenants SET limits = '{"max_channels": 20}' WHERE slug = 'default'`)
	var bodies []string
	record := func(r response) response { bodies = append(bodies, r.raw); clk.Advance(5 * time.Second); return r }
	post := func(path, body string) response { return record(c.do(http.MethodPost, path, body, unsafe)) }

	t.Run("preview", func(t *testing.T) {
		if r := c.do(http.MethodPost, "/api/v1/channels/preview-template", `{"contentType":"text","bodyTemplate":"x"}`, nil); r.code != http.StatusUnauthorized {
			t.Fatalf("without session: %d", r.code)
		}
		if r := c.do(http.MethodPost, "/api/v1/channels/preview-template", `{"contentType":"text","bodyTemplate":"x"}`, cookie); r.code != http.StatusForbidden {
			t.Fatalf("without CSRF: %d", r.code)
		}
		r := post("/api/v1/channels/preview-template", `{"contentType":"json","bodyTemplate":"{\"name\":\"{{monitor.name}}\",\"event\":\"{{event}}\"}"}`)
		if r.code != 200 || r.body["body"] != `{"name":"Example \"API\" monitor","event":"test"}` {
			t.Fatalf("json preview: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels/preview-template", `{"contentType":"text","bodyTemplate":"{{monitor.name}} {{link}}"}`)
		if r.code != 200 || r.body["body"] != `Example "API" monitor https://proreta.example.com/monitors/0190a8d0-0000-7000-8000-000000000001` {
			t.Fatalf("text preview: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels/preview-template", `{"contentType":"text","bodyTemplate":"hi {{monitor.nam}}"}`)
		if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || !strings.Contains(fe["bodyTemplate"], "{{monitor.nam}}") {
			t.Fatalf("unknown placeholder: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels/preview-template", `{"contentType":"json","bodyTemplate":"{\"a\": \"{{event}}\",}"}`)
		if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || !strings.Contains(fe["bodyTemplate"], "at byte") {
			t.Fatalf("invalid json: %d %s", r.code, r.raw)
		}
		if r := post("/api/v1/channels/preview-template", `{"contentType":"xml","bodyTemplate":"x"}`); r.code != http.StatusBadRequest {
			t.Fatalf("unknown content type: %d %s", r.code, r.raw)
		}
		if r := post("/api/v1/channels/preview-template", `{"contentType":"text","bodyTemplate":"`+strings.Repeat("a", 8193)+`"}`); r.code != http.StatusBadRequest {
			t.Fatalf("oversized template: %d", r.code)
		}
	})

	t.Run("create validates", func(t *testing.T) {
		for _, u := range []string{"http://hooks.example.net/x", "https://127.0.0.1/x", "https://10.1.2.3/x", "https://localhost/x", "https://[::1]/x", "https://hooks.example.net:8443/x", "https://user:pw@hooks.example.net/x", "https://service.internal/x", "https://169.254.169.254/latest"} {
			r := post("/api/v1/channels", `{"type":"webhook","name":"w","webhookUrl":"`+u+`"}`)
			if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || fe["webhookUrl"] == "" {
				t.Errorf("%s: %d %s", u, r.code, r.raw)
			}
			if strings.Contains(r.raw, "169.254") || strings.Contains(r.raw, "user:pw") {
				t.Errorf("%s echoed: %s", u, r.raw)
			}
		}
		r := post("/api/v1/channels", `{"type":"webhook","name":"w","webhookUrl":"`+hookEndpoint+`","mention":["@here"]}`)
		if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || fe["mention"] == "" {
			t.Errorf("mention on webhook: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels", `{"type":"webhook","name":"w","webhookUrl":"`+hookEndpoint+`","contentType":"json","bodyTemplate":"{{bogus}}"}`)
		if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || !strings.Contains(fe["bodyTemplate"], "{{bogus}}") {
			t.Errorf("unknown placeholder: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels", `{"type":"webhook","name":"w","webhookUrl":"`+hookEndpoint+`","contentType":"json","bodyTemplate":"not json {{event}}"}`)
		if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || !strings.Contains(fe["bodyTemplate"], "valid JSON") {
			t.Errorf("not json: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels", `{"name":"s","webhookUrl":"`+slackURL+`","contentType":"json"}`)
		if fe := fieldErrorsOf(r); r.code != http.StatusUnprocessableEntity || fe["contentType"] == "" {
			t.Errorf("content type on slack: %d %s", r.code, r.raw)
		}
		r = post("/api/v1/channels", `{"type":"webhook","name":"w","webhookUrl":"`+slackURL+`x"}`)
		if r.code != http.StatusCreated {
			t.Errorf("a Slack-looking URL is just an https URL for a webhook channel: %d %s", r.code, r.raw)
		}
		if r := post("/api/v1/channels", `{"type":"smtp","name":"w","webhookUrl":"`+hookEndpoint+`"}`); r.code != http.StatusBadRequest {
			t.Errorf("unknown type: %d %s", r.code, r.raw)
		}
	})

	var id string
	t.Run("create with defaults, read, update", func(t *testing.T) {
		r := post("/api/v1/channels", `{"type":"webhook","name":"hook","webhookUrl":"`+hookEndpoint+`","events":["down"],"reminderMinutes":15}`)
		if r.code != http.StatusCreated || r.body["type"] != "webhook" || r.body["contentType"] != "json" || r.body["bodyTemplate"] != notify.DefaultJSONTemplate ||
			fmt.Sprint(r.body["mention"]) != "[]" || r.body["reminderMinutes"] != 15.0 {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		id = r.body["id"].(string)
		for _, k := range []string{"webhookUrl", "url"} {
			if _, ok := r.body[k]; ok {
				t.Fatalf("%s returned", k)
			}
		}
		slack := createChannel(t, c, unsafe, `{"name":"slack","webhookUrl":"`+slackURL+`"}`)
		g := record(c.do(http.MethodGet, "/api/v1/channels/"+slack, "", cookie))
		if g.code != 200 || g.body["contentType"] != nil || g.body["bodyTemplate"] != nil {
			t.Fatalf("slack channel: %s", g.raw)
		}
		l := record(c.do(http.MethodGet, "/api/v1/channels", "", cookie))
		if l.code != 200 || !strings.Contains(l.raw, `"type":"webhook"`) {
			t.Fatalf("list: %s", l.raw)
		}

		p := record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"contentType":"text","bodyTemplate":"{{event}}: {{monitor.name}}"}`, unsafe))
		if p.code != 200 || p.body["contentType"] != "text" || p.body["bodyTemplate"] != "{{event}}: {{monitor.name}}" {
			t.Fatalf("patch template: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"contentType":"json"}`, unsafe))
		if fe := fieldErrorsOf(p); p.code != http.StatusUnprocessableEntity || !strings.Contains(fe["bodyTemplate"], "valid JSON") {
			t.Fatalf("content type change that breaks the template: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"contentType":"json","bodyTemplate":"{\"e\":\"{{event}}\"}","name":"renamed"}`, unsafe))
		if p.code != 200 || p.body["contentType"] != "json" || p.body["name"] != "renamed" {
			t.Fatalf("patch both: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"bodyTemplate":"{{nope}}"}`, unsafe))
		if fe := fieldErrorsOf(p); p.code != http.StatusUnprocessableEntity || !strings.Contains(fe["bodyTemplate"], "{{nope}}") {
			t.Fatalf("patch unknown placeholder: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"webhookUrl":"https://10.0.0.5/x"}`, unsafe))
		if fe := fieldErrorsOf(p); p.code != http.StatusUnprocessableEntity || fe["webhookUrl"] == "" {
			t.Fatalf("patch private url: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"mention":["@here"]}`, unsafe))
		if p.code != http.StatusUnprocessableEntity {
			t.Fatalf("patch mention: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+slack, `{"bodyTemplate":"x"}`, unsafe))
		if p.code != http.StatusUnprocessableEntity {
			t.Fatalf("patch template of a slack channel: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+slack, `{"webhookUrl":"`+hookEndpoint+`"}`, unsafe))
		if fe := fieldErrorsOf(p); p.code != http.StatusUnprocessableEntity || fe["webhookUrl"] == "" {
			t.Fatalf("a Slack channel keeps the Slack URL rule: %d %s", p.code, p.raw)
		}
		p = record(c.do(http.MethodPatch, "/api/v1/channels/"+id, `{"webhookUrl":"https://hooks.example.net/in/NEWSECRETURL"}`, unsafe))
		if p.code != 200 {
			t.Fatalf("patch url: %d %s", p.code, p.raw)
		}
	})

	t.Run("test sends the rendered sample", func(t *testing.T) {
		if r := post("/api/v1/channels/"+id+"/test", ""); r.code != http.StatusNoContent {
			t.Fatalf("test: %d %s", r.code, r.raw)
		}
		hook.mu.Lock()
		got := hook.sends
		hook.mu.Unlock()
		if len(got) != 1 || got[0].url != "https://hooks.example.net/in/NEWSECRETURL" || got[0].contentType != "json" || got[0].body != `{"e":"test"}` {
			t.Fatalf("sends = %+v", got)
		}
		hook.err = &notify.SendError{Kind: notify.KindHTTPStatus, Status: 503, Generic: true}
		r := post("/api/v1/channels/"+id+"/test", "")
		if r.code != http.StatusBadGateway || r.body["type"] != "urn:proreta:problem:channel-delivery-failed" || !strings.Contains(r.raw, "503") || strings.Contains(r.raw, "Slack") {
			t.Fatalf("test 503: %d %s", r.code, r.raw)
		}
		hook.err = nil
	})

	all := strings.Join(bodies, "\n") + logs.String()
	for _, secret := range []string{"SECRETPATHTOKEN", "NEWSECRETURL"} {
		if strings.Contains(all, secret) {
			t.Errorf("%s leaked in responses or logs", secret)
		}
	}
	var sealed int
	if err := env.AdminConn(t).QueryRow(context.Background(), `SELECT count(*) FROM notification_channels WHERE type = 'webhook' AND position(convert_to('hooks.example.net', 'UTF8') in config_encrypted) > 0`).Scan(&sealed); err != nil || sealed != 0 {
		t.Errorf("plain URL in the database: %d %v", sealed, err)
	}
}

func TestIntegrationWebhookChannelRoles(t *testing.T) {
	w := newProjectWorld(t, "webhookchannelroles")
	c := w.c
	pgtest.MustExec(t, w.env.AdminConn(t), `UPDATE tenants SET limits = '{"max_channels": 50}' WHERE slug = 'default'`)
	body := func(extra string) string {
		return `{"type":"webhook","name":"w","webhookUrl":"` + hookEndpoint + `"` + extra + `}`
	}
	preview := `{"contentType":"text","bodyTemplate":"{{event}}"}`

	// A maintainer of a project only manages that project's webhook channels.
	aMaint := w.hdr("aMaint")
	r := c.do(http.MethodPost, "/api/v1/channels", body(`,"projectId":"`+w.alpha+`"`), aMaint)
	if r.code != http.StatusCreated || r.body["projectId"] != w.alpha {
		t.Fatalf("project maintainer creates a channel in its project: %d %s", r.code, r.raw)
	}
	mine := r.body["id"].(string)
	if r := c.do(http.MethodPatch, "/api/v1/channels/"+mine, `{"bodyTemplate":"{{status}}","contentType":"text"}`, aMaint); r.code != http.StatusOK {
		t.Errorf("project maintainer edits its template: %d %s", r.code, r.raw)
	}
	for name, call := range map[string]roleCall{
		"create in another project": {http.MethodPost, "/api/v1/channels", body(`,"projectId":"` + w.beta + `"`)},
		"create organization-wide":  {http.MethodPost, "/api/v1/channels", body(`,"organizationWide":true`)},
		"move to the organization":  {http.MethodPatch, "/api/v1/channels/" + mine, `{"organizationWide":true}`},
	} {
		if r := c.do(call.method, call.path, call.body, aMaint); r.code != http.StatusForbidden && r.code != http.StatusNotFound && r.code != http.StatusUnprocessableEntity {
			t.Errorf("project maintainer: %s: %d %s", name, r.code, r.raw)
		}
	}
	if r := c.do(http.MethodPost, "/api/v1/channels/preview-template", preview, aMaint); r.code != http.StatusOK {
		t.Errorf("project maintainer previews: %d %s", r.code, r.raw)
	}

	// An organization-wide webhook channel: managed by owner and maintainer, hidden from others.
	r = c.do(http.MethodPost, "/api/v1/channels", body(`,"organizationWide":true`), w.hdr("maintainer"))
	if r.code != http.StatusCreated {
		t.Fatalf("organization maintainer creates an organization-wide channel: %d %s", r.code, r.raw)
	}
	org := "/api/v1/channels/" + r.body["id"].(string)
	for _, who := range []string{"aMaint", "aMember", "aViewer", "member", "viewer"} {
		hdr := w.hdr(who)
		g := c.do(http.MethodGet, org, "", hdr)
		if g.code != http.StatusOK || g.body["configHidden"] != true || g.body["bodyTemplate"] != nil || g.body["contentType"] != nil || strings.Contains(g.raw, "hooks.example.net") {
			t.Errorf("%s: organization-wide webhook channel: %d %s", who, g.code, g.raw)
		}
		if r := c.do(http.MethodPatch, org, `{"bodyTemplate":"x","contentType":"text"}`, hdr); r.code != http.StatusForbidden {
			t.Errorf("%s: edit an organization-wide channel: %d %s", who, r.code, r.raw)
		}
	}
	if g := c.do(http.MethodGet, org, "", w.hdr("maintainer")); g.code != http.StatusOK || g.body["bodyTemplate"] != notify.DefaultJSONTemplate || g.body["configHidden"] != false {
		t.Errorf("maintainer reads the template: %d %s", g.code, g.raw)
	}

	// Members, viewers and people without a role in the project cannot create or preview.
	for _, who := range []string{"aMember", "aViewer", "member", "viewer", "none"} {
		hdr := w.hdr(who)
		if r := c.do(http.MethodPost, "/api/v1/channels", body(`,"projectId":"`+w.alpha+`"`), hdr); r.code != http.StatusForbidden && r.code != http.StatusUnprocessableEntity {
			t.Errorf("%s: create: %d %s", who, r.code, r.raw)
		}
		if r := c.do(http.MethodPost, "/api/v1/channels/preview-template", preview, hdr); r.code != http.StatusForbidden {
			t.Errorf("%s: preview: %d %s", who, r.code, r.raw)
		}
	}
}
