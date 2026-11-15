package gateway

import (
	"sync"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula"
)

// Limits of the token check. All of it is in memory and per gateway replica: with N replicas the
// effective per-IP budget is up to N times authFailLimit (the load balancer spreads a client's
// requests), which still bounds the database cost of a flood and needs no shared state.
const (
	// authCacheTTL is how long a verified token is trusted without asking the database again: a
	// revoked Specula, a suspended or deleted organization or a disabled location is refused
	// within this window (plus nothing else: the entry is dropped when it expires).
	authCacheTTL = 5 * time.Second
	// authCacheMax bounds the cache. Only verified tokens enter it, so a flood of random tokens
	// cannot fill it.
	authCacheMax = 10_000
	// authFailLimit is the number of database lookups per client IP and authFailWindow that did not
	// end in a valid token. Lookups that succeed are refunded, so executors sharing an address
	// (one customer network) are not limited by each other, only by hostile neighbours.
	authFailLimit  = 60
	authFailWindow = time.Minute
	authFailKeys   = 100_000
	// authLookups caps concurrent authentication transactions of one replica, so a flood from many
	// addresses cannot take every database connection from claims and results.
	authLookups = 8
)

// ttlCache remembers values for a few seconds, in at most max entries: verified token hashes
// (authCache) and the schedule version of a Specula (versionCache).
type ttlCache[V any] struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	m   map[string]cached[V]
}

type cached[V any] struct {
	v     V
	until time.Time
}

type authCache = ttlCache[specula.Identity]

func newAuthCache(ttl time.Duration, max int) *authCache {
	return newTTLCache[specula.Identity](ttl, max)
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, max: max, m: map[string]cached[V]{}}
}

func (c *ttlCache[V]) get(key string, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !now.Before(e.until) {
		delete(c.m, key)
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *ttlCache[V]) put(key string, v V, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok && len(c.m) >= c.max {
		for k, e := range c.m {
			if !now.Before(e.until) {
				delete(c.m, k)
			}
		}
		// Still full of live entries: drop an arbitrary one. shortcut: not LRU; an evicted
		// executor only pays one extra lookup.
		for k := range c.m {
			if len(c.m) < c.max {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[key] = cached[V]{v: v, until: now.Add(c.ttl)}
}
