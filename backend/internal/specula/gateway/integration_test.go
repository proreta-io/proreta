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

const (
	tenantA = "01920000-0000-7000-8000-00000000000a"
	tenantB = "01920000-0000-7000-8000-00000000000b"
)

func call(t *testing.T, srv *httptest.Server, token, path string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+wire.BasePath+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestIntegrationGateway(t *testing.T) {
	pg := pgtest.New(t, pgtest.DBName("speculagw"))
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
	actorA := specula.Actor{TenantID: uuid.MustParse(tenantA), UserID: user}
	s, token, err := svc.Create(ctx, actorA, specula.CreateInput{Name: "Prod", AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	newMonitor := func(tenant, name, url string, h *monitors.HTTPSettings) monitors.Monitor {
		t.Helper()
		h.URL = &url
		m, err := mon.Create(ctx, monitors.Actor{TenantID: uuid.MustParse(tenant), UserID: user, Access: authz.Actor{UserID: user, Role: authz.Owner}},
			monitors.CreateInput{Name: name, HTTP: h})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	sec := "Bearer s3cr3t-token"
	method, body := "POST", `{"ping":true}`
	hdrs := []monitors.HeaderInput{{Name: "Authorization", Value: &sec}}
	m := newMonitor(tenantA, "Internal API", "https://api.internal.example.com/health", &monitors.HTTPSettings{Method: &method, Headers: &hdrs, Body: &body})
	other := newMonitor(tenantB, "Other org", "https://other.example.com/", &monitors.HTTPSettings{})
	// Run the monitors at the Specula's location, due now.
	assign := func(monitor monitors.Monitor, tenant string) {
		pgtest.MustExec(t, admin,
			`INSERT INTO monitor_locations (tenant_id, monitor_id, location_id) VALUES ('`+tenant+`', '`+monitor.ID.String()+`', '`+s.LocationID.String()+`')`,
			`INSERT INTO monitor_location_state (tenant_id, monitor_id, location_id, next_run_at) VALUES ('`+tenant+`', '`+monitor.ID.String()+`', '`+s.LocationID.String()+`', now() - interval '1 minute')`)
	}
	assign(m, tenantA)

	build := func(k *crypto.Keyring) *httptest.Server {
		h := gateway.New(gateway.Deps{
			Pool: worker, Scheduler: sched, Recorder: checkresult.Recorder{Scheduler: sched, Incidents: inc, Clock: clock.System{}},
			RequestKeys: k, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registerer: prometheus.NewRegistry(),
		})
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv
	}
	srv := build(keys)

	t.Run("authentication", func(t *testing.T) {
		for _, tok := range []string{"", "nope", specula.TokenPrefix + "bad", strings.Replace(token, "spc_", "spd_", 1)} {
			if code, _ := call(t, srv, tok, "/hello", wire.HelloRequest{Version: "1"}); code != http.StatusUnauthorized {
				t.Errorf("token %q: %d", tok, code)
			}
		}
		if code, b := call(t, srv, token, "/hello", wire.HelloRequest{Version: "0.1.0", Hostname: "pod-1"}); code != http.StatusOK || !strings.Contains(string(b), "serverTime") {
			t.Fatalf("hello: %d %s", code, b)
		}
		if got, err := svc.Get(ctx, actorA.TenantID, s.ID); err != nil || got.Version != "0.1.0" || got.Hostname != "pod-1" {
			t.Fatalf("after hello: %+v %v", got, err)
		}
	})

	t.Run("hello with a long multi-byte hostname is stored cut on a rune", func(t *testing.T) {
		if code, b := call(t, srv, token, "/hello", wire.HelloRequest{Version: "1", Hostname: strings.Repeat("ж", 200)}); code != http.StatusOK {
			t.Fatalf("hello: %d %s", code, b)
		}
	})

	var job wire.Job
	t.Run("claim hands out the job with its decrypted request", func(t *testing.T) {
		if code, _ := call(t, srv, token, "/claim", wire.ClaimRequest{Max: 0}); code != http.StatusBadRequest {
			t.Fatalf("max 0: %d", code)
		}
		code, b := call(t, srv, token, "/claim", wire.ClaimRequest{Max: 10})
		var out wire.ClaimResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil || len(out.Jobs) != 1 {
			t.Fatalf("claim: %d %s", code, b)
		}
		job = out.Jobs[0]
		if job.MonitorID != m.ID || job.URL != "https://api.internal.example.com/health" || job.Method != "POST" || job.Body != body ||
			len(job.Headers) != 1 || job.Headers[0].Value != sec || job.ExpectedStatus != "2xx" || job.TimeoutSeconds != 10 {
			t.Fatalf("job: %+v", job)
		}
		// Leased: a second claim gets nothing.
		code, b = call(t, srv, token, "/claim", wire.ClaimRequest{Max: 10})
		if code != http.StatusOK || !strings.Contains(string(b), `"jobs":[]`) {
			t.Fatalf("second claim: %d %s", code, b)
		}
	})

	t.Run("results are recorded, applied and idempotent", func(t *testing.T) {
		now := time.Now().UTC()
		good := wire.Result{MonitorID: job.MonitorID, LeaseToken: job.LeaseToken, CheckedAt: now, OK: true, StatusCode: 200, LatencyMs: 42}
		outcome := func(res ...wire.Result) []string {
			t.Helper()
			code, b := call(t, srv, token, "/results", wire.ResultsRequest{Results: res})
			var out wire.ResultsResponse
			if code != http.StatusOK || json.Unmarshal(b, &out) != nil {
				t.Fatalf("results: %d %s", code, b)
			}
			var got []string
			for _, o := range out.Outcomes {
				got = append(got, o.Outcome)
			}
			return got
		}
		if got := outcome(good); len(got) != 1 || got[0] != wire.OutcomeRecorded {
			t.Fatalf("outcomes %v", got)
		}
		var n int
		var status string
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM check_results WHERE monitor_id = $1 AND ok AND status_code = 200`, m.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("results stored: %d %v", n, err)
		}
		if err := admin.QueryRow(ctx, `SELECT status FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&status); err != nil || status != "up" {
			t.Fatalf("monitor status %q %v", status, err)
		}
		// The lease is done: the same result again is stale and stores nothing.
		if got := outcome(good); got[0] != wire.OutcomeStale {
			t.Fatalf("repeat: %v", got)
		}
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM check_results WHERE monitor_id = $1`, m.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("results after repeat: %d %v", n, err)
		}
		// Malformed results are rejected, the rest of the batch still goes through.
		bad := []wire.Result{
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now, OK: true},                   // wrong lease: stale
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now, OK: true, ErrorKind: "tls"}, // ok with an error kind
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now, ErrorKind: "made_up"},       // unknown kind
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now.Add(time.Hour), OK: true},    // from the future
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now.Add(-time.Hour), OK: true},   // too old
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now, OK: true, StatusCode: 99},   // bad status
			{MonitorID: m.ID, LeaseToken: uuid.New(), CheckedAt: now, OK: true, LatencyMs: -1},    // bad latency
			{MonitorID: uuid.Nil, LeaseToken: uuid.New(), CheckedAt: now, OK: true},               // no monitor
		}
		got := outcome(bad...)
		want := []string{wire.OutcomeStale, wire.OutcomeRejected, wire.OutcomeRejected, wire.OutcomeRejected, wire.OutcomeRejected, wire.OutcomeRejected, wire.OutcomeRejected, wire.OutcomeRejected}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("outcomes %v, want %v", got, want)
		}
		if code, _ := call(t, srv, token, "/results", wire.ResultsRequest{}); code != http.StatusBadRequest {
			t.Fatalf("empty: %d", code)
		}
	})

	t.Run("a result the database refuses is rejected alone and keeps its lease", func(t *testing.T) {
		// latency 7 stands in for any permanent database error (here a check violation).
		pgtest.MustExec(t, admin, `CREATE FUNCTION refuse_seven() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.latency_ms = 7 THEN RAISE EXCEPTION 'refused' USING ERRCODE = '23514'; END IF; RETURN NEW; END $$`,
			`CREATE TRIGGER refuse_seven BEFORE INSERT ON check_results FOR EACH ROW EXECUTE FUNCTION refuse_seven()`,
			`UPDATE tenants SET plan_key = 'community' WHERE id = '`+tenantA+`'`)
		now := time.Now().UTC()
		var batch []wire.Result
		var ids []uuid.UUID
		for i, lat := range []int{1, 7, 2} {
			mm := newMonitor(tenantA, "Batch "+string(rune('a'+i)), "https://batch.example.com/", &monitors.HTTPSettings{})
			assign(mm, tenantA)
			var lease uuid.UUID
			if err := admin.QueryRow(ctx, `UPDATE monitor_location_state SET lease_token = gen_random_uuid(), lease_until = now() + interval '30 seconds'
				WHERE monitor_id = $1 AND location_id = $2 RETURNING lease_token`, mm.ID, s.LocationID).Scan(&lease); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, mm.ID)
			// Distinct times: the results table's key does not include the monitor.
			batch = append(batch, wire.Result{MonitorID: mm.ID, LeaseToken: lease, CheckedAt: now.Add(-time.Duration(i+1) * time.Second), OK: true, StatusCode: 200, LatencyMs: lat})
		}
		code, b := call(t, srv, token, "/results", wire.ResultsRequest{Results: batch})
		var out wire.ResultsResponse
		if code != http.StatusOK || json.Unmarshal(b, &out) != nil || len(out.Outcomes) != 3 {
			t.Fatalf("results: %d %s", code, b)
		}
		if got := []string{out.Outcomes[0].Outcome, out.Outcomes[1].Outcome, out.Outcomes[2].Outcome}; strings.Join(got, ",") != wire.OutcomeRecorded+","+wire.OutcomeRejected+","+wire.OutcomeRecorded {
			t.Fatalf("outcomes %v", got)
		}
		var held []bool
		for _, id := range ids {
			var h bool
			if err := admin.QueryRow(ctx, `SELECT lease_token IS NOT NULL FROM monitor_location_state WHERE monitor_id = $1 AND location_id = $2`, id, s.LocationID).Scan(&h); err != nil {
				t.Fatal(err)
			}
			held = append(held, h)
		}
		if held[0] || !held[1] || held[2] {
			t.Fatalf("leases held (first, failed, last) = %v, want false true false", held)
		}
		pgtest.MustExec(t, admin, `DROP TRIGGER refuse_seven ON check_results`)
	})

	t.Run("a Specula cannot report on another organization's monitor", func(t *testing.T) {
		assign(other, tenantB)
		// Even with a valid lease token of the other monitor (which the Specula cannot get), the
		// result is not applied: tenant and location come from the token.
		var lease uuid.UUID
		if err := admin.QueryRow(ctx, `UPDATE monitor_location_state SET lease_token = gen_random_uuid(), lease_until = now() + interval '30 seconds'
			WHERE monitor_id = $1 RETURNING lease_token`, other.ID).Scan(&lease); err != nil {
			t.Fatal(err)
		}
		code, b := call(t, srv, token, "/results", wire.ResultsRequest{Results: []wire.Result{
			{MonitorID: other.ID, LeaseToken: lease, CheckedAt: time.Now().UTC(), OK: true, StatusCode: 200},
		}})
		if code != http.StatusOK || !strings.Contains(string(b), wire.OutcomeStale) {
			t.Fatalf("cross-tenant result: %d %s", code, b)
		}
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM check_results WHERE monitor_id = $1`, other.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("stored a result for another organization: %d %v", n, err)
		}
		// And it is never claimed by this Specula.
		_, b = call(t, srv, token, "/claim", wire.ClaimRequest{Max: 10})
		if strings.Contains(string(b), other.ID.String()) {
			t.Fatalf("claimed another organization's monitor: %s", b)
		}
	})

	t.Run("without request keys a job with a sealed request fails visibly instead of being sent", func(t *testing.T) {
		pgtest.MustExec(t, admin, `UPDATE monitor_location_state SET next_run_at = now() - interval '1 minute', lease_token = NULL, lease_until = NULL WHERE monitor_id = '`+m.ID.String()+`'`)
		plain := build(nil)
		code, b := call(t, plain, token, "/claim", wire.ClaimRequest{Max: 10})
		if code != http.StatusOK || !strings.Contains(string(b), `"jobs":[]`) {
			t.Fatalf("claim without keys: %d %s", code, b)
		}
		var kind *string
		if err := admin.QueryRow(ctx, `SELECT error_kind FROM check_results WHERE monitor_id = $1 ORDER BY checked_at DESC LIMIT 1`, m.ID).Scan(&kind); err != nil || kind == nil || *kind != "other" {
			t.Fatalf("recorded failure: %v %v", kind, err)
		}
	})

	t.Run("a revoked token is refused at once", func(t *testing.T) {
		if err := svc.Revoke(ctx, actorA, s.ID); err != nil {
			t.Fatal(err)
		}
		if code, _ := call(t, srv, token, "/claim", wire.ClaimRequest{Max: 1}); code != http.StatusUnauthorized {
			t.Fatalf("after revoke: %d", code)
		}
	})
}
