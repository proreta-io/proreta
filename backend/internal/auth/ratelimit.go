package auth

import (
	"container/list"
	"sync"
	"time"

	"github.com/proreta-io/proreta/backend/internal/platform/clock"
)

// Limiter counts attempts per key in a fixed window that starts at the first attempt.
// It is in memory, so each api replica limits independently.
//
// An attempt is reserved (counted) before the expensive check runs, under the same lock as
// the limit test, so concurrent requests cannot all pass the test before any of them is
// counted. A successful attempt is refunded.
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	clock   clock.Clock
	entries map[string]*list.Element // values are *limitEntry
	lru     *list.List               // front: most recently used
}

type limitEntry struct {
	key      string
	start    time.Time
	attempts int
}

// NewLimiter allows up to limit attempts per key per window and tracks at most maxKeys keys.
func NewLimiter(limit int, window time.Duration, maxKeys int, c clock.Clock) *Limiter {
	return &Limiter{limit: limit, window: window, maxKeys: maxKeys, clock: c, entries: map[string]*list.Element{}, lru: list.New()}
}

// Reserve counts one attempt for key if the limit allows it. Otherwise it reports false and
// how long until the key may attempt again.
func (l *Limiter) Reserve(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	var e *limitEntry
	if el, ok := l.entries[key]; ok {
		l.lru.MoveToFront(el)
		e = el.Value.(*limitEntry)
		if now.Sub(e.start) >= l.window {
			e.start, e.attempts = now, 0
		}
	} else {
		// The least recently used key goes, so a flood of new keys cannot erase a recent counter at random.
		for len(l.entries) >= l.maxKeys && l.lru.Len() > 0 {
			l.remove(l.lru.Back())
		}
		e = &limitEntry{key: key, start: now}
		l.entries[key] = l.lru.PushFront(e)
	}
	if e.attempts >= l.limit {
		return false, e.start.Add(l.window).Sub(now)
	}
	e.attempts++
	return true, 0
}

// Refund gives back one reserved attempt (the attempt succeeded or never ran).
func (l *Limiter) Refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		if e := el.Value.(*limitEntry); e.attempts > 0 {
			e.attempts--
		}
	}
}

// Reset forgets key (after a successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.remove(el)
	}
}

func (l *Limiter) remove(el *list.Element) {
	delete(l.entries, el.Value.(*limitEntry).key)
	l.lru.Remove(el)
}
