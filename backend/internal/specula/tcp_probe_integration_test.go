package specula_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
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

// A tcp monitor with send and expect at a Specula: it is not handed to an executor that did not
// announce tcp/probe (it would report a plain connect as a pass), and one that did sends the
// bytes and records the reply's verdict, a mismatch included.
func TestIntegrationSpeculaTCPProbe(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("speculatcpprobe"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	keys, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{4}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}
	apiPool, worker := env.Pool(t, env.APIURL, 4), env.Pool(t, env.WorkerURL, 4)
	sched := scheduler.New()
	inc := incidents.NewService(sched)
	mon := monitors.NewService(apiPool, sched, inc).WithRequestKeys(keys)
	svc := specula.NewService(apiPool)
	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	macts := monitors.Actor{TenantID: actor.TenantID, UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}}
	s, token, err := svc.Create(ctx, actor, specula.CreateInput{Name: "Prod cluster", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}

	ask := []byte{0, 0, 0, 4, 'p', 'i', 'n', 'g'}
	serve := func(reply string) int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					got := make([]byte, len(ask))
					if _, err := io.ReadFull(c, got); err == nil && bytes.Equal(got, ask) {
						_, _ = c.Write([]byte(reply))
					}
				}()
			}
		}()
		return l.Addr().(*net.TCPAddr).Port
	}
	host, threshold := "127.0.0.1", 1
	create := func(name string, port int) uuid.UUID {
		m, err := mon.Create(ctx, macts, monitors.CreateInput{
			Type: monitors.TypeTCP, Name: name, SpeculaID: &s.ID,
			TCP: &monitors.TCPSettings{Host: &host, Port: &port, FailureThreshold: &threshold,
				Send:   &monitors.TCPSend{Encoding: "hex", Value: "00000004 70696e67"},
				Expect: &monitors.TCPExpect{Mode: "contains", Value: `"status":"ok"`}},
		})
		if err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 minute' WHERE monitor_id = '`+m.ID.String()+`'`)
		return m.ID
	}
	good, bad := create("ready", serve(`{"status":"ok"}`)), create("sick", serve(`{"status":"down"}`))

	// An executor that runs tcp checks but not probes gets nothing.
	pgtest.MustExec(t, admin, `UPDATE specula_agents SET capabilities = '{tcp}' WHERE id = '`+s.ID.String()+`'`)
	var code string
	if err := admin.QueryRow(ctx, `SELECT l.code FROM probe_locations l JOIN specula_agents a ON a.location_id = l.id WHERE a.id = $1`, s.ID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if jobs, err := sched.Claim(ctx, worker, 10, code); err != nil || len(jobs) != 0 {
		t.Fatalf("an executor without tcp/probe was handed %d jobs (%v)", len(jobs), err)
	}

	gw := httptest.NewServer(withoutSchedule(gateway.New(gateway.Deps{
		Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
		RequestKeys: keys, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
	})))
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

	count := func(q string, id uuid.UUID) (n int) {
		_ = admin.QueryRow(ctx, q, id).Scan(&n)
		return n
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND ok AND latency_ms IS NOT NULL`, good) > 0 &&
			count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND NOT ok AND error_kind = 'unexpected_response'`, bad) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no verdicts: good ok=%d, bad kinds=%d", count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND ok`, good),
		count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND NOT ok`, bad))
}
