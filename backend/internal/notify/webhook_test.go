package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const hookURL = "https://hooks.example.com/in/abc?token=secret"

func TestWebhookSend(t *testing.T) {
	var gotCT, gotMethod, gotBody, gotUA string
	d := newRewrite(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT, gotMethod, gotUA = r.Header.Get("Content-Type"), r.Method, r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
	}, time.Second)
	n := NewWebhookNotifier(d)
	if err := n.Send(context.Background(), hookURL, ContentJSON, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotCT != "application/json" || gotBody != `{"a":1}` || !strings.HasPrefix(gotUA, "Proreta/") {
		t.Errorf("method %q type %q body %q ua %q", gotMethod, gotCT, gotBody, gotUA)
	}
	if err := n.Send(context.Background(), hookURL, ContentText, "plain"); err != nil {
		t.Fatal(err)
	}
	if gotCT != "text/plain; charset=utf-8" || gotBody != "plain" {
		t.Errorf("type %q body %q", gotCT, gotBody)
	}
}

func TestWebhookSendFailures(t *testing.T) {
	tests := []struct {
		status            int
		kind              string
		permanent         bool
		retryAfterSeconds string
	}{
		{500, KindHTTPStatus, false, ""},
		{401, KindHTTPStatus, false, ""},
		{404, KindNotFound, true, ""},
		{410, KindNotFound, true, ""},
		{429, KindRateLimited, false, "7"},
		{302, KindHTTPStatus, false, ""},
	}
	for _, tt := range tests {
		d := newRewrite(t, func(w http.ResponseWriter, r *http.Request) {
			if tt.retryAfterSeconds != "" {
				w.Header().Set("Retry-After", tt.retryAfterSeconds)
			}
			if tt.status == 302 {
				w.Header().Set("Location", "https://other.example/")
			}
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte("secret answer body"))
		}, time.Second)
		d.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		err := NewWebhookNotifier(d).Send(context.Background(), hookURL, ContentText, "x")
		var se *SendError
		if !errors.As(err, &se) || se.Kind != tt.kind || se.Permanent != tt.permanent || se.Status != tt.status || !se.Generic {
			t.Errorf("status %d: %+v", tt.status, err)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("error leaks: %v", err)
		}
		if tt.status == 429 && se.RetryAfter != 7*time.Second {
			t.Errorf("retry after = %v", se.RetryAfter)
		}
	}
}

func TestWebhookSendRefusesBadInput(t *testing.T) {
	d := newRewrite(t, func(http.ResponseWriter, *http.Request) { t.Error("request sent") }, time.Second)
	n := NewWebhookNotifier(d)
	for _, u := range []string{"http://hooks.example.com/x", "https://127.0.0.1/x", "https://10.0.0.1/x", "https://localhost/x", "https://u:p@hooks.example.com/x", "https://hooks.example.com:8443/x", ""} {
		var se *SendError
		if err := n.Send(context.Background(), u, ContentText, "x"); !errors.As(err, &se) || se.Kind != KindInvalidURL || !se.Permanent {
			t.Errorf("%q: %v", u, err)
		}
	}
	if err := n.Send(context.Background(), hookURL, "xml", "x"); err == nil {
		t.Error("unknown content type accepted")
	}
	if err := n.Send(context.Background(), hookURL, ContentText, strings.Repeat("x", MaxRenderedBytes+1)); err == nil {
		t.Error("oversized body accepted")
	}
}

func TestWebhookSendThroughGuardRefusesPrivateName(t *testing.T) {
	// The real guarded client: a literal private address is refused before any connection.
	c := NewSlackClient(nil)
	n := NewWebhookNotifier(c)
	err := n.Send(context.Background(), "https://127.0.0.1/x", ContentText, "x")
	var se *SendError
	if !errors.As(err, &se) || se.Kind != KindInvalidURL {
		t.Fatalf("err = %v", err)
	}
}

func TestWebhookValues(t *testing.T) {
	id, inc := uuid.New(), uuid.New()
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	raw := []byte(`{"monitorId":"` + id.String() + `","monitorName":"API","url":"https://example.com/h?token=1","incidentId":"` + inc.String() +
		`","reason":"timeout","startedAt":"` + started.Format(time.RFC3339) + `","durationSeconds":300}`)
	hc := WebhookContext{MonitorType: "http", ProjectName: "P", OrganizationName: "O"}

	v, err := WebhookValues(DeliveryResolved, raw, "https://p.example/", hc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"event": "resolved", "status": "up", "monitor.id": id.String(), "monitor.name": "API", "monitor.url": "https://example.com/h",
		"monitor.type": "http", "project.name": "P", "organization.name": "O", "incident.id": inc.String(),
		"incident.startedAt": "2026-01-02T02:04:05Z", "incident.duration": "5m", "error.kind": "timeout",
		"link": "https://p.example/monitors/" + id.String(),
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if v["incident.resolvedAt"] != "" || v["tls.daysLeft"] != "" {
		t.Errorf("absent values must stay empty: %v", v)
	}
	v, _ = WebhookValues(DeliveryOpened, raw, "https://p.example", hc)
	if v["event"] != "opened" || v["status"] != "down" || v["incident.duration"] != "" {
		t.Errorf("opened: %v", v)
	}
	if _, err := WebhookValues(DeliveryTLSExpiring, raw, "", hc); !errors.Is(err, ErrBadPayload) {
		t.Errorf("tls without data: %v", err)
	}
	if _, err := WebhookValues("nope", raw, "", hc); !errors.Is(err, ErrBadPayload) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := WebhookValues(DeliveryOpened, []byte("{"), "", hc); !errors.Is(err, ErrBadPayload) {
		t.Errorf("bad json: %v", err)
	}
}

func TestTargetWebhookBody(t *testing.T) {
	id := uuid.New()
	raw := []byte(`{"monitorId":"` + id.String() + `","monitorName":"A \"quoted\" name","url":"https://example.com/"}`)
	tg := Target{ContentType: ContentJSON, BodyTemplate: `{"m":"{{monitor.name}}","e":"{{event}}"}`}
	body, err := tg.WebhookBody(DeliveryOpened, raw, "https://p.example")
	if err != nil || body != `{"m":"A \"quoted\" name","e":"opened"}` {
		t.Fatalf("%q %v", body, err)
	}
	tg.BodyTemplate = `{"m": {{monitor.name}}}`
	if _, err := tg.WebhookBody(DeliveryOpened, raw, "https://p.example"); !errors.Is(err, ErrBadTemplate) {
		t.Errorf("invalid json: %v", err)
	}
	tg.BodyTemplate = `{{bogus}}`
	if _, err := tg.WebhookBody(DeliveryOpened, raw, "https://p.example"); !errors.Is(err, ErrBadTemplate) {
		t.Errorf("unknown placeholder: %v", err)
	}
}
