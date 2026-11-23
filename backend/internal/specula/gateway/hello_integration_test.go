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

	reg := prometheus.NewRegistry()
	h := gateway.New(gateway.Deps{
		Limits: openLimits,
		Pool:   worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		RequestKeys: keys, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: reg,
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

	// The clock offset is stored; two instance ids within three minutes count once, a restart not.
	offset := int64(90_000)
	hello(wire.HelloRequest{Version: "0.3.0", Hostname: "pod", InstanceID: "inst-a", ClockOffsetMs: &offset})
	hello(wire.HelloRequest{Version: "0.3.0", Hostname: "pod", InstanceID: "inst-b"})
	hello(wire.HelloRequest{Version: "0.3.0", Hostname: "pod", InstanceID: "inst-b"})
	if n := metricValue(t, reg, "proreta_specula_duplicate_instance_total"); n != 1 {
		t.Errorf("duplicate_instance_total = %v, want 1", n)
	}
	var stored64 *int64
	if err := admin.QueryRow(ctx, `SELECT clock_offset_ms FROM specula_agents WHERE id = $1`, s.ID).Scan(&stored64); err != nil || stored64 != nil {
		t.Errorf("the latest hello sent no offset: %v %v", stored64, err)
	}
	hello(wire.HelloRequest{Version: "0.3.0", Hostname: "pod", InstanceID: "inst-b", ClockOffsetMs: &offset})
	if err := admin.QueryRow(ctx, `SELECT clock_offset_ms FROM specula_agents WHERE id = $1`, s.ID).Scan(&stored64); err != nil || stored64 == nil || *stored64 != 90_000 {
		t.Errorf("clock offset %v %v", stored64, err)
	}
	wild := int64(wire.MaxClockOffsetMs + 1)
	if code, _ := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", ClockOffsetMs: &wild}); code != http.StatusBadRequest {
		t.Errorf("clock offset out of range: %d", code)
	}

	// schedule/2: offered next to schedule/1 only to an executor that announced it, counters are
	// recorded as deltas and counted in the metric; schedule/2 alone is not a schedule mode.
	caps2 := []string{"tcp", "schedule/1", "schedule/2"}
	if out := hello(wire.HelloRequest{Version: "0.4.0", Hostname: "pod", Capabilities: []string{"tcp", "schedule/1"}}); !slices.Equal(out.Modes, []string{wire.ModeSchedule}) {
		t.Errorf("a schedule/1 executor is offered %v", out.Modes)
	}
	if out := hello(wire.HelloRequest{Version: "0.4.0", Hostname: "pod", Capabilities: []string{"tcp", "schedule/2"}}); !slices.Equal(out.Modes, []string{wire.ModePull}) {
		t.Errorf("schedule/2 without schedule/1 is offered %v", out.Modes)
	}
	if out := hello(wire.HelloRequest{Version: "0.4.0", Hostname: "pod", Capabilities: caps2, InstanceID: "s2"}); !slices.Equal(out.Modes, []string{wire.ModeSchedule, wire.ModeSchedule2}) {
		t.Errorf("a schedule/2 executor is offered %v", out.Modes)
	}
	counters := func(busy, tooOld int64) *wire.SlotCounters {
		return &wire.SlotCounters{Missed: wire.MissedCounters{Busy: busy}, Dropped: wire.DroppedCounters{TooOld: tooOld}}
	}
	hello(wire.HelloRequest{Version: "0.4.0", Hostname: "pod", Capabilities: caps2, InstanceID: "s2", SlotCounters: counters(4, 1)})
	hello(wire.HelloRequest{Version: "0.4.0", Hostname: "pod", Capabilities: caps2, InstanceID: "s2", SlotCounters: counters(7, 1)})
	if n := metricValue(t, reg, "proreta_specula_slot_losses_total", "kind", "missed", "reason", "busy"); n != 7 {
		t.Errorf("slot_losses_total missed/busy = %v, want 7", n)
	}
	if n := metricValue(t, reg, "proreta_specula_slot_losses_total", "kind", "dropped", "reason", "tooOld"); n != 1 {
		t.Errorf("slot_losses_total dropped/tooOld = %v, want 1", n)
	}
	var rows int64
	if err := admin.QueryRow(ctx, `SELECT sum(count) FROM specula_slot_losses WHERE specula_id = $1 AND reason = 'busy'`, s.ID).Scan(&rows); err != nil || rows != 7 {
		t.Errorf("stored busy %d %v", rows, err)
	}
	for name, in := range map[string]any{
		"counters without the capability": wire.HelloRequest{Version: "1", Capabilities: []string{"tcp", "schedule/1"}, SlotCounters: counters(1, 0)},
		"an unknown counter":              map[string]any{"version": "1", "hostname": "h", "capabilities": caps2, "slotCounters": map[string]any{"missed": map[string]any{"bogus": 1}, "dropped": map[string]any{}}},
	} {
		if code, _ := call(t, srv, token, "/hello", in); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", Capabilities: caps2, SlotCounters: counters(wire.MaxSlotCounter, 0)}); code != http.StatusOK {
		t.Errorf("a counter at the maximum: %d", code)
	}
	// A counter outside the range is left out and counted; the hello itself is not refused.
	for name, v := range map[string]int64{"a negative counter": -1, "a counter over the maximum": wire.MaxSlotCounter + 1} {
		if code, _ := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", Capabilities: caps2, SlotCounters: counters(v, 0)}); code != http.StatusOK {
			t.Errorf("%s: %d, want 200", name, code)
		}
	}
	if n := metricValue(t, reg, "proreta_specula_slot_counters_ignored_total", "reason", "busy"); n != 2 {
		t.Errorf("slot_counters_ignored_total busy = %v, want 2", n)
	}

	// Bad values answer 400 and change nothing.
	neg := -1
	if code, _ := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", BufferDepth: &neg}); code != http.StatusBadRequest {
		t.Errorf("negative bufferDepth: %d", code)
	}
}
