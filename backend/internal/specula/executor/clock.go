package executor

import (
	"sync"
	"time"
)

// Clock is the time source of the executor; tests replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

// Now keeps the monotonic reading, which serverClock relies on; callers convert with UTC() to print.
func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// serverClock is the local clock corrected by the last offset to the server's clock. Between two
// observations it advances with the local clock's monotonic reading, so a step of the wall clock
// (NTP) does not move the slots.
type serverClock struct {
	local Clock
	mu    sync.Mutex
	set   bool
	base  time.Time // the server's time at localAt
	at    time.Time
}

func (s *serverClock) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return s.local.Now()
	}
	return s.base.Add(s.local.Now().Sub(s.at))
}

// Observe takes a server time that was answered between sent and received.
func (s *serverClock) Observe(server, sent, received time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set, s.base, s.at = true, server.Add(received.Sub(sent)/2), received
}

// Restore applies an offset saved by an earlier run, until the first observation.
func (s *serverClock) Restore(offset time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = s.local.Now()
	s.base, s.set = s.at.Add(offset), true
}

// Measured is Offset and whether one is known (observed or restored).
func (s *serverClock) Measured() (time.Duration, bool) {
	s.mu.Lock()
	set := s.set
	s.mu.Unlock()
	return s.Offset(), set
}

// Offset is the server's time minus the local time.
func (s *serverClock) Offset() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return 0
	}
	return s.base.Sub(s.at)
}
