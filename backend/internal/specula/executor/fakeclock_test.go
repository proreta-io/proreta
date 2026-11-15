package executor

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// fakeClock is a manual clock. Advance fires the timers in order and waits for the goroutines
// they wake to settle (to register their next timer), so a test can step through hours of
// executor time in a moment.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	regs   int
	busy   atomic.Int32 // requests the fake gateway is serving; settle waits for them
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.regs++
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t.ch
	}
	c.timers = append(c.timers, t)
	return t.ch
}

func (c *fakeClock) settle() {
	for stable, last := 0, -1; stable < 4; {
		time.Sleep(time.Millisecond)
		c.mu.Lock()
		r := c.regs
		c.mu.Unlock()
		if r == last && c.busy.Load() == 0 {
			stable++
		} else {
			stable, last = 0, r
		}
	}
}

// Advance moves the clock forward by d, firing every timer that falls due on the way.
func (c *fakeClock) Advance(d time.Duration) {
	c.settle()
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.Slice(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		if len(c.timers) == 0 || c.timers[0].at.After(target) {
			c.now = target
			c.mu.Unlock()
			c.settle()
			return
		}
		t := c.timers[0]
		c.timers = c.timers[1:]
		if t.at.After(c.now) {
			c.now = t.at
		}
		now := c.now
		before := c.regs
		c.mu.Unlock()
		t.ch <- now
		c.afterFire(t, before)
	}
}

// afterFire waits for the goroutine a timer woke to take the tick and to arm its next timer, which
// is the sign that it has finished what the tick started. A tick nobody takes (the goroutine
// left its select by another way) is not waited for.
func (c *fakeClock) afterFire(t *fakeTimer, regsBefore int) {
	for end := time.Now().Add(100 * time.Millisecond); len(t.ch) > 0; time.Sleep(200 * time.Microsecond) {
		if time.Now().After(end) {
			return
		}
	}
	for end := time.Now().Add(100 * time.Millisecond); time.Now().Before(end); time.Sleep(200 * time.Microsecond) {
		c.mu.Lock()
		r := c.regs
		c.mu.Unlock()
		if r != regsBefore {
			break
		}
	}
	c.settle()
}
