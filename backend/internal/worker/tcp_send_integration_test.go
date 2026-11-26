package worker_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/internal/worker"
)

// A tcp monitor with a sealed probe sends its bytes and passes only on a matching reply; the
// stored result carries the time to the reply and the kind of a mismatch.
func TestIntegrationWorkerRunsTCPSendAndExpect(t *testing.T) {
	e := newEnv(t, "workertcpsend")
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
	keys, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{4}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}
	monitor := func(port int, expect string) uuid.UUID {
		id := e.monitor(tenantA, "https://unused.example.com", 2)
		sealed, err := monitorrequest.Seal(keys, uuid.MustParse(tenantA), id, monitorrequest.Secrets{Probe: &monitorrequest.TCPProbe{
			Send: ask, ExpectMode: "contains", ExpectValue: expect, ReadTimeoutSeconds: 2, MaxResponseBytes: 100}})
		if err != nil {
			t.Fatal(err)
		}
		pgtest.MustExec(t, e.admin, fmt.Sprintf(`UPDATE monitors SET type = 'tcp', config = '{"host":"127.0.0.1","port":%d,"request":"%s"}' WHERE id = '%s'`, port, sealed, id))
		return id
	}
	ok, mismatch := monitor(serve(`{"status":"ok"}`), `"status":"ok"`), monitor(serve(`{"status":"down"}`), `"status":"ok"`)

	reg := prometheus.NewRegistry()
	sched := scheduler.New()
	plain := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	w := worker.New(worker.Config{ClaimBatch: 100, Concurrency: 50, TenantConcurrency: 5, HostConcurrency: 2,
		PollInterval: 50 * time.Millisecond, FlushInterval: 20 * time.Millisecond}, worker.Deps{
		Pool: e.Pool(t, e.WorkerURL, 4), Scheduler: sched, Incidents: incidents.NewService(sched),
		Prober: checks.Mux{HTTP: &fakeProber{}, TCP: checks.NewTCPProber(plain)}, RequestKeys: keys,
		Metrics: worker.NewMetrics(reg), Log: slog.New(slog.NewTextHandler(&e.logs, nil)), Jitter: func() time.Duration { return 0 },
	})
	w.RunOnce(e.ctx)
	if n := e.count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND ok AND latency_ms IS NOT NULL AND error_kind IS NULL`, ok); n != 1 {
		t.Errorf("matching reply: %d matching results", n)
	}
	if n := e.count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND NOT ok AND error_kind = 'unexpected_response'`, mismatch); n != 1 {
		t.Errorf("mismatching reply: %d matching results", n)
	}
}
