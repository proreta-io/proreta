// Package gateway serves the Specula API: the endpoint executors inside customer networks call to
// say hello, claim due checks and report results. It runs with the worker's database role and
// reaches tenant data only for the organization its token belongs to.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/platform/logging"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

const (
	maxBody       = 1 << 20
	writeTimeout  = 20 * time.Second
	lockTimeout   = "3s"
	maxClockSkew  = time.Minute
	maxResultAge  = 10 * time.Minute
	maxLatencyMs  = int(time.Hour / time.Millisecond)
	retryAfterSec = "5"
)

// Deps are what the gateway needs.
type Deps struct {
	Pool      *db.Pool
	Scheduler *scheduler.Service
	Recorder  checkresult.Recorder
	// RequestKeys opens the sealed headers and bodies of monitor requests; nil on an installation
	// without them, where a job that has one fails.
	RequestKeys *crypto.Keyring
	Clock       clock.Clock
	Log         *slog.Logger
	Registerer  prometheus.Registerer
}

// Handler serves BasePath.
type Handler struct {
	d       Deps
	metrics *metrics
}

type ctxKey struct{}

// New returns the gateway handler, wrapped in request ids, access logs, panic recovery and a body
// limit.
func New(d Deps) http.Handler {
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	if d.Recorder.Clock == nil {
		d.Recorder.Clock = d.Clock
	}
	if d.Recorder.Jitter == nil {
		d.Recorder.Jitter = func() time.Duration { return 0 }
	}
	g := &Handler{d: d, metrics: newMetrics(d.Registerer)}
	mux := http.NewServeMux()
	mux.Handle("POST "+wire.BasePath+"/hello", g.auth(http.HandlerFunc(g.hello)))
	mux.Handle("POST "+wire.BasePath+"/claim", g.auth(http.HandlerFunc(g.claim)))
	mux.Handle("POST "+wire.BasePath+"/results", g.auth(http.HandlerFunc(g.results)))
	var h http.Handler = mux
	h = httpx.MaxBody(maxBody)(h)
	h = httpx.Recover(d.Log)(h)
	h = httpx.AccessLog(d.Log)(h)
	return httpx.RequestID(h)
}

// auth resolves the bearer token. Every failure looks the same.
func (g *Handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			g.unauthorized(w, r)
			return
		}
		id, err := specula.Authenticate(r.Context(), g.d.Pool, token)
		if errors.Is(err, specula.ErrUnauthorized) {
			g.unauthorized(w, r)
			return
		}
		if err != nil {
			g.d.Log.ErrorContext(r.Context(), "specula authenticate failed", "error", err)
			problem(w, r, http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func identity(r *http.Request) specula.Identity {
	return r.Context().Value(ctxKey{}).(specula.Identity)
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

func (g *Handler) unauthorized(w http.ResponseWriter, r *http.Request) {
	g.metrics.unauthorized.Inc()
	w.Header().Set("WWW-Authenticate", `Bearer realm="specula"`)
	problem(w, r, http.StatusUnauthorized, "unauthorized", "Unauthorized")
}

func (g *Handler) hello(w http.ResponseWriter, r *http.Request) {
	var in wire.HelloRequest
	if !decode(w, r, &in) {
		return
	}
	id := identity(r)
	if err := specula.Hello(r.Context(), g.d.Pool, id.SpeculaID, clean(in.Version, 64), clean(in.Hostname, 253)); err != nil {
		if errors.Is(err, specula.ErrUnauthorized) {
			g.unauthorized(w, r)
			return
		}
		g.fail(w, r, "hello", err)
		return
	}
	write(w, http.StatusOK, wire.HelloResponse{ServerTime: g.d.Clock.Now().UTC()})
}

func (g *Handler) claim(w http.ResponseWriter, r *http.Request) {
	var in wire.ClaimRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Max < 1 || in.Max > wire.MaxClaim {
		problem(w, r, http.StatusBadRequest, "bad-request", fmt.Sprintf("max must be between 1 and %d", wire.MaxClaim))
		return
	}
	id := identity(r)
	claimed, err := g.d.Scheduler.Claim(r.Context(), g.d.Pool, in.Max, id.LocationCode)
	if err != nil {
		g.fail(w, r, "claim", err)
		return
	}
	out := wire.ClaimResponse{Jobs: make([]wire.Job, 0, len(claimed))}
	for _, j := range claimed {
		// The claim already limits the location to the organization; this keeps a bug there
		// from ever handing out another organization's monitor.
		if j.TenantID != id.TenantID || j.LocationID != id.LocationID {
			g.d.Log.ErrorContext(r.Context(), "claim returned a job outside the Specula's scope", "specula_id", id.SpeculaID)
			continue
		}
		job := wire.Job{
			MonitorID: j.MonitorID, LeaseToken: j.Token, LeaseUntil: j.Until.UTC(), URL: j.URL, Method: j.Method,
			ExpectedStatus: j.ExpectedStatus, TimeoutSeconds: int(j.Timeout / time.Second),
		}
		if j.TCP() {
			job.Type, job.Host, job.Port = wire.TypeTCP, j.Host, j.Port
		}
		if j.Request != "" {
			sec, err := monitorrequest.Open(g.d.RequestKeys, j.TenantID, j.MonitorID, j.Request)
			if err != nil {
				// Misconfiguration (no or wrong request keys): fail the check visibly, as the worker does.
				g.d.Log.ErrorContext(r.Context(), "cannot open the sealed request of a monitor",
					"tenant_id", j.TenantID, "monitor_id", j.MonitorID, "error", err)
				g.recordUnsent(r.Context(), j)
				continue
			}
			for _, h := range sec.Headers {
				job.Headers = append(job.Headers, wire.Header{Name: h.Name, Value: h.Value})
			}
			job.Body = sec.Body
		}
		out.Jobs = append(out.Jobs, job)
	}
	g.metrics.jobs.Add(float64(len(out.Jobs)))
	write(w, http.StatusOK, out)
}

// recordUnsent stores a failed check for a job that cannot be sent.
func (g *Handler) recordUnsent(ctx context.Context, j scheduler.Job) {
	now := g.d.Clock.Now().UTC()
	err := db.WithTenantTx(ctx, g.d.Pool, j.TenantID, func(tx pgx.Tx) error {
		_, _, err := g.d.Recorder.Write(ctx, tx, j.Lease, j.Interval, checks.Result{CheckedAt: now, ErrorKind: checks.KindOther})
		return err
	})
	if err != nil {
		g.d.Log.ErrorContext(ctx, "recording an unsent check failed", "monitor_id", j.MonitorID, "error", err)
	}
}

var knownKinds = map[string]bool{
	checks.KindDNS: true, checks.KindConnect: true, checks.KindTLS: true, checks.KindTimeout: true,
	checks.KindBlockedAddress: true, checks.KindBadPort: true, checks.KindRedirect: true,
	checks.KindUnexpectedStatus: true, checks.KindOther: true,
}

func (g *Handler) validResult(x wire.Result, now time.Time) bool {
	switch {
	case x.MonitorID == uuid.Nil || x.LeaseToken == uuid.Nil:
		return false
	case x.CheckedAt.After(now.Add(maxClockSkew)) || x.CheckedAt.Before(now.Add(-maxResultAge)):
		return false
	case x.OK != (x.ErrorKind == ""):
		return false
	case x.ErrorKind != "" && !knownKinds[x.ErrorKind]:
		return false
	case x.StatusCode != 0 && (x.StatusCode < 100 || x.StatusCode > 599):
		return false
	case x.LatencyMs < 0 || x.LatencyMs > maxLatencyMs:
		return false
	}
	return true
}

func (g *Handler) results(w http.ResponseWriter, r *http.Request) {
	var in wire.ResultsRequest
	if !decode(w, r, &in) {
		return
	}
	if len(in.Results) == 0 || len(in.Results) > wire.MaxResults {
		problem(w, r, http.StatusBadRequest, "bad-request", fmt.Sprintf("send between 1 and %d results", wire.MaxResults))
		return
	}
	id := identity(r)
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	now := g.d.Clock.Now().UTC()
	outcomes := make([]wire.ResultOutcome, len(in.Results))
	err := db.WithTenantTx(ctx, g.d.Pool, id.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+lockTimeout+"'"); err != nil {
			return err
		}
		for i, x := range in.Results {
			outcomes[i] = wire.ResultOutcome{MonitorID: x.MonitorID, Outcome: wire.OutcomeRejected}
			if !g.validResult(x, now) {
				continue
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			ok, err := g.record(ctx, sp, id, x)
			switch {
			case err != nil:
				_ = sp.Rollback(ctx)
				return err
			case !ok:
				outcomes[i].Outcome = wire.OutcomeStale
				if err := sp.Rollback(ctx); err != nil {
					return err
				}
			default:
				outcomes[i].Outcome = wire.OutcomeRecorded
				if err := sp.Commit(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available: nothing was written, resend
			w.Header().Set("Retry-After", retryAfterSec)
			problem(w, r, http.StatusServiceUnavailable, "busy", "Busy, retry")
			return
		}
		g.fail(w, r, "results", err)
		return
	}
	for _, o := range outcomes {
		g.metrics.results.WithLabelValues(o.Outcome).Inc()
	}
	write(w, http.StatusOK, wire.ResultsResponse{Outcomes: outcomes})
}

// record stores one result of the Specula's own location. The lease is looked up by monitor, tenant
// and location from the token, never from the request, so a Specula can only complete leases it
// was handed.
func (g *Handler) record(ctx context.Context, tx pgx.Tx, id specula.Identity, x wire.Result) (bool, error) {
	var interval int32
	err := tx.QueryRow(ctx, `SELECT interval_seconds FROM monitors WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, id.TenantID, x.MonitorID).Scan(&interval)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lease := scheduler.Lease{TenantID: id.TenantID, MonitorID: x.MonitorID, LocationID: id.LocationID, Token: x.LeaseToken}
	res := checks.Result{
		OK: x.OK, StatusCode: x.StatusCode, LatencyMs: x.LatencyMs, ErrorKind: x.ErrorKind,
		TLSNotAfter: x.TLSNotAfter, CheckedAt: x.CheckedAt.UTC(),
	}
	ok, permanent, err := g.d.Recorder.Write(ctx, tx, lease, time.Duration(interval)*time.Second, res)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// The same result again (a retry after a lost answer): already stored, nothing to do.
		return false, nil
	}
	if permanent != "" {
		g.d.Log.ErrorContext(ctx, "applying a Specula result failed permanently; result kept, lease completed",
			"monitor_id", x.MonitorID, "error_kind", permanent)
	}
	return ok, err
}

func (g *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	g.d.Log.ErrorContext(r.Context(), "specula gateway error", "op", op, "error", err)
	problem(w, r, http.StatusInternalServerError, "internal", "Internal error")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		problem(w, r, http.StatusBadRequest, "bad-request", "Invalid JSON body")
		return false
	}
	return true
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, r *http.Request, status int, slug, title string) {
	httpx.WriteProblem(w, httpx.Problem{
		Type: "urn:proreta:problem:" + slug, Title: title, Status: status, Instance: r.URL.Path, RequestID: logging.RequestID(r.Context()),
	})
}

func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}
