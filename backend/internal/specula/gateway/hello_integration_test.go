package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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

// The latest hello decides what a Specula gets from claim: tcp jobs only after it announced tcp,
// nothing once it announces schedule/1, and a downgraded executor is served again at its next hello.
func TestIntegrationHelloCapabilities(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculahello"))
	ctx := context.Background()
	admin := pg.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
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
	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, token, err := svc.Create(ctx, actor, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	macts := monitors.Actor{TenantID: actor.TenantID, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	url, host, port, threshold := "https://api.internal.example.com/", "db.internal", 5432, 1
	if _, err := mon.Create(ctx, macts, monitors.CreateInput{Name: "API", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url}}); err != nil {
		t.Fatal(err)
	}
	if _, err := mon.Create(ctx, macts, monitors.CreateInput{Type: monitors.TypeTCP, Name: "DB", SpeculaID: &s.ID,
		TCP: &monitors.TCPSettings{Host: &host, Port: &port, FailureThreshold: &threshold}}); err != nil {
		t.Fatal(err)
	}

	h := gateway.New(gateway.Deps{
		Limits: openLimits,
		Pool:   worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		RequestKeys: keys, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	hello := func(in any) wire.HelloResponse {
		t.Helper()
		code, b := call(t, srv, token, "/hello", in)
		var out wire.HelloResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("hello: %d %s", code, b)
		}
		return out
	}
	// claimed returns the monitor types the next claim hands out, with every monitor due again.
	claimed := func() []string {
		t.Helper()
		pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET lease_token = NULL, lease_until = NULL, next_run_at = now() - interval '1 minute'`)
		code, b := call(t, srv, token, "/claim", wire.ClaimRequest{Max: 10})
		var out wire.ClaimResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("claim: %d %s", code, b)
		}
		var types []string
		for _, j := range out.Jobs {
			if j.Type == "" {
				j.Type = wire.TypeHTTP
			}
			types = append(types, j.Type)
		}
		slices.Sort(types)
		return types
	}
	stored := func() (caps []string, instance *string, depth *int, degraded bool) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT capabilities, instance_id, buffer_depth, buffer_degraded FROM specula_agents WHERE id = $1`, s.ID).
			Scan(&caps, &instance, &depth, &degraded); err != nil {
			t.Fatal(err)
		}
		return
	}
	want := func(step string, got []string, exp ...string) {
		t.Helper()
		if !slices.Equal(got, exp) {
			t.Errorf("%s: claimed %v, want %v", step, got, exp)
		}
	}

	// An executor that announces nothing: pull mode, http only, as before.
	if out := hello(wire.HelloRequest{Version: "0.1.0", Hostname: "pod"}); !slices.Equal(out.Modes, []string{wire.ModePull}) {
		t.Errorf("old executor modes %v", out.Modes)
	}
	want("no capabilities", claimed(), "http")

	// tcp announced, with the telemetry fields: stored, tcp is claimed again.
	issued, oldest, depth, degraded := time.Now().Add(-time.Minute).UTC().Truncate(time.Second), time.Now().Add(-time.Hour).UTC().Truncate(time.Second), 7, true
	out := hello(wire.HelloRequest{Version: "0.2.0", Hostname: "pod", Capabilities: []string{"tcp", "tcp", "", "future/9"}, InstanceID: "inst-1",
		ScheduleIssuedAt: &issued, BufferDepth: &depth, OldestUnsentAt: &oldest, BufferDegraded: &degraded})
	if !slices.Equal(out.Modes, []string{wire.ModePull}) {
		t.Errorf("tcp executor modes %v", out.Modes)
	}
	caps, instance, gotDepth, gotDegraded := stored()
	if !slices.Equal(caps, []string{"tcp", "future/9"}) || instance == nil || *instance != "inst-1" || gotDepth == nil || *gotDepth != 7 || !gotDegraded {
		t.Errorf("stored %v %v %v %v", caps, instance, gotDepth, gotDegraded)
	}
	var at time.Time
	if err := admin.QueryRow(ctx, `SELECT schedule_issued_at FROM specula_agents WHERE id = $1`, s.ID).Scan(&at); err != nil || !at.Equal(issued) {
		t.Errorf("scheduleIssuedAt %v %v", at, err)
	}
	want("tcp announced", claimed(), "http", "tcp")

	// schedule/1: the location is autonomous, claim hands out nothing.
	if out := hello(wire.HelloRequest{Version: "0.3.0", Hostname: "pod", Capabilities: []string{"tcp", "schedule/1"}}); !slices.Equal(out.Modes, []string{wire.ModeSchedule}) {
		t.Errorf("schedule modes %v", out.Modes)
	}
	want("schedule/1", claimed())

	// Downgrade: the next hello without schedule/1 is back in pull mode, one without tcp loses tcp.
	if out := hello(wire.HelloRequest{Version: "0.2.0", Hostname: "pod", Capabilities: []string{"tcp"}}); !slices.Equal(out.Modes, []string{wire.ModePull}) {
		t.Errorf("downgrade modes %v", out.Modes)
	}
	want("downgrade to pull", claimed(), "http", "tcp")
	hello(wire.HelloRequest{Version: "0.1.0", Hostname: "pod"})
	want("downgrade without tcp", claimed(), "http")
	if caps, instance, gotDepth, gotDegraded := stored(); len(caps) != 0 || instance != nil || gotDepth != nil || gotDegraded {
		t.Errorf("an old hello must clear the previous fields: %v %v %v %v", caps, instance, gotDepth, gotDegraded)
	}

	// Bad values answer 400 and change nothing.
	neg := -1
	if code, _ := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", BufferDepth: &neg}); code != http.StatusBadRequest {
		t.Errorf("negative bufferDepth: %d", code)
	}
}
