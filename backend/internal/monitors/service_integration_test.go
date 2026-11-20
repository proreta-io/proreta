package monitors_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

const (
	privateURL = "http://10.0.0.5/health"
	publicURL  = "https://example.com/health"
)

// world is one organization with two projects, three actors of different standing, two Specula
// and two notification channels, driven through the services the way the API does.
type world struct {
	t                  *testing.T
	ctx                context.Context
	admin              *pgx.Conn
	svc                *monitors.Service // with the request keyring
	plain              *monitors.Service // without it
	owner              monitors.Actor
	projMaint          monitors.Actor // maintainer of the default project only
	projViewer         monitors.Actor // viewer of the default project only
	def, other         uuid.UUID
	specAll, specOther uuid.UUID // a Specula for every project, one for "other" only
	chDef, chOther     uuid.UUID // channels of the default and of the other project
}

func newWorld(t *testing.T, name string) *world {
	t.Helper()
	env := pgtest.New(t, pgtest.DBName(name))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin, pgtest.CappedPlanSQL,
		`INSERT INTO tenants (id, slug, name, plan_key) VALUES ('`+tenantA+`', 'a', 'A', 'internal'), ('`+tenantB+`', 'b', 'B', 'internal')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	pool := env.Pool(t, env.APIURL, 8)
	sched := scheduler.New()
	w := &world{t: t, ctx: ctx, admin: admin, other: uuid.New(),
		svc:   monitors.NewService(pool, sched, incidents.NewService(sched)).WithRequestKeys(keys),
		plain: monitors.NewService(pool, sched, incidents.NewService(sched))}
	tenant := uuid.MustParse(tenantA)
	if err := admin.QueryRow(ctx, `SELECT id FROM projects WHERE tenant_id = $1 AND is_default`, tenant).Scan(&w.def); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `INSERT INTO projects (id, tenant_id, name, slug) VALUES ('`+w.other.String()+`', '`+tenantA+`', 'Other', 'other')`)
	w.owner = monitors.Actor{TenantID: tenant, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	w.projMaint = monitors.Actor{TenantID: tenant, UserID: user, Access: authz.Actor{UserID: user, Projects: map[uuid.UUID]authz.Role{w.def: authz.Maintainer}}}
	w.projViewer = monitors.Actor{TenantID: tenant, UserID: user, Access: authz.Actor{UserID: user, Projects: map[uuid.UUID]authz.Role{w.def: authz.Viewer}}}

	sp := specula.NewService(pool)
	sa := specula.Actor{TenantID: tenant, UserID: user}
	all, _, err := sp.Create(ctx, sa, specula.CreateInput{Name: "all", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	only, _, err := sp.Create(ctx, sa, specula.CreateInput{Name: "only other", ProjectIDs: []uuid.UUID{w.other}})
	if err != nil {
		t.Fatal(err)
	}
	w.specAll, w.specOther = all.ID, only.ID

	ns := notify.NewService(pool, keys, nil, "https://proreta.example.com")
	na := notify.Actor{TenantID: tenant, UserID: user, Access: authz.Actor{Role: authz.Owner}}
	d, err := ns.Create(ctx, na, notify.CreateInput{Name: "d", WebhookURL: "https://hooks.slack.com/services/T1/B1/aaaa", ProjectID: &w.def})
	if err != nil {
		t.Fatal(err)
	}
	o, err := ns.Create(ctx, na, notify.CreateInput{Name: "o", WebhookURL: "https://hooks.slack.com/services/T2/B2/bbbb", ProjectID: &w.other})
	if err != nil {
		t.Fatal(err)
	}
	w.chDef, w.chOther = d.ID, o.ID
	return w
}

func (w *world) must(m monitors.Monitor, err error) monitors.Monitor {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) http(name string) monitors.Monitor {
	w.t.Helper()
	return w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: name, HTTP: &monitors.HTTPSettings{URL: ptr(publicURL)}}))
}

func (w *world) tcp(name string) monitors.Monitor {
	w.t.Helper()
	return w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Type: monitors.TypeTCP, Name: name, TCP: &monitors.TCPSettings{Host: ptr("example.com"), Port: ptr(22)}}))
}

func (w *world) heartbeat(name string) monitors.Monitor {
	w.t.Helper()
	return w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Type: monitors.TypeHeartbeat, Name: name, Heartbeat: &monitors.HeartbeatSettings{}}))
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.admin.QueryRow(w.ctx, q, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) audit(action string, id uuid.UUID) (meta map[string]any, n int) {
	w.t.Helper()
	n = w.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND entity_id = $2`, action, id.String())
	var raw []byte
	if err := w.admin.QueryRow(w.ctx, `SELECT metadata FROM audit_log WHERE action = $1 AND entity_id = $2 ORDER BY created_at DESC, id DESC LIMIT 1`, action, id.String()).Scan(&raw); err == nil {
		if err := json.Unmarshal(raw, &meta); err != nil {
			w.t.Fatal(err)
		}
	}
	return meta, n
}

func (w *world) locationCode(id uuid.UUID) string {
	w.t.Helper()
	var code string
	if err := w.admin.QueryRow(w.ctx, `SELECT p.code FROM monitor_location_state AS ml JOIN probe_locations AS p ON p.id = ml.location_id WHERE ml.monitor_id = $1`, id).Scan(&code); err != nil {
		w.t.Fatal(err)
	}
	return code
}

func (w *world) status(id uuid.UUID) string {
	w.t.Helper()
	var s string
	if err := w.admin.QueryRow(w.ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, id).Scan(&s); err != nil {
		w.t.Fatal(err)
	}
	return s
}

func stripDash(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "") }

func TestIntegrationCreateResultsAndDefaults(t *testing.T) {
	w := newWorld(t, "mon_create_defaults")
	m := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: "  web  ", HTTP: &monitors.HTTPSettings{URL: ptr("HTTPS://Example.COM/health")}}))
	if m.Name != "web" || m.URL != publicURL || m.ExpectedStatus != "2xx" || m.Method != "GET" || m.IntervalSeconds != 60 || m.TimeoutSeconds != 10 ||
		m.FailureThreshold != 2 || !m.NotificationsEnabled || m.Paused || m.Status != monitors.StatusUnknown || m.ProjectID != w.def || m.HeartbeatToken != "" ||
		m.Type != monitors.TypeHTTP || len(m.HeaderNames) != 0 || m.BodySet {
		t.Fatalf("http defaults: %+v", m)
	}
	if meta, n := w.audit(monitors.ActionCreated, m.ID); n != 1 || meta["type"] != "http" || meta["projectId"] != w.def.String() || len(meta) != 2 {
		t.Fatalf("audit = %v (%d)", meta, n)
	}
	if code := w.locationCode(m.ID); code != "default" || w.status(m.ID) != "unknown" {
		t.Fatalf("location %q", code)
	}

	m2 := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{
		Name: "api", NotificationsEnabled: ptr(false), ProjectID: &w.other,
		HTTP: &monitors.HTTPSettings{URL: ptr(publicURL), ExpectedStatus: ptr("200"), FailureThreshold: ptr(3), Method: ptr("HEAD")},
	}))
	if m2.NotificationsEnabled || m2.ExpectedStatus != "200" || m2.FailureThreshold != 3 || m2.Method != "HEAD" || m2.ProjectID != w.other {
		t.Fatalf("http explicit: %+v", m2)
	}

	// The request is sealed: names stay readable, values and body never appear in the row.
	m3 := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: "post", HTTP: &monitors.HTTPSettings{
		URL: ptr(publicURL), Method: ptr("POST"), Body: ptr("payload-body"), Headers: &[]monitors.HeaderInput{{Name: "X-Token", Value: ptr("secret-value")}},
	}}))
	if m3.Method != "POST" || len(m3.HeaderNames) != 1 || m3.HeaderNames[0] != "X-Token" || !m3.BodySet {
		t.Fatalf("request: %+v", m3)
	}
	var cfg string
	if err := w.admin.QueryRow(w.ctx, `SELECT config::text FROM monitors WHERE id = $1`, m3.ID).Scan(&cfg); err != nil || strings.Contains(cfg, "secret-value") || strings.Contains(cfg, "payload-body") || !strings.Contains(cfg, `"request"`) {
		t.Fatalf("config = %s %v", cfg, err)
	}
	// Without the keyring a request without headers and body is fine.
	if _, err := w.plain.Create(w.ctx, w.owner, monitors.CreateInput{Name: "plain post", HTTP: &monitors.HTTPSettings{URL: ptr(publicURL), Method: ptr("POST")}}); err != nil {
		t.Fatal(err)
	}

	tc := w.tcp("ssh")
	if tc.Type != monitors.TypeTCP || tc.Host != "example.com" || tc.Port != 22 || tc.FailureThreshold != 2 || tc.IntervalSeconds != 60 || tc.TimeoutSeconds != 10 || tc.URL != "" {
		t.Fatalf("tcp: %+v", tc)
	}
	hb := w.heartbeat("cron")
	if hb.Type != monitors.TypeHeartbeat || hb.HeartbeatToken == "" || hb.FailureThreshold != 1 || hb.IntervalSeconds != 300 || hb.GraceSeconds != 60 || hb.URL != "" {
		t.Fatalf("heartbeat: %+v", hb)
	}
	var hashed int
	if err := w.admin.QueryRow(w.ctx, `SELECT count(*) FROM heartbeat_tokens WHERE monitor_id = $1`, hb.ID).Scan(&hashed); err != nil || hashed != 1 {
		t.Fatalf("token rows %d %v", hashed, err)
	}
	if got := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Type: monitors.TypeHeartbeat, Name: "tuned", Heartbeat: &monitors.HeartbeatSettings{ExpectedIntervalSeconds: ptr(120), GraceSeconds: ptr(10)}})); got.IntervalSeconds != 120 || got.GraceSeconds != 10 {
		t.Fatalf("tuned heartbeat: %+v", got)
	}
}

func TestIntegrationCreateProjectsPermissionsAndChannels(t *testing.T) {
	w := newWorld(t, "mon_create_access")
	in := func(p *uuid.UUID, ch *[]uuid.UUID) monitors.CreateInput {
		return monitors.CreateInput{Name: "m", ProjectID: p, ChannelIDs: ch, HTTP: &monitors.HTTPSettings{URL: ptr(publicURL)}}
	}
	missing := uuid.New()
	before := func() int { return w.count(`SELECT count(*) FROM monitors`) }
	n := before()
	cases := []struct {
		name string
		a    monitors.Actor
		in   monitors.CreateInput
		want string // fields, or an error text prefix in <>
	}{
		{"unknown project", w.owner, in(&missing, nil), "projectId"},
		{"project the actor does not see", w.projMaint, in(&w.other, nil), "projectId"},
		{"viewer may not create", w.projViewer, in(nil, nil), "<" + authz.ErrProjectForbidden.Error() + ">"},
		{"channel of another project", w.owner, in(nil, &[]uuid.UUID{w.chOther}), "channelIds"},
		{"unknown channel", w.owner, in(nil, &[]uuid.UUID{uuid.New()}), "channelIds"},
		{"specula not shared with the project", w.owner, monitors.CreateInput{Name: "m", SpeculaID: &w.specOther, HTTP: &monitors.HTTPSettings{URL: ptr(privateURL)}}, "location.speculaId"},
		{"unknown specula", w.owner, monitors.CreateInput{Name: "m", SpeculaID: ptr(uuid.New()), HTTP: &monitors.HTTPSettings{URL: ptr(privateURL)}}, "location.speculaId"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := w.svc.Create(w.ctx, c.a, c.in)
			if got := fieldsOf(err); got != c.want || m.ID != uuid.Nil {
				t.Fatalf("got %q (%v), want %q", got, err, c.want)
			}
			// Everything the transaction wrote before the refusal is gone.
			if got := before(); got != n {
				t.Fatalf("%d monitors, want %d", got, n)
			}
		})
	}
	if w.count(`SELECT count(*) FROM audit_log WHERE action = 'monitor.created'`) != 0 {
		t.Fatal("a refused create left an audit row")
	}

	// Permitted combinations.
	m := w.must(w.svc.Create(w.ctx, w.projMaint, in(nil, &[]uuid.UUID{w.chDef})))
	if m.ProjectID != w.def {
		t.Fatalf("default project: %+v", m)
	}
	if got := w.count(`SELECT count(*) FROM monitor_channels WHERE monitor_id = $1 AND channel_id = $2`, m.ID, w.chDef); got != 1 {
		t.Fatalf("channel links = %d", got)
	}
	if m := w.must(w.svc.Create(w.ctx, w.owner, in(&w.other, &[]uuid.UUID{w.chOther}))); m.ProjectID != w.other {
		t.Fatalf("other project: %+v", m)
	}
	// A Specula takes private targets and runs only there.
	sm := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: "inside", SpeculaID: &w.specAll, HTTP: &monitors.HTTPSettings{URL: ptr(privateURL)}}))
	if code := w.locationCode(sm.ID); code != "specula-"+stripDash(w.specAll) || sm.URL != privateURL {
		t.Fatalf("specula location %q url %q", code, sm.URL)
	}
	tm := w.must(w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Type: monitors.TypeTCP, Name: "inside tcp", SpeculaID: &w.specAll, TCP: &monitors.TCPSettings{Host: ptr("10.0.0.7"), Port: ptr(5432)}}))
	if tm.Host != "10.0.0.7" {
		t.Fatalf("tcp at specula: %+v", tm)
	}
}

func TestIntegrationCreateQuotaComesFirst(t *testing.T) {
	w := newWorld(t, "mon_create_quota")
	pgtest.MustExec(t, w.admin, `UPDATE tenants SET plan_key = 'capped' WHERE id = '`+tenantA+`'`)
	for i := 0; i < 3; i++ {
		w.http("m")
	}
	var lerr *plans.LimitError
	missing := uuid.New()
	// The cap on monitors is checked before the project, so a full quota hides a bad project id.
	_, err := w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: "m", ProjectID: &missing, HTTP: &monitors.HTTPSettings{URL: ptr(publicURL)}})
	if !errors.As(err, &lerr) || lerr.Resource != plans.ResourceMonitor || err != error(lerr) {
		t.Fatalf("http over the cap: %T %v", err, err)
	}
	if _, err := w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Type: monitors.TypeTCP, Name: "m", TCP: &monitors.TCPSettings{Host: ptr("example.com"), Port: ptr(1)}}); !errors.As(err, &lerr) {
		t.Fatalf("tcp over the cap: %v", err)
	}
	// Heartbeats have their own cap (one on this plan).
	w.heartbeat("hb")
	_, err = w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Type: monitors.TypeHeartbeat, Name: "hb2", Heartbeat: &monitors.HeartbeatSettings{}})
	if !errors.As(err, &lerr) || lerr.Resource != plans.ResourceHeartbeat {
		t.Fatalf("second heartbeat: %v", err)
	}
	// Validation comes before the quota.
	if _, err := w.svc.Create(w.ctx, w.owner, monitors.CreateInput{Name: ""}); fieldsOf(err) != "name,http" {
		t.Fatalf("validation vs quota: %v", err)
	}
}
