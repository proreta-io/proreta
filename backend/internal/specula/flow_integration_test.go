package specula_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/safehttp"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/executor"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationSpeculaFlow runs the whole path: an executor with the private client checks a
// plain-http service on a non-standard port (which the hosted worker would refuse), reports
// through the gateway, and the incident opens and resolves like for any other check.
func TestIntegrationSpeculaFlow(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculaflow"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool, worker := env.Pool(t, env.APIURL, 4), env.Pool(t, env.WorkerURL, 4)
	keys, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{8}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc).WithRequestKeys(keys)
	svc := specula.NewService(apiPool)

	// The private service: plain http, a high port, a secret header it must receive.
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	var gotKey atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey.Store(r.Header.Get("X-Api-Key"))
		w.WriteHeader(int(status.Load()))
	}))
	defer target.Close()

	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, token, err := svc.Create(ctx, actor, specula.CreateInput{Name: "Prod cluster", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	url, threshold, key := "https://placeholder.example.com/health", 1, "internal-key"
	hdrs := []monitors.HeaderInput{{Name: "X-Api-Key", Value: &key}}
	m, err := mon.Create(ctx, monitors.Actor{TenantID: actor.TenantID, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}},
		monitors.CreateInput{Name: "Internal", HTTP: &monitors.HTTPSettings{URL: &url, FailureThreshold: &threshold, Headers: &hdrs}})
	if err != nil {
		t.Fatal(err)
	}
	// Monitor URLs are validated as public https today; a Specula target is private. Point the
	// stored target at the local service and run the monitor at the Specula's location.
	pgtest.MustExec(t, admin,
		`DELETE FROM monitor_location_state WHERE monitor_id = '`+m.ID.String()+`'`,
		`DELETE FROM monitor_locations WHERE monitor_id = '`+m.ID.String()+`'`,
		`UPDATE monitors SET config = jsonb_set(config, '{url}', to_jsonb('`+target.URL+`/health'::text)) WHERE id = '`+m.ID.String()+`'`,
		`INSERT INTO monitor_locations (tenant_id, monitor_id, location_id) VALUES ('`+tenantA+`', '`+m.ID.String()+`', '`+s.LocationID.String()+`')`,
		`INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at) VALUES ('`+tenantA+`', '`+m.ID.String()+`', '`+s.LocationID.String()+`', now() - interval '1 minute')`)

	gw := httptest.NewServer(gateway.New(gateway.Deps{
		Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		RequestKeys: keys, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	}))
	defer gw.Close()
	exec := executor.New(executor.Config{
		BaseURL: gw.URL, Token: token, Version: "test", Hostname: "pod-1", Concurrency: 4, PollInterval: 100 * time.Millisecond,
		Prober: checks.NewHTTPSProber(safehttp.NewPrivateClient(safehttp.Options{MaxRedirects: 3})), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- exec.Run(runCtx) }()
	defer func() { stop(); <-done }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	openIncidents := func() (n int) {
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE monitor_id = $1 AND resolved_at IS NULL`, m.ID).Scan(&n)
		return n
	}
	due := func() {
		pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 second' WHERE monitor_id = '`+m.ID.String()+`' AND lease_token IS NULL`)
	}

	waitFor("an incident for the failing service", func() bool { return openIncidents() == 1 })
	var status1 string
	if err := admin.QueryRow(ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&status1); err != nil || status1 != "down" {
		t.Fatalf("status %q %v", status1, err)
	}
	if got, _ := gotKey.Load().(string); got != "internal-key" {
		t.Fatalf("the service received header %q, want the sealed one", got)
	}
	var code *int
	if err := admin.QueryRow(ctx, `SELECT status_code FROM check_results WHERE monitor_id = $1 ORDER BY checked_at DESC LIMIT 1`, m.ID).Scan(&code); err != nil || code == nil || *code != 503 {
		t.Fatalf("stored status %v %v", code, err)
	}

	status.Store(http.StatusOK)
	waitFor("the monitor to recover", func() bool {
		due()
		return openIncidents() == 0
	})
	if err := admin.QueryRow(ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&status1); err != nil || status1 != "up" {
		t.Fatalf("status after recovery %q %v", status1, err)
	}
	var seenAt *time.Time
	if err := admin.QueryRow(ctx, `SELECT last_seen_at FROM specula_agents WHERE id = $1`, s.ID).Scan(&seenAt); err != nil || seenAt == nil {
		t.Fatalf("last seen %v %v", seenAt, err)
	}

	// Revoking stops it: the executor ends with a rejected token.
	if err := svc.Revoke(ctx, actor, s.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != executor.ErrRejected {
			t.Fatalf("executor ended with %v", err)
		}
		done <- nil
	case <-time.After(15 * time.Second):
		t.Fatal("the executor kept running after the token was revoked")
	}
}
