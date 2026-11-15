package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// hello and reports reuse the schedule version for a few seconds instead of decrypting the schedule
// on every call, and a change shows up once the cache entry has expired.
func TestIntegrationScheduleVersionCache(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculaversion"))
	ctx := context.Background()
	admin := pg.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool := pg.Pool(t, pg.APIURL, 4)
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc)
	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, token, err := specula.NewService(apiPool).Create(ctx, actor, specula.CreateInput{Name: "A", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	macts := monitors.Actor{TenantID: actor.TenantID, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	url := "https://api.internal.example.com/"
	m, err := mon.Create(ctx, macts, monitors.CreateInput{Name: "API", SpeculaID: &s.ID, HTTP: &monitors.HTTPSettings{URL: &url}})
	if err != nil {
		t.Fatal(err)
	}

	clk := clock.NewFake(time.Now())
	reg := prometheus.NewRegistry()
	srv := httptest.NewServer(gateway.New(gateway.Deps{
		Limits: openLimits, Clock: clk, Pool: pg.Pool(t, pg.WorkerURL, 4), Scheduler: sched, Registerer: reg,
		Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clk},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(srv.Close)

	hello := func() string {
		t.Helper()
		code, b := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", Capabilities: []string{"schedule/1"}})
		var out wire.HelloResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("hello: %d %s", code, b)
		}
		return out.ScheduleVersion
	}
	reportsVersion := func() string { // a report of a monitor that is not there is only rejected
		t.Helper()
		code, b := call(t, srv, token, "/reports", wire.ReportsRequest{Results: []wire.Report{{MonitorID: uuid.New(), Epoch: 1, Slot: 60, OK: true}}})
		var out wire.ReportsResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("reports: %d %s", code, b)
		}
		return out.ScheduleVersion
	}
	builds := func() float64 { return metricValue(t, reg, "proreta_specula_schedule_build_seconds") }

	v1 := hello()
	if v1 == "" || builds() != 1 {
		t.Fatalf("first hello: version %q after %v builds", v1, builds())
	}
	if hello() != v1 || reportsVersion() != v1 || builds() != 1 {
		t.Errorf("hello and reports must reuse the cached version: %v builds", builds())
	}
	if _, err := mon.Pause(ctx, macts, m.ID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(4 * time.Second)
	if hello() != v1 {
		t.Error("the version changed inside the cache window")
	}
	clk.Advance(2 * time.Second)
	v2 := hello()
	if v2 == v1 || reportsVersion() != v2 || builds() != 2 {
		t.Errorf("after the window: hello %q, want a new version; %v builds", v2, builds())
	}
}
