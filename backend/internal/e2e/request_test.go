//go:build proretatest

package e2e_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/api"
	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/queries"
	"github.com/proreta-io/proreta/backend/internal/safehttp"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/internal/worker"
)

// seenRequest is what the monitored site received.
type seenRequest struct {
	method, path, body, contentType, authz, key string
}

type recorder struct {
	mu   sync.Mutex
	seen []seenRequest
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.seen = append(r.seen, seenRequest{req.Method, req.URL.Path, string(b), req.Header.Get("Content-Type"), req.Header.Get("Authorization"), req.Header.Get("X-Api-Key")})
	r.mu.Unlock()
	switch req.URL.Path {
	case "/same-host":
		http.Redirect(w, req, "/landed", http.StatusTemporaryRedirect)
	case "/other-host":
		http.Redirect(w, req, "https://"+otherHost+"/landed", http.StatusTemporaryRedirect)
	}
}

func (r *recorder) take() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.seen
	r.seen = nil
	return out
}

const otherHost = "other.e2e-target.test"

// TestIntegrationMonitorRequestEndToEnd creates monitors with a method, headers and a body through the
// api, runs the worker against a loopback site through the real guard and checks what arrived,
// that a redirect to another host drops the check, and that a worker without the request keys
// fails the check instead of sending a request without its headers.
func TestIntegrationMonitorRequestEndToEnd(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("e2ereq"))
	ctx := context.Background()
	admin := env.AdminConn(t)
	log := logging.New(io.Discard, -4)
	cert := newTestCert(t)
	site := &recorder{}
	_, port := startTLS(t, cert, site)
	loopback := netip.MustParseAddr("127.0.0.1")
	resolver := hostsResolver{targetHost: loopback, otherHost: loopback}
	probeClient := safehttp.NewLoopbackTestClient(safehttp.Options{Timeout: checks.DefaultTimeout, MaxRedirects: 3, Resolver: resolver}, cert.roots, port)
	keys, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{5}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}

	apiPool := env.Pool(t, env.APIURL, 4)
	authSvc, err := auth.NewService(apiPool, auth.Options{SessionTTL: time.Hour, Logger: log, Registerer: prometheus.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := crypto.HashPassword(adminPassword, crypto.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	if err := authSvc.BootstrapAdmin(ctx, adminLogin, hash); err != nil {
		t.Fatal(err)
	}
	pgtest.MustExec(t, admin, `UPDATE users SET password_must_change = false WHERE email = '`+adminLogin+`'`)
	sched := scheduler.New()
	handler, err := api.NewHandler(api.Config{Version: "e2e", Commit: "e2e", CookieSecure: true, PublicURL: publicURL}, api.Deps{
		Auth: authSvc, Monitors: monitors.NewService(apiPool, sched, incidents.NewService(sched)).WithRequestKeys(keys),
		Queries: queries.New(apiPool, nil), Plans: plans.NewService(apiPool), Projects: projects.NewService(apiPool), Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := &client{t: t, srv: srv}
	lr := c.do(http.MethodPost, "/api/v1/auth/login", `{"login":"`+adminLogin+`","password":"`+adminPassword+`"}`, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login: %d %s", lr.code, lr.raw)
	}
	csrf, _ := lr.body["csrfToken"].(string)
	var session string
	for _, ck := range (&http.Response{Header: lr.header}).Cookies() {
		if ck.Name == api.SessionCookie {
			session = ck.Value
		}
	}
	unsafe := map[string]string{"Cookie": api.SessionCookie + "=" + session, "X-CSRF-Token": csrf}

	workerPool := env.Pool(t, env.WorkerURL, 4)
	newWorker := func(k *crypto.Keyring) *worker.Worker {
		return worker.New(worker.Config{ClaimBatch: 10, Concurrency: 4, TenantConcurrency: 2, HostConcurrency: 2}, worker.Deps{
			Pool: workerPool, Scheduler: sched, Incidents: incidents.NewService(sched),
			Prober: checks.NewHTTPSProber(probeClient), RequestKeys: k, Metrics: worker.NewMetrics(prometheus.NewRegistry()), Log: log,
			Jitter: func() time.Duration { return 0 },
		})
	}
	withKeys, withoutKeys := newWorker(keys), newWorker(nil)
	if err := withKeys.EnsurePartitions(ctx); err != nil {
		t.Fatal(err)
	}
	create := func(path, httpBody string) string {
		t.Helper()
		b := c.mustDo(http.StatusCreated, http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"m `+path+`","http":{"url":"https://`+targetHost+path+`",`+httpBody+`}}`, unsafe)
		return b["id"].(string)
	}
	run := func(w *worker.Worker, id string) {
		t.Helper()
		if _, err := admin.Exec(ctx, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 second' WHERE monitor_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		w.RunOnce(ctx)
	}
	lastKind := func(id string) (ok bool, kind *string) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT ok, error_kind FROM check_results WHERE monitor_id = $1 ORDER BY checked_at DESC LIMIT 1`, id).Scan(&ok, &kind); err != nil {
			t.Fatalf("no check result: %v", err)
		}
		return ok, kind
	}

	t.Run("method headers and body arrive", func(t *testing.T) {
		id := create("/ping", `"method":"POST","headers":[{"name":"Authorization","value":"Bearer e2e-token"},{"name":"X-Api-Key","value":"k1"}],"body":"{\"check\":true}"`)
		run(withKeys, id)
		got := site.take()
		want := seenRequest{"POST", "/ping", `{"check":true}`, "application/json", "Bearer e2e-token", "k1"}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("site saw %+v, want %+v", got, want)
		}
		if ok, kind := lastKind(id); !ok {
			t.Fatalf("check failed: %v", kind)
		}
	})
	t.Run("default is a plain GET", func(t *testing.T) {
		id := create("/plain", `"failureThreshold":1`)
		run(withKeys, id)
		got := site.take()
		if len(got) != 1 || got[0] != (seenRequest{method: "GET", path: "/plain"}) {
			t.Fatalf("site saw %+v", got)
		}
	})
	t.Run("a same host redirect keeps body and headers", func(t *testing.T) {
		id := create("/same-host", `"method":"PUT","headers":[{"name":"X-Api-Key","value":"k2"}],"body":"payload"`)
		run(withKeys, id)
		got := site.take()
		if len(got) != 2 || got[1].path != "/landed" || got[1].method != "PUT" || got[1].body != "payload" || got[1].key != "k2" {
			t.Fatalf("site saw %+v", got)
		}
	})
	t.Run("a redirect to another host ends the check before the second request", func(t *testing.T) {
		id := create("/other-host", `"method":"POST","headers":[{"name":"X-Api-Key","value":"leak-me-not"}],"body":"top-secret"`)
		run(withKeys, id)
		got := site.take()
		if len(got) != 1 {
			t.Fatalf("a request followed the cross host redirect: %+v", got)
		}
		if ok, kind := lastKind(id); ok || kind == nil || *kind != checks.KindRedirect {
			t.Fatalf("result ok=%v kind=%v", ok, kind)
		}
	})
	t.Run("a worker without the request keys fails the check and sends nothing", func(t *testing.T) {
		id := create("/nokeys", `"method":"POST","headers":[{"name":"X-Api-Key","value":"k3"}]`)
		run(withoutKeys, id)
		if got := site.take(); len(got) != 0 {
			t.Fatalf("a request was sent: %+v", got)
		}
		if ok, kind := lastKind(id); ok || kind == nil || *kind != checks.KindOther {
			t.Fatalf("result ok=%v kind=%v", ok, kind)
		}
	})
}
