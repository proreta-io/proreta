// Package tlsask is the TLS permission endpoint of custom status page domains: a front proxy that
// issues certificates on demand (Caddy's on-demand TLS) asks GET /internal/tls/allow?domain=<host>
// before it requests one, and gets 200 for a host that may have a certificate and 404 for every
// other host. The body is always empty. It runs on its own internal listener, never behind the
// public Gateway, and every request carries a shared token.
package tlsask

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

// Path is the one route of the endpoint.
const Path = "/internal/tls/allow"

// Defaults of Options.
const (
	DefaultCacheTTL   = 10 * time.Second
	DefaultRate       = 50.0 // lookups per second that reach the database, per replica
	DefaultBurst      = 100
	maxCacheEntries   = 10000
	maxTokenQueryLen  = 256
	allowResultLabel  = "allowed"
	denyResultLabel   = "denied"
	unauthorizedLabel = "unauthorized"
	limitedLabel      = "rate_limited"
	errorLabel        = "error"
)

// Allower says whether a certificate may be issued for a host (already normalised).
type Allower interface {
	Allowed(ctx context.Context, host string) (bool, error)
}

// DBAllower asks status_page_domain_allowed, the only thing the renderer's role may do with the
// domain table.
type DBAllower struct{ Pool *db.Pool }

// Allowed implements Allower.
func (a DBAllower) Allowed(ctx context.Context, host string) (bool, error) {
	var ok bool
	err := db.WithTx(ctx, a.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status_page_domain_allowed($1)`, host).Scan(&ok)
	})
	return ok, err
}

// Options configure a Handler. Zero values take the defaults.
type Options struct {
	// Token is the shared secret; New refuses an empty one.
	Token string
	// Rules are the installation's hostname rules applied before the lookup.
	Rules domains.Rules
	// CacheTTL keeps answers, positive and negative, in process; Rate and Burst bound the lookups
	// that miss the cache (a flood of random names must not reach the database).
	CacheTTL time.Duration
	Rate     float64
	Burst    int
	Now      func() time.Time
	Logger   *slog.Logger
	// Registerer receives proreta_statuspage_tls_allow_total{result}.
	Registerer prometheus.Registerer
}

// Handler serves the endpoint.
type Handler struct {
	allower  Allower
	tokenSum [sha256.Size]byte
	rules    domains.Rules
	ttl      time.Duration
	now      func() time.Time
	log      *slog.Logger
	results  *prometheus.CounterVec

	mu     sync.Mutex
	cache  map[string]cacheEntry
	tokens float64
	last   time.Time
	rate   float64
	burst  float64
}

type cacheEntry struct {
	allowed bool
	until   time.Time
}

// ErrNoToken means the endpoint has no token to check requests against.
var ErrNoToken = errors.New("tlsask: a token is required")

// New returns the handler. It refuses to be built without a token, so the endpoint can never run
// open.
func New(a Allower, o Options) (*Handler, error) {
	if o.Token == "" {
		return nil, ErrNoToken
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.Rate <= 0 {
		o.Rate = DefaultRate
	}
	if o.Burst <= 0 {
		o.Burst = DefaultBurst
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	h := &Handler{allower: a, tokenSum: sha256.Sum256([]byte(o.Token)), rules: o.Rules, ttl: o.CacheTTL, now: o.Now, log: o.Logger,
		cache: map[string]cacheEntry{}, rate: o.Rate, burst: float64(o.Burst), tokens: float64(o.Burst), last: o.Now(),
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proreta_statuspage_tls_allow_total",
			Help: "Answers of the TLS permission endpoint by result (allowed, denied, unauthorized, rate_limited, error).",
		}, []string{"result"})}
	if o.Registerer != nil {
		if err := o.Registerer.Register(h.results); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// ServeHTTP implements http.Handler. Status codes: 200 allowed, 404 anything else about the host,
// 401 wrong or missing token, 429 over the lookup rate, 503 the lookup failed; the body is empty.
// The query string (it may carry the token) is never logged.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != Path {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	if !h.authorized(r, q.Get("token")) {
		h.results.WithLabelValues(unauthorizedLabel).Inc()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	host, err := h.rules.Validate(q.Get("domain"))
	if err != nil {
		h.results.WithLabelValues(denyResultLabel).Inc()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	allowed, status := h.decide(r.Context(), host)
	switch {
	case status != 0:
		w.WriteHeader(status)
	case allowed:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// authorized compares the presented token (Authorization: Bearer, or the token query parameter
// for proxies that cannot send a header) with the configured one in constant time.
func (h *Handler) authorized(r *http.Request, queryToken string) bool {
	got := queryToken
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = v
	}
	if len(got) > maxTokenQueryLen {
		return false
	}
	sum := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(sum[:], h.tokenSum[:]) == 1
}

// decide answers from the cache, else from the database within the rate limit. A non-zero status
// is the HTTP status to answer instead of 200/404.
func (h *Handler) decide(ctx context.Context, host string) (allowed bool, status int) {
	now := h.now()
	h.mu.Lock()
	if e, ok := h.cache[host]; ok && now.Before(e.until) {
		h.mu.Unlock()
		h.count(e.allowed)
		return e.allowed, 0
	}
	h.tokens = min(h.burst, h.tokens+now.Sub(h.last).Seconds()*h.rate)
	h.last = now
	if h.tokens < 1 {
		h.mu.Unlock()
		h.results.WithLabelValues(limitedLabel).Inc()
		return false, http.StatusTooManyRequests
	}
	h.tokens--
	h.mu.Unlock()

	ok, err := h.allower.Allowed(ctx, host)
	if err != nil {
		h.results.WithLabelValues(errorLabel).Inc()
		h.log.ErrorContext(ctx, "tls permission lookup failed", "error", err)
		return false, http.StatusServiceUnavailable
	}
	h.mu.Lock()
	if len(h.cache) >= maxCacheEntries {
		for k, e := range h.cache {
			if !now.Before(e.until) {
				delete(h.cache, k)
			}
		}
	}
	if len(h.cache) < maxCacheEntries {
		h.cache[host] = cacheEntry{allowed: ok, until: now.Add(h.ttl)}
	}
	h.mu.Unlock()
	h.count(ok)
	return ok, 0
}

func (h *Handler) count(allowed bool) {
	if allowed {
		h.results.WithLabelValues(allowResultLabel).Inc()
		return
	}
	h.results.WithLabelValues(denyResultLabel).Inc()
}
