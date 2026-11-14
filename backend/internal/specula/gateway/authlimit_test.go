package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/specula"
)

func TestAuthCache(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newAuthCache(5*time.Second, 3)
	id := specula.Identity{SpeculaID: uuid.New()}
	c.put("a", id, now)
	if got, ok := c.get("a", now.Add(4*time.Second)); !ok || got != id {
		t.Fatal("entry must live for the ttl")
	}
	if _, ok := c.get("a", now.Add(5*time.Second)); ok {
		t.Fatal("entry must expire after the ttl: revocation window")
	}
	for _, k := range []string{"b", "c", "d", "e", "f"} {
		c.put(k, id, now)
	}
	if len(c.m) > 3 {
		t.Fatalf("cache grew to %d, max 3", len(c.m))
	}
	// Expired entries go first when the cache is full.
	c.put("late", id, now.Add(time.Minute))
	if _, ok := c.get("late", now.Add(time.Minute)); !ok || len(c.m) > 3 {
		t.Fatal("new entry must be kept within the bound")
	}
}

// Tokens without the prefix are refused before the database, so a handler without a pool is enough
// to test the per-IP bucket.
func TestPerIPBucket(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	h := New(Deps{Clock: clk, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), TrustedProxyHops: 1})
	do := func(xff string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/specula/v1/hello", nil)
		r.Header.Set("Authorization", "Bearer nope")
		r.Header.Set("X-Forwarded-For", xff)
		r.RemoteAddr = "10.0.0.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < authFailLimit; i++ {
		// A client-supplied leftmost entry must not give it a fresh bucket: the proxy appends the real one.
		if w := do("198.51.100." + strconv.Itoa(i%200) + ", 203.0.113.7"); w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := do("1.2.3.4, 203.0.113.7")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over budget: %d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	}
	if w := do("203.0.113.8"); w.Code != http.StatusUnauthorized {
		t.Fatalf("another client must have its own bucket: %d", w.Code)
	}
	clk.Advance(authFailWindow)
	if w := do("203.0.113.7"); w.Code != http.StatusUnauthorized {
		t.Fatalf("budget must return after the window: %d", w.Code)
	}
}
