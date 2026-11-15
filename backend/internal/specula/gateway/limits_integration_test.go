package gateway_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/gateway"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// The per-token budgets answer 429 with Retry-After before the body is read, count it per endpoint,
// give every Specula its own bucket, and refill with time.
func TestIntegrationTokenLimits(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculalimits"))
	ctx := context.Background()
	admin := pg.AdminConn(t)
	user := uuid.New()
	pgtest.MustExec(t, admin,
		`INSERT INTO tenants (id, slug, name) VALUES ('`+tenantA+`', 'a', 'A'), ('`+tenantB+`', 'b', 'B')`,
		`INSERT INTO users (id, email) VALUES ('`+user.String()+`', 'owner@example.com')`)
	svc := specula.NewService(pg.Pool(t, pg.APIURL, 4))
	actor := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	_, tokenA, err := svc.Create(ctx, actor, specula.CreateInput{Name: "A", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	_, tokenB, err := svc.Create(ctx, specula.Actor{TenantID: uuid.MustParse(tenantB), UserID: user}, specula.CreateInput{Name: "B", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}

	clk := clock.NewFake(time.Now())
	reg := prometheus.NewRegistry()
	sched := scheduler.New()
	srv := httptest.NewServer(gateway.New(gateway.Deps{
		Pool: pg.Pool(t, pg.WorkerURL, 4), Scheduler: sched, Clock: clk, Registerer: reg,
		Recorder: checkresult.Recorder{Scheduler: sched, Incidents: incidents.NewService(sched), Clock: clk},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Two calls, then one more per 4 seconds (hello), 20 seconds (schedule) and 2 seconds (reports).
		Limits: gateway.Limits{
			Hello: gateway.Budget{Rate: 0.25, Burst: 2}, Schedule: gateway.Budget{Rate: 0.05, Burst: 2}, Reports: gateway.Budget{Rate: 0.5, Burst: 2},
		},
	}))
	t.Cleanup(srv.Close)

	// An empty body fails validation (400) but still uses a token of the budget.
	do := map[string]func(token string) answer{
		"hello":    func(tok string) answer { return doReq(t, srv, http.MethodPost, wire.PathHello, tok) },
		"reports":  func(tok string) answer { return doReq(t, srv, http.MethodPost, wire.PathReports, tok) },
		"schedule": func(tok string) answer { return doReq(t, srv, http.MethodGet, wire.PathSchedule, tok) },
	}
	for endpoint, wait := range map[string]time.Duration{"hello": 4 * time.Second, "reports": 2 * time.Second, "schedule": 20 * time.Second} {
		call := do[endpoint]
		for i := 0; i < 2; i++ {
			if r := call(tokenA); r.StatusCode == http.StatusTooManyRequests {
				t.Fatalf("%s: call %d of the burst refused", endpoint, i)
			}
		}
		r := call(tokenA)
		if ra, _ := strconv.Atoi(r.Header.Get("Retry-After")); r.StatusCode != http.StatusTooManyRequests || ra < int(wait.Seconds()) || ra > int(wait.Seconds())+1 {
			t.Fatalf("%s over budget: %d Retry-After=%q, want 429 and about %v", endpoint, r.StatusCode, r.Header.Get("Retry-After"), wait)
		}
		if r := call(tokenB); r.StatusCode == http.StatusTooManyRequests {
			t.Errorf("%s: another Specula's token shares the bucket", endpoint)
		}
		clk.Advance(wait)
		if r := call(tokenA); r.StatusCode == http.StatusTooManyRequests {
			t.Errorf("%s: still refused after Retry-After", endpoint)
		}
		if got := metricValue(t, reg, "proreta_specula_token_limited_total", "endpoint", endpoint); got != 1 {
			t.Errorf("token_limited_total{endpoint=%q} = %v, want 1", endpoint, got)
		}
	}
	// claim and results (pull mode) have no per-token budget.
	for i := 0; i < 10; i++ {
		if code, _ := call(t, srv, tokenA, "/claim", wire.ClaimRequest{Max: 1}); code == http.StatusTooManyRequests {
			t.Fatal("claim must not be limited per token")
		}
	}
}

func doReq(t *testing.T, srv *httptest.Server, method, path, token string) answer {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return answer{resp.StatusCode, resp.Header}
}

// metricValue is the counter or gauge value, or the sample count of a histogram, of the series of
// name whose labels include every key/value pair; 0 when there is none.
func metricValue(t *testing.T, reg *prometheus.Registry, name string, kv ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	series:
		for _, m := range mf.GetMetric() {
			for i := 0; i < len(kv); i += 2 {
				if !hasLabel(m.GetLabel(), kv[i], kv[i+1]) {
					continue series
				}
			}
			switch {
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount())
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			default:
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}

func hasLabel(ls []*dto.LabelPair, name, value string) bool {
	for _, l := range ls {
		if l.GetName() == name && l.GetValue() == value {
			return true
		}
	}
	return false
}
