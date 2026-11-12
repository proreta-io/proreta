package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const hookA = "https://hooks.example.net/in/tenant-a-secret"

type hookCall struct{ path, contentType, body string }

// fakeHook is the receiving endpoint of a webhook channel; it answers with the queued statuses.
type fakeHook struct {
	mu       sync.Mutex
	statuses []int
	calls    []hookCall
	srv      *httptest.Server
}

func newFakeHook(t *testing.T) *fakeHook {
	f := &fakeHook{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, hookCall{r.URL.Path, r.Header.Get("Content-Type"), string(b)})
		status := http.StatusOK
		if len(f.statuses) > 0 {
			status, f.statuses = f.statuses[0], f.statuses[1:]
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHook) got() []hookCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hookCall(nil), f.calls...)
}

func (f *fakeHook) Do(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(f.srv.URL)
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host = u.Scheme, u.Host, ""
	return f.srv.Client().Do(out)
}

func (e *env) webhookChannel(tenant, contentType, tpl string) uuid.UUID {
	e.t.Helper()
	pgtest.MustExec(e.t, e.admin, `UPDATE tenants SET limits = '{"max_channels": 10}' WHERE id = '`+tenant+`'`)
	svc := notify.NewService(e.Pool(e.t, e.APIURL, 2), e.keys, nil, "")
	c, err := svc.Create(e.ctx, notify.Actor{TenantID: uuid.MustParse(tenant), UserID: uuid.MustParse(userID), Access: authz.Actor{Role: authz.Owner}},
		notify.CreateInput{Type: notify.TypeWebhook, Name: "hook", WebhookURL: hookA, ContentType: &contentType, BodyTemplate: &tpl})
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *env) webhookDeliverer(hook *fakeHook, reg prometheus.Registerer) *jobs.Deliverer {
	return jobs.NewDeliverer(jobs.DeliveryConfig{PublicURL: "https://proreta.example.com", Concurrency: 10}, jobs.DeliveryDeps{
		Pool:     e.Pool(e.t, e.WorkerURL, 4),
		Channels: notify.NewService(e.Pool(e.t, e.WorkerURL, 2), e.keys, nil, ""),
		Notifier: notify.NewSlackNotifier(e.slack),
		Webhook:  notify.NewWebhookNotifier(hook),
		Metrics:  jobs.NewMetrics(reg),
		Log:      slog.New(slog.NewJSONHandler(e, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:    clock.System{},
		Sleep:    func(context.Context, time.Duration) error { return nil },
	})
}

func TestIntegrationWebhookDelivery(t *testing.T) {
	e := newEnv(t, "jobswebhook")
	hook := newFakeHook(t)
	reg := prometheus.NewRegistry()
	d := e.webhookDeliverer(hook, reg)
	jsonCh := e.webhookChannel(tenantA, notify.ContentJSON, `{"e":"{{event}}","s":"{{status}}","m":"{{monitor.name}}","t":"{{monitor.type}}","p":"{{project.name}}","o":"{{organization.name}}","r":"{{error.kind}}","l":"{{link}}","res":"{{incident.resolvedAt}}"}`)
	e.channels[tenantA] = jsonCh
	started := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

	t.Run("json body is escaped and filled in", func(t *testing.T) {
		m := e.monitor(tenantA, `Web "prod" \ <x>`)
		id := e.deliver(tenantA, m, notify.DeliveryOpened, map[string]any{"monitorName": `Web "prod" \ <x>`, "url": "https://web.example/", "reason": "timeout", "startedAt": started})
		if n := d.RunOnce(e.ctx); n != 1 {
			t.Fatalf("claimed %d", n)
		}
		if r := e.row(id); r.status != "sent" || r.attempts != 1 {
			t.Fatalf("row = %+v\n%s", r, e.logs.String())
		}
		got := hook.got()
		want := `{"e":"opened","s":"down","m":"Web \"prod\" \\ <x>","t":"http","p":"Default","o":"A","r":"timeout","l":"https://proreta.example.com/monitors/` + m.String() + `","res":""}`
		if len(got) != 1 || got[0].path != "/in/tenant-a-secret" || got[0].contentType != "application/json" || got[0].body != want {
			t.Fatalf("calls = %+v\nwant body %s", got, want)
		}
		if len(e.slack.sent()) != 0 {
			t.Fatal("a Slack message was sent for a webhook channel")
		}
		if v := metric(t, reg, "proreta_jobs_deliveries_total", notify.TypeWebhook, "sent"); v != 1 {
			t.Fatalf("sent metric = %v", v)
		}
	})

	t.Run("text body is verbatim", func(t *testing.T) {
		pgtest.MustExec(t, e.admin, `UPDATE notification_channels SET content_type = 'text', body_template = '{{event}} {{monitor.name}} after {{incident.duration}}' WHERE id = '`+jsonCh.String()+`'`)
		before := len(hook.got())
		m := e.monitor(tenantA, `A "b"`)
		id := e.deliver(tenantA, m, notify.DeliveryResolved, map[string]any{"monitorName": `A "b"`, "durationSeconds": 300})
		d.RunOnce(e.ctx)
		got := hook.got()
		if e.row(id).status != "sent" || len(got) != before+1 || got[before].contentType != "text/plain; charset=utf-8" || got[before].body != `resolved A "b" after 5m` {
			t.Fatalf("calls = %+v", got)
		}
	})

	t.Run("failure retries then succeeds", func(t *testing.T) {
		m := e.monitor(tenantA, "Retry")
		id := e.deliver(tenantA, m, notify.DeliveryResolved, map[string]any{"monitorName": "Retry", "durationSeconds": 60})
		hook.mu.Lock()
		hook.statuses = []int{500}
		hook.mu.Unlock()
		d.RunOnce(e.ctx)
		if r := e.row(id); r.status != "pending" || r.attempts != 1 || r.lastError == nil || *r.lastError != "http_status 500" {
			t.Fatalf("after 500: %+v", r)
		}
		e.makeDue()
		d.RunOnce(e.ctx)
		if r := e.row(id); r.status != "sent" || r.attempts != 2 {
			t.Fatalf("after retry: %+v", r)
		}
	})

	t.Run("404 marks the channel failed", func(t *testing.T) {
		m := e.monitor(tenantA, "Gone")
		id := e.deliver(tenantA, m, notify.DeliveryResolved, map[string]any{"monitorName": "Gone", "durationSeconds": 60})
		hook.mu.Lock()
		hook.statuses = []int{404}
		hook.mu.Unlock()
		d.RunOnce(e.ctx)
		var status string
		if err := e.admin.QueryRow(e.ctx, `SELECT status FROM notification_channels WHERE id = $1`, jsonCh).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if r := e.row(id); r.status != "failed" || status != "failed" {
			t.Fatalf("delivery %+v, channel %s", r, status)
		}
		pgtest.MustExec(t, e.admin, `UPDATE notification_channels SET status = 'active' WHERE id = '`+jsonCh.String()+`'`)
	})

	t.Run("a template that no longer renders fails the delivery", func(t *testing.T) {
		pgtest.MustExec(t, e.admin, `UPDATE notification_channels SET content_type = 'json', body_template = '{"a": {{event}}}' WHERE id = '`+jsonCh.String()+`'`)
		before := len(hook.got())
		m := e.monitor(tenantA, "Bad")
		id := e.deliver(tenantA, m, notify.DeliveryOpened, map[string]any{"monitorName": "Bad", "reason": "timeout"})
		d.RunOnce(e.ctx)
		if r := e.row(id); r.status != "failed" || r.lastError == nil || *r.lastError != "bad_template" || len(hook.got()) != before {
			t.Fatalf("row %+v, calls %d", r, len(hook.got())-before)
		}
	})

	t.Run("route changed", func(t *testing.T) {
		pgtest.MustExec(t, e.admin, `UPDATE notification_channels SET body_template = '{"a":"{{event}}"}' WHERE id = '`+jsonCh.String()+`'`)
		other := uuid.New()
		pgtest.MustExec(t, e.admin, `INSERT INTO projects (id, tenant_id, name, slug) VALUES ('`+other.String()+`', '`+tenantA+`', 'Other', 'other')`)
		m := e.monitor(tenantA, "Moved")
		before := len(hook.got())
		id := e.deliver(tenantA, m, notify.DeliveryOpened, map[string]any{"monitorName": "Moved", "reason": "timeout"})
		pgtest.MustExec(t, e.admin, `UPDATE monitors SET project_id = '`+other.String()+`' WHERE id = '`+m.String()+`'`)
		d.RunOnce(e.ctx)
		if r := e.row(id); r.status != "failed" || r.lastError == nil || *r.lastError != "route_changed" || len(hook.got()) != before {
			t.Fatalf("row %+v", r)
		}
	})

	t.Run("deleted channel", func(t *testing.T) {
		m := e.monitor(tenantA, "Del")
		id := e.deliver(tenantA, m, notify.DeliveryOpened, map[string]any{"monitorName": "Del", "reason": "timeout"})
		pgtest.MustExec(t, e.admin, `UPDATE notification_channels SET deleted_at = now(), config_encrypted = NULL, status = 'failed' WHERE id = '`+jsonCh.String()+`'`)
		before := len(hook.got())
		d.RunOnce(e.ctx)
		if r := e.row(id); r.status != "failed" || r.lastError == nil || *r.lastError != "channel_deleted" || len(hook.got()) != before {
			t.Fatalf("row %+v", r)
		}
	})
}

func TestIntegrationWebhookDeliveryRefusesPrivateAddress(t *testing.T) {
	e := newEnv(t, "jobswebhookssrf")
	ch := e.webhookChannel(tenantA, notify.ContentText, "{{event}}")
	e.channels[tenantA] = ch
	// A row that bypassed the save-time check: the delivery path must still refuse it. The
	// production guarded client is used, which never connects to loopback or private ranges.
	sealed, err := e.keys.Encrypt([]byte("https://127.0.0.1/hook"), []byte("notification_channels.config_encrypted:"+ch.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Exec(e.ctx, `UPDATE notification_channels SET config_encrypted = $1 WHERE id = $2`, sealed, ch); err != nil {
		t.Fatal(err)
	}
	d := jobs.NewDeliverer(jobs.DeliveryConfig{PublicURL: "https://proreta.example.com"}, jobs.DeliveryDeps{
		Pool:     e.Pool(e.t, e.WorkerURL, 4),
		Channels: notify.NewService(e.Pool(e.t, e.WorkerURL, 2), e.keys, nil, ""),
		Notifier: notify.NewSlackNotifier(e.slack),
		Webhook:  notify.NewWebhookNotifier(notify.NewSlackClient(nil)),
		Metrics:  jobs.NewMetrics(prometheus.NewRegistry()),
		Log:      slog.New(slog.NewJSONHandler(e, nil)),
	})
	m := e.monitor(tenantA, "Internal")
	id := e.deliver(tenantA, m, notify.DeliveryOpened, map[string]any{"monitorName": "Internal", "reason": "timeout"})
	d.RunOnce(e.ctx)
	r := e.row(id)
	if r.status != "failed" || r.lastError == nil || *r.lastError != "invalid_url" {
		t.Fatalf("row %+v", r)
	}
	if strings.Contains(e.logs.String(), "127.0.0.1") {
		t.Error("the URL reached the logs")
	}
}
