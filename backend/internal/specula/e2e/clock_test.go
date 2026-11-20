package e2e_test

import (
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// simClock is a manual clock for the gateway (Now) and the executor (Now, After). Advance fires the
// timers in order and waits for the goroutines they wake to settle, so hours of executor time pass
// in moments. busy counts requests the link is serving; settle waits for them.
type simClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*simTimer
	regs   int
	busy   atomic.Int32
}

type simTimer struct {
	at time.Time
	ch chan time.Time
}

func newSimClock(t time.Time) *simClock { return &simClock{now: t.UTC()} }

func (c *simClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *simClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.regs++
	t := &simTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t.ch
	}
	c.timers = append(c.timers, t)
	return t.ch
}

// forget drops the timers of an executor that has stopped: nobody reads them any more.
func (c *simClock) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers = nil
}

func (c *simClock) settle() {
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

// startup is how long, in real time, an executor that was just started or restarted gets to fetch its
// schedule (or read the cache) and arm the timers of its checks. A starved goroutine that misses it
// starts one slot late and the test then misses a slot; it is paid once per start, so it is long.
const startup = 500 * time.Millisecond

// quiet waits, in real time, until no timer was armed and no request served for d.
func (c *simClock) quiet(d time.Duration) {
	for last, since := -1, time.Now(); time.Since(since) < d; time.Sleep(time.Millisecond) {
		c.mu.Lock()
		r := c.regs
		c.mu.Unlock()
		if r != last || c.busy.Load() != 0 {
			last, since = r, time.Now()
		}
	}
}

// Advance moves the clock forward by d, firing every timer that falls due on the way.
func (c *simClock) Advance(d time.Duration) {
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
		now, before := c.now, c.regs
		c.mu.Unlock()
		t.ch <- now
		c.afterFire(t, before)
	}
}

// afterFire waits for the goroutine a timer woke to take the tick and to arm its next timer.
func (c *simClock) afterFire(t *simTimer, regsBefore int) {
	// A tick nobody takes within 25 ms belongs to a goroutine that gave up that timer (a select that
	// another case won): not worth waiting for.
	for end := time.Now().Add(25 * time.Millisecond); len(t.ch) > 0; time.Sleep(100 * time.Microsecond) {
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

// skewed is the executor's view of the clock when its own clock is off by d.
type skewed struct {
	c *simClock
	d time.Duration
}

func (s skewed) Now() time.Time                         { return s.c.Now().Add(s.d) }
func (s skewed) After(d time.Duration) <-chan time.Time { return s.c.After(d) }

// counted wraps c so that every request counts as busy from the moment it is sent until its response
// body is closed. The link counts a request only while its handler runs, which leaves a window, under
// load of many milliseconds, where a request is on the wire and the clock looks quiet.
func (c *simClock) counted(client *http.Client) *http.Client {
	cp := *client
	cp.Transport = countingTransport{c, client.Transport}
	return &cp
}

type countingTransport struct {
	c    *simClock
	next http.RoundTripper
}

func (t countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.c.busy.Add(1)
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(r)
	if err != nil {
		t.c.busy.Add(-1)
		return resp, err
	}
	resp.Body = &countedBody{ReadCloser: resp.Body, c: t.c}
	return resp, nil
}

type countedBody struct {
	io.ReadCloser
	c    *simClock
	once sync.Once
}

func (b *countedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.c.busy.Add(-1) })
	return err
}
