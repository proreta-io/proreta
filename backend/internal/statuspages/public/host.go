package public

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

// HostKind says which kind of address a request came to.
type HostKind int

const (
	// HostInstallation is a name of the installation itself: the path routes under /status/<slug>.
	HostInstallation HostKind = iota
	// HostCustom is any other well-formed name: a candidate customer domain.
	HostCustom
	// HostInvalid is not a plain host name (IP address, port garbage, reserved or non-ASCII names).
	HostInvalid
)

// HostClassifier normalises the Host header and tells the installation's own names from the rest.
type HostClassifier struct {
	installation map[string]struct{}
	rules        domains.Rules
}

// NewHostClassifier returns a classifier for the installation hosts (PRORETA_PUBLIC_URL's host and
// PRORETA_STATUSPAGE_INSTALLATION_HOSTS).
func NewHostClassifier(installationHosts []string) HostClassifier {
	c := HostClassifier{installation: map[string]struct{}{}, rules: domains.NewRules(installationHosts)}
	for _, h := range installationHosts {
		if h = domains.Normalize(h); h != "" {
			c.installation[h] = struct{}{}
		}
	}
	return c
}

// Classify returns the kind of raw (a Host header value) and, for a custom host, its normalised form.
func (c HostClassifier) Classify(raw string) (HostKind, string) {
	host := raw
	if strings.Contains(raw, ":") {
		h, _, err := net.SplitHostPort(raw)
		if err != nil {
			return HostInvalid, ""
		}
		host = h
	}
	host = domains.Normalize(host)
	if _, ok := c.installation[host]; ok {
		return HostInstallation, host
	}
	// The same rules as on input, so a host that could never have been added is not looked up.
	h, err := c.rules.Validate(host)
	if err != nil {
		return HostInvalid, ""
	}
	return HostCustom, h
}

// HostSource maps a customer's host to the slug of its page. The database implementation calls
// status_page_domain_lookup; tests use a fake.
type HostSource interface {
	LookupHost(ctx context.Context, host string) (slug string, found bool, err error)
}

// LookupHost implements HostSource: the slug of a serving domain of a published page, nothing else.
func (s DBSource) LookupHost(ctx context.Context, host string) (string, bool, error) {
	var slug string
	err := db.WithTx(ctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT slug FROM status_page_domain_lookup($1)`, host).Scan(&slug)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return slug, true, nil
}

// HostCacheTTL is how long a host -> slug answer is reused, found or not.
const HostCacheTTL = 15 * time.Second

// maxHostCacheEntries bounds memory under a flood of random hosts (negative answers are cached too).
const maxHostCacheEntries = 10_000

// HostCache is an in-process cache per host in front of a HostSource, per replica.
type HostCache struct {
	src   HostSource
	clock clock.Clock
	group singleflight.Group
	mu    sync.Mutex
	items map[string]hostItem
}

type hostItem struct {
	slug    string
	found   bool
	expires time.Time
}

// NewHostCache wraps src.
func NewHostCache(src HostSource, c clock.Clock) *HostCache {
	if c == nil {
		c = clock.System{}
	}
	return &HostCache{src: src, clock: c, items: map[string]hostItem{}}
}

// Cached returns a fresh cached answer, if there is one.
func (c *HostCache) Cached(host string) (slug string, found, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[host]
	if !ok || !c.clock.Now().Before(it.expires) {
		return "", false, false
	}
	return it.slug, it.found, true
}

// MaxStaleAge is how long past its expiry an answer may still be served when a lookup is not
// allowed: long enough to ride out a short flood, short enough that a removed or taken-over
// domain cannot be kept alive by one.
const MaxStaleAge = 2 * time.Minute

// Stale returns the last answer for host, up to MaxStaleAge past its expiry, for when a lookup is not allowed.
func (c *HostCache) Stale(host string) (slug string, found, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[host]
	if !ok || !c.clock.Now().Before(it.expires.Add(MaxStaleAge)) {
		return "", false, false
	}
	return it.slug, it.found, true
}

// Fetch asks the source (concurrent calls for one host make one lookup) and caches the answer.
// An error is not cached.
func (c *HostCache) Fetch(ctx context.Context, host string) (string, bool, error) {
	v, err, _ := c.group.Do(host, func() (any, error) {
		slug, found, err := c.src.LookupHost(context.WithoutCancel(ctx), host)
		if err != nil {
			return nil, err
		}
		now := c.clock.Now()
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.items) >= maxHostCacheEntries {
			for k, v := range c.items {
				if !now.Before(v.expires) {
					delete(c.items, k)
				}
			}
			// Still full: drop unknown hosts (what a flood fills the cache with), then arbitrary
			// entries, down to half (bounded memory over accuracy).
			for _, keepFound := range []bool{false, true} {
				for k, v := range c.items {
					if len(c.items) < maxHostCacheEntries/2 {
						break
					}
					if v.found == keepFound {
						delete(c.items, k)
					}
				}
			}
		}
		it := hostItem{slug: slug, found: found, expires: now.Add(HostCacheTTL)}
		c.items[host] = it
		return it, nil
	})
	if err != nil {
		return "", false, err
	}
	it := v.(hostItem)
	return it.slug, it.found, nil
}

// hstsWriter adds Strict-Transport-Security to successful answers only, so every 404 stays
// identical whatever host and path it was for. No includeSubDomains and no preload: the customer
// owns the rest of their name.
type hstsWriter struct {
	http.ResponseWriter
	wrote bool
}

const hstsValue = "max-age=31536000"

func (w *hstsWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		if code < 400 {
			w.Header().Set("Strict-Transport-Security", hstsValue)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *hstsWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *hstsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
