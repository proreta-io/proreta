package specula_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strconv"
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
	"github.com/proreta-io/proreta/backend/internal/safehttp"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/executor"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// A tcp monitor at a Specula: a private target (loopback, any port) is accepted, the executor
// connects, the incident opens when the port is closed and resolves when it is open again. A tcp
// monitor from Proreta's own worker is claimed as a tcp job and refuses the same private target.
func TestIntegrationSpeculaTCPFlow(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculatcp"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	apiPool, worker := env.Pool(t, env.APIURL, 4), env.Pool(t, env.WorkerURL, 4)
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc)
	svc := specula.NewService(apiPool)
	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	macts := monitors.Actor{TenantID: actor.TenantID, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}

	s, token, err := svc.Create(ctx, actor, specula.CreateInput{Name: "Prod cluster", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	// A free port: nothing listens until the test opens it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	host, threshold := "127.0.0.1", 1
	m, err := mon.Create(ctx, macts, monitors.CreateInput{
		Type: monitors.TypeTCP, Name: "Internal database", SpeculaID: &s.ID,
		TCP: &monitors.TCPSettings{Host: &host, Port: &port, FailureThreshold: &threshold},
	})
	if err != nil {
		t.Fatalf("a private target at a Specula must be accepted: %v", err)
	}
	if _, err := mon.Create(ctx, macts, monitors.CreateInput{
		Type: monitors.TypeTCP, Name: "Cloud", TCP: &monitors.TCPSettings{Host: &host, Port: &port},
	}); err == nil {
		t.Fatal("a private target from Proreta's own worker must be refused")
	}
	pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 minute' WHERE monitor_id = '`+m.ID.String()+`'`)

	gw := httptest.NewServer(gateway.New(gateway.Deps{
		Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	}))
	defer gw.Close()
	exec := executor.New(executor.Config{
		BaseURL: gw.URL, Token: token, Version: "test", Hostname: "pod-1", Concurrency: 4, PollInterval: 100 * time.Millisecond,
		Prober: checks.Mux{
			HTTP: checks.NewHTTPSProber(safehttp.NewPrivateClient(safehttp.Options{MaxRedirects: 3})),
			TCP:  checks.NewTCPProber(safehttp.NewPrivateDialer(0)),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- exec.Run(runCtx) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Logf("executor stopped: %v", err)
		}
	}()

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
	waitFor("an incident for the closed port", func() bool { return openIncidents() == 1 })
	var kind *string
	var code *int
	if err := admin.QueryRow(ctx, `SELECT error_kind, status_code FROM check_results WHERE monitor_id = $1 ORDER BY checked_at DESC LIMIT 1`, m.ID).Scan(&kind, &code); err != nil || kind == nil || *kind != checks.KindConnect || code != nil {
		t.Fatalf("stored failure %v %v %v", kind, code, err)
	}

	open, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Skipf("the port was taken meanwhile: %v", err)
	}
	defer open.Close()
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	waitFor("the monitor to recover", func() bool {
		pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 second' WHERE monitor_id = '`+m.ID.String()+`' AND lease_token IS NULL`)
		return openIncidents() == 0
	})
	var status string
	var latency *int
	if err := admin.QueryRow(ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&status); err != nil || status != "up" {
		t.Fatalf("status %q %v", status, err)
	}
	if err := admin.QueryRow(ctx, `SELECT latency_ms FROM check_results WHERE monitor_id = $1 AND ok ORDER BY checked_at DESC LIMIT 1`, m.ID).Scan(&latency); err != nil || latency == nil {
		t.Fatalf("a successful check stores the connect time: %v %v", latency, err)
	}
}

// The hosted worker's claim hands a tcp monitor out as a tcp job with host and port.
func TestIntegrationClaimHandsOutTCPJobs(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("claimtcp"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	sched := scheduler.New()
	mon := monitors.NewService(env.Pool(t, env.APIURL, 4), sched, incidents.NewService(sched))
	host, port := "db.example.com", 5432
	m, err := mon.Create(ctx, monitors.Actor{TenantID: uuid.MustParse(tenantA), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}},
		monitors.CreateInput{Type: monitors.TypeTCP, Name: "DB", TCP: &monitors.TCPSettings{Host: &host, Port: &port}})
	if err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 minute' WHERE monitor_id = '`+m.ID.String()+`'`)
	jobs, err := sched.Claim(ctx, env.Pool(t, env.WorkerURL, 2), 10, scheduler.DefaultLocationCode)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim: %v %v", jobs, err)
	}
	if j := jobs[0]; !j.TCP() || j.Host != host || j.Port != port || j.URL != "" || j.Timeout != 10*time.Second || j.FailureThreshold != 2 {
		t.Fatalf("job = %+v", j)
	}
}
