package gateway

import (
	"container/list"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Per-token budgets of the Specula API. They are in memory and per gateway replica (like the
// per-IP budget), so with N replicas a token may use up to N times these rates.
const (
	// reportsRate and reportsBurst fit the executor's replay: 5 batches of 100 per second is 500
	// results per second, and the burst absorbs jitter and a few parallel calls.
	reportsRate  = 5.0
	reportsBurst = 20
	// Pull mode (claim and results, executors that predate the schedule) gets the same budget
	// per endpoint: a crash-looping old executor must not flood the shared database.
	// scheduleRate and scheduleBurst: the executor refreshes about every 5 minutes and fetches
	// again when hello names a new version, so a call every 10 seconds is far above any normal use.
	scheduleRate  = 0.1
	scheduleBurst = 3
	// helloRate and helloBurst: the executor says hello once a minute.
	helloRate  = 2.0 / 60
	helloBurst = 2
	// bucketKeys bounds the state per endpoint. Only verified tokens get a bucket, so the number of
	// Specula of a replica's traffic is the bound; this is a safety net, not a working limit.
	bucketKeys = 10_000
)

// Budget is a token bucket: Rate tokens per second up to Burst.
type Budget struct {
	Rate  float64
	Burst int
}

// Limits are the per-token budgets; a zero Budget uses the constants above. Tests that call
// faster than any executor raise them, the server never does.
type Limits struct{ Reports, Schedule, Hello, Claim, Results Budget }

func (b Budget) or(rate float64, burst int) *bucketSet {
	if b.Rate <= 0 || b.Burst < 1 {
		b = Budget{rate, burst}
	}
	return newBucketSet(b.Rate, b.Burst, bucketKeys)
}

// bucketSet is a token bucket per Specula, least recently used first out.
type bucketSet struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	max     int
	buckets map[uuid.UUID]*list.Element // values are *bucket
	lru     *list.List                  // front: most recently used
}

type bucket struct {
	key    uuid.UUID
	tokens float64
	at     time.Time
}

func newBucketSet(rate float64, burst, limit int) *bucketSet {
	return &bucketSet{rate: rate, burst: float64(burst), max: limit, buckets: map[uuid.UUID]*list.Element{}, lru: list.New()}
}

// take uses one token of key at now. When there is none it reports false and how long until there is.
func (s *bucketSet) take(key uuid.UUID, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b *bucket
	if el, ok := s.buckets[key]; ok {
		s.lru.MoveToFront(el)
		b = el.Value.(*bucket)
		b.tokens = min(s.burst, b.tokens+s.rate*max(0, now.Sub(b.at).Seconds()))
		b.at = now
	} else {
		// shortcut: an evicted Specula starts again with a full burst; only reachable with more
		// than max active tokens on one replica, when the cost is a briefly looser limit.
		for len(s.buckets) >= s.max && s.lru.Len() > 0 {
			old := s.lru.Remove(s.lru.Back()).(*bucket)
			delete(s.buckets, old.key)
		}
		b = &bucket{key: key, tokens: s.burst, at: now}
		s.buckets[key] = s.lru.PushFront(b)
	}
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / s.rate * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// limited answers 429 with Retry-After when the Specula of the request used up its budget for
// endpoint; it runs after authentication.
func (g *Handler) limited(endpoint string, s *bucketSet, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := s.take(identity(r).SpeculaID, g.d.Clock.Now()); !ok {
			g.metrics.tokenLimited.WithLabelValues(endpoint).Inc()
			tooMany(w, r, wait)
			return
		}
		next(w, r)
	})
}
