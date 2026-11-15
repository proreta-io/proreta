package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// answer is the status and headers of a finished response.
type answer struct {
	StatusCode int
	Header     http.Header
}

func getSchedule(t *testing.T, srv *httptest.Server, token, inm string) (answer, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+wire.PathSchedule, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header}, b
}

func TestIntegrationSchedule(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculasched"))
	ctx := context.Background()
	admin := pg.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool, worker := pg.Pool(t, pg.APIURL, 4), pg.Pool(t, pg.WorkerURL, 4)
	keys, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{6}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc).WithRequestKeys(keys)
	svc := specula.NewService(apiPool)
	actor := func(tenant string) monitors.Actor {
		return monitors.Actor{TenantID: uuid.MustParse(tenant), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	}
	sa, tokenA, err := svc.Create(ctx, specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}, specula.CreateInput{Name: "A", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	sb, tokenB, err := svc.Create(ctx, specula.Actor{TenantID: uuid.MustParse(tenantB), UserID: user}, specula.CreateInput{Name: "B", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	const secretHeader, secretBody = "Bearer s3cr3t-header-value", `{"s3cr3t":"body-value"}`
	url, host, port, threshold := "https://api.internal.example.com/", "db.internal", 5432, 1
	method, body := "POST", secretBody
	// Header order is deliberately not alphabetical.
	zv, av := secretHeader, "a-value"
	hdrs := []monitors.HeaderInput{{Name: "X-Z", Value: &zv}, {Name: "X-A", Value: &av}}
	api, err := mon.Create(ctx, actor(tenantA), monitors.CreateInput{Name: "API", SpeculaID: &sa.ID,
		HTTP: &monitors.HTTPSettings{URL: &url, Method: &method, Headers: &hdrs, Body: &body}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mon.Create(ctx, actor(tenantA), monitors.CreateInput{Type: monitors.TypeTCP, Name: "DB", SpeculaID: &sa.ID,
		TCP: &monitors.TCPSettings{Host: &host, Port: &port, FailureThreshold: &threshold}}); err != nil {
		t.Fatal(err)
	}
	foreign := "https://other-org.example.com/"
	if _, err := mon.Create(ctx, actor(tenantB), monitors.CreateInput{Name: "Other", SpeculaID: &sb.ID, HTTP: &monitors.HTTPSettings{URL: &foreign}}); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	build := func(k *crypto.Keyring) *httptest.Server {
		h := gateway.New(gateway.Deps{
			Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
			RequestKeys: k, Log: slog.New(slog.NewTextHandler(&logs, nil)), Registerer: prometheus.NewRegistry(), ScheduleTTL: time.Hour,
		})
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv
	}
	srv := build(keys)
	fetch := func(inm string) (answer, wire.Schedule) {
		t.Helper()
		resp, b := getSchedule(t, srv, tokenA, inm)
		var s wire.Schedule
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(b, &s); err != nil {
				t.Fatalf("schedule body: %v", err)
			}
		}
		return resp, s
	}

	// An authenticated GET: the executor's own monitors only, secrets opened, canonical order.
	resp, first := fetch("")
	if resp.StatusCode != http.StatusOK || len(first.Monitors) != 2 {
		t.Fatalf("schedule: %d with %d monitors", resp.StatusCode, len(first.Monitors))
	}
	if resp.Header.Get("ETag") != `"`+first.Version+`"` || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", resp.Header)
	}
	if got := first.ValidUntil.Sub(first.IssuedAt); got != time.Hour || first.RefreshAfterSeconds < 1 {
		t.Errorf("validUntil-issuedAt %v refresh %d", got, first.RefreshAfterSeconds)
	}
	var gotAPI, gotTCP *wire.ScheduledMonitor
	for i, m := range first.Monitors {
		if i > 0 && first.Monitors[i-1].MonitorID.String() >= m.MonitorID.String() {
			t.Errorf("monitors not sorted by id")
		}
		switch {
		case m.MonitorID == api.ID:
			gotAPI = &first.Monitors[i]
		case m.Type == wire.TypeTCP:
			gotTCP = &first.Monitors[i]
		default:
			t.Errorf("a monitor of another organization is in the schedule: %v", m.URL)
		}
	}
	if gotAPI == nil || gotTCP == nil {
		t.Fatalf("missing monitors: %+v", first.Monitors)
	}
	if gotAPI.URL != url || gotAPI.Method != "POST" || gotAPI.Body != secretBody || gotAPI.Epoch < 1 || !gotAPI.Active || gotAPI.IntervalSeconds < 1 ||
		len(gotAPI.Headers) != 2 || gotAPI.Headers[0].Name != "X-A" || gotAPI.Headers[1].Value != secretHeader {
		t.Errorf("http monitor %+v", *gotAPI)
	}
	if gotTCP.Host != host || gotTCP.Port != port || gotTCP.URL != "" || gotTCP.Method != "" {
		t.Errorf("tcp monitor %+v", *gotTCP)
	}

	// Same state, same version, in the hello answer too; a conditional request is a 304 that extends validUntil.
	_, again := fetch("")
	if again.Version != first.Version {
		t.Errorf("version changed without a change: %s %s", first.Version, again.Version)
	}
	code, b := call(t, srv, tokenA, "/hello", wire.HelloRequest{Version: "1", Hostname: "pod", Capabilities: []string{"schedule/1"}})
	var hello wire.HelloResponse
	if code != http.StatusOK || json.Unmarshal(b, &hello) != nil || hello.ScheduleVersion != first.Version {
		t.Errorf("hello: %d %s, want scheduleVersion %s", code, b, first.Version)
	}
	for _, inm := range []string{`"` + first.Version + `"`, `W/"` + first.Version + `"`, `"other", "` + first.Version + `"`} {
		resp, raw := getSchedule(t, srv, tokenA, inm)
		until, perr := time.Parse(time.RFC3339, resp.Header.Get(wire.HeaderScheduleValidUntil))
		if resp.StatusCode != http.StatusNotModified || len(raw) != 0 || perr != nil || !until.After(time.Now().Add(50*time.Minute)) {
			t.Errorf("If-None-Match %s: %d %q until=%q", inm, resp.StatusCode, raw, resp.Header.Get(wire.HeaderScheduleValidUntil))
		}
	}
	if resp, _ := fetch(`"stale"`); resp.StatusCode != http.StatusOK {
		t.Errorf("a stale ETag: %d", resp.StatusCode)
	}

	// Another organization's executor sees only its own monitor.
	respB, bb := getSchedule(t, srv, tokenB, "")
	var schedB wire.Schedule
	if respB.StatusCode != http.StatusOK || json.Unmarshal(bb, &schedB) != nil || len(schedB.Monitors) != 1 || schedB.Monitors[0].URL != foreign {
		t.Errorf("tenant B schedule: %d %s", respB.StatusCode, bb)
	}
	if strings.Contains(string(bb), secretHeader) || strings.Contains(string(bb), "api.internal") {
		t.Error("tenant B received data of tenant A")
	}

	// Authentication: no token and a made-up one are the same 401; a pull-mode GET is not a POST route.
	for _, tok := range []string{"", specula.TokenPrefix + "nope"} {
		if resp, _ := getSchedule(t, srv, tok, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: %d", tok, resp.StatusCode)
		}
	}

	// A pause changes the version; so does a body change (secrets are part of what the executor runs).
	if _, err := mon.Pause(ctx, actor(tenantA), api.ID); err != nil {
		t.Fatal(err)
	}
	_, paused := fetch("")
	if len(paused.Monitors) != 1 || paused.Version == first.Version {
		t.Errorf("after pause: %d monitors, version %s", len(paused.Monitors), paused.Version)
	}
	if _, err := mon.Resume(ctx, actor(tenantA), api.ID); err != nil {
		t.Fatal(err)
	}
	newBody := `{"changed":true}`
	if _, err := mon.Update(ctx, actor(tenantA), api.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{Body: &newBody}}); err != nil {
		t.Fatal(err)
	}
	_, edited := fetch("")
	if len(edited.Monitors) != 2 || edited.Version == first.Version || edited.Version == paused.Version {
		t.Errorf("after edit: %d monitors, version %s", len(edited.Monitors), edited.Version)
	}

	// A monitor whose request cannot be opened is left out and logged, never with values.
	wrong, _ := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{9}, 32)}, "r1")
	broken := build(wrong)
	resp2, bw := getSchedule(t, broken, tokenA, "")
	var sw wire.Schedule
	if resp2.StatusCode != http.StatusOK || json.Unmarshal(bw, &sw) != nil || len(sw.Monitors) != 1 || sw.Monitors[0].Type != wire.TypeTCP {
		t.Errorf("wrong keys: %d %s", resp2.StatusCode, bw)
	}
	if !strings.Contains(logs.String(), "cannot open the sealed request") {
		t.Error("the unreadable monitor was not logged")
	}
	for _, secret := range []string{secretHeader, secretBody, newBody, "a-value"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("a secret appears in the logs: %q", secret)
		}
	}

	// A revoked executor gets 401 (a fresh handler has no token cache) and an old cache entry
	// would still not yield anything: the function returns nothing for a revoked Specula.
	if err := svc.Revoke(ctx, specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}, sa.ID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := getSchedule(t, build(keys), tokenA, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked, new handler: %d", resp.StatusCode)
	}
	if resp, bs := getSchedule(t, srv, tokenA, ""); resp.StatusCode == http.StatusOK && strings.Contains(string(bs), `"monitorId"`) {
		t.Errorf("revoked executor still served monitors: %s", bs)
	}
}

// More than 5000 monitors at one location: 413, and no version in hello.
func TestIntegrationScheduleCap(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculacap"))
	ctx := context.Background()
	admin := pg.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool, worker := pg.Pool(t, pg.APIURL, 4), pg.Pool(t, pg.WorkerURL, 4)
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc)
	s, token, err := specula.NewService(apiPool).Create(ctx, specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}, specula.CreateInput{Name: "A", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://api.internal.example.com/"
	m, err := mon.Create(ctx, monitors.Actor{TenantID: uuid.MustParse(tenantA), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}},
		monitors.CreateInput{Name: "seed", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url}})
	if err != nil {
		t.Fatal(err)
	}
	// Clone the monitor, its locations and its state 5000 more times (5001 in all); the state
	// insert opens the epochs.
	id := "'" + m.ID.String() + "'"
	pgtest.MustExec(t, admin,
		`CREATE TEMP TABLE cm AS SELECT x.*, generate_series(1, 5000) AS n FROM monitors x WHERE x.id = `+id,
		`ALTER TABLE cm ADD COLUMN old_id uuid`,
		`UPDATE cm SET old_id = id, id = gen_random_uuid(), name = 'clone-' || n`,
		`CREATE TEMP TABLE map AS SELECT id AS new_id, old_id FROM cm`,
		`ALTER TABLE cm DROP COLUMN n, DROP COLUMN old_id`,
		`INSERT INTO monitors SELECT * FROM cm`,
		`CREATE TEMP TABLE cl AS SELECT l.*, map.new_id AS nid FROM monitor_locations l, map WHERE l.monitor_id = `+id,
		`UPDATE cl SET monitor_id = nid`, `ALTER TABLE cl DROP COLUMN nid`, `INSERT INTO monitor_locations SELECT * FROM cl`,
		`CREATE TEMP TABLE cs AS SELECT s.*, map.new_id AS nid FROM monitor_location_state s, map WHERE s.monitor_id = `+id,
		`UPDATE cs SET monitor_id = nid`, `ALTER TABLE cs DROP COLUMN nid`, `INSERT INTO monitor_location_state SELECT * FROM cs`)

	srv := httptest.NewServer(gateway.New(gateway.Deps{
		Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	}))
	t.Cleanup(srv.Close)
	if resp, b := getSchedule(t, srv, token, ""); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("5001 monitors: %d %.200s", resp.StatusCode, b)
	}
	code, b := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", Capabilities: []string{"schedule/1"}})
	var hello wire.HelloResponse
	if code != http.StatusOK || json.Unmarshal(b, &hello) != nil || hello.ScheduleVersion != "" || len(hello.Modes) != 1 || hello.Modes[0] != wire.ModeSchedule {
		t.Errorf("hello over the cap: %d %s", code, b)
	}
	// One monitor fewer is served.
	pgtest.MustExec(t, admin, `UPDATE monitors SET paused = true WHERE id = (SELECT new_id FROM map LIMIT 1)`)
	if resp, _ := getSchedule(t, srv, token, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("5000 monitors: %d", resp.StatusCode)
	}
}
