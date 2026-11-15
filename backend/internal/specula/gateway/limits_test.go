package gateway

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBucketBurstRefillAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newBucketSet(scheduleRate, scheduleBurst, 10)
	k := uuid.New()
	for i := 0; i < scheduleBurst; i++ {
		if ok, _ := s.take(k, now); !ok {
			t.Fatalf("call %d of the burst refused", i)
		}
	}
	ok, wait := s.take(k, now)
	if ok || wait != 10*time.Second {
		t.Fatalf("over the burst: ok=%v wait=%v, want a refusal for one interval (10s)", ok, wait)
	}
	// Half an interval later the wait shrinks by that much; a full one brings one token back, not more.
	if ok, wait := s.take(k, now.Add(5*time.Second)); ok || wait != 5*time.Second {
		t.Fatalf("after 5s: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := s.take(k, now.Add(10*time.Second)); !ok {
		t.Fatal("one token must be back after 10s")
	}
	if ok, _ := s.take(k, now.Add(10*time.Second)); ok {
		t.Fatal("only one token came back")
	}
	// A long pause refills to the burst and no further.
	later := now.Add(time.Hour)
	for i := 0; i < scheduleBurst; i++ {
		if ok, _ := s.take(k, later); !ok {
			t.Fatalf("after a pause, call %d refused", i)
		}
	}
	if ok, _ := s.take(k, later); ok {
		t.Fatal("the bucket refilled past its burst")
	}
	// Another token has its own bucket.
	if ok, _ := s.take(uuid.New(), later); !ok {
		t.Fatal("tokens must not share a bucket")
	}
}

func TestBucketStateIsBoundedAndKeepsRecentKeys(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newBucketSet(1, 1, 3)
	hot := uuid.New()
	s.take(hot, now)
	for i := 0; i < 50; i++ {
		s.take(uuid.New(), now)
		s.take(hot, now) // keeps the hot key recent
		if len(s.buckets) > 3 || s.lru.Len() > 3 {
			t.Fatalf("%d buckets, max 3", len(s.buckets))
		}
	}
	if ok, _ := s.take(hot, now); ok {
		t.Fatal("the recently used key lost its state: it must still be empty")
	}
}
