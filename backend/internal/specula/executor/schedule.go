package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

var (
	// errNoSchedule: the gateway has no /schedule (404), an older gateway.
	errNoSchedule = errors.New("executor: the gateway does not serve a schedule")
	// errTooLarge: the gateway answered 413, or sent more monitors than the contract allows.
	errTooLarge = errors.New("executor: the schedule has too many monitors")
)

const (
	// scheduleBodyLimit bounds the answer; 5000 monitors with headers are a few megabytes.
	scheduleBodyLimit      = 32 << 20
	defaultRefreshInterval = 5 * time.Minute
	minRefreshInterval     = 10 * time.Second
	maxRefreshInterval     = time.Hour
)

// counters count what happened to slots; the metrics read them.
type counters struct {
	checks, missedBusy, missedLate, missedExpired atomic.Int64
}

// runner runs the checks of one monitor.
type runner struct {
	m      wire.ScheduledMonitor
	cancel context.CancelFunc
}

// scheduler holds the cached schedule and one runner per active monitor. A runner sleeps until
// the next slot (anchor + n * interval) on the executor's corrected clock, runs the check
// and records the result under that slot. A runner does one check at a time, so a check that
// outlasts its interval skips the slots it covered.
type scheduler struct {
	e *Executor

	mu         sync.Mutex
	cur        *wire.Schedule
	validUntil time.Time
	runners    map[uuid.UUID]*runner
	ctx        context.Context // runners stop with it
	workCtx    context.Context // checks run on it, so a replaced runner's check still finishes
	wg         sync.WaitGroup
	expiredLog bool

	kick chan struct{} // a newer schedule version was announced
	n    counters
}

func newScheduler(e *Executor) *scheduler {
	return &scheduler{e: e, runners: map[uuid.UUID]*runner{}, kick: make(chan struct{}, 1)}
}

func (s *scheduler) start(ctx, workCtx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx, s.workCtx = ctx, workCtx
}

// stop ends the runners and waits for the checks in flight; after the drain time it cancels them.
func (s *scheduler) stop(cancelWork context.CancelFunc) {
	s.mu.Lock()
	for id, r := range s.runners {
		r.cancel()
		delete(s.runners, id)
	}
	s.cur = nil
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(drainTimeout):
		s.e.cfg.Log.Warn("drain timeout; cancelling checks in flight")
		cancelWork()
		<-done
	}
}

func (s *scheduler) issuedAt() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	t := s.cur.IssuedAt
	return &t
}

func (s *scheduler) validity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validUntil
}

// running is the number of monitors with a runner.
func (s *scheduler) running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runners)
}

func (s *scheduler) version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ""
	}
	return s.cur.Version
}

// notifyVersion tells the refresh loop that the gateway has another version than the one in use.
func (s *scheduler) notifyVersion(v string) {
	if v == "" || v == s.version() {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// loadCache starts the runners from the saved schedule, if there is one and it is still valid.
func (s *scheduler) loadCache() {
	b, err := s.e.store.readFile(scheduleFile)
	if err != nil || b == nil {
		if err != nil {
			s.e.cfg.Log.Warn("cannot read the cached schedule", "error", err)
		}
		return
	}
	var sc wire.Schedule
	if err := json.Unmarshal(b, &sc); err != nil || validate(&sc) != nil {
		s.e.cfg.Log.Warn("the cached schedule is unusable; ignoring it")
		return
	}
	valid := sc.ValidUntil
	if v := s.e.store.snapshot().ScheduleValidUntil; v != nil && v.After(valid) {
		valid = *v
	}
	s.apply(&sc, valid)
	s.e.cfg.Log.Info("started from the cached schedule", "monitors", len(sc.Monitors), "valid_until", valid)
}

func validate(sc *wire.Schedule) error {
	if len(sc.Monitors) > wire.MaxSchedule {
		return fmt.Errorf("%w: %d (limit %d)", errTooLarge, len(sc.Monitors), wire.MaxSchedule)
	}
	return nil
}

// refresh fetches the schedule, sending the version in use. A new schedule replaces the runners;
// a 304 only extends the time it is valid.
func (s *scheduler) refresh(ctx context.Context) error {
	e := s.e
	hdr := http.Header{}
	if v := s.version(); v != "" {
		hdr.Set("If-None-Match", `"`+v+`"`)
	}
	sent := e.cfg.Clock.Now()
	status, h, body, err := e.do(ctx, http.MethodGet, "/schedule", nil, hdr, scheduleBodyLimit)
	if err != nil {
		return err
	}
	received := e.cfg.Clock.Now()
	switch status {
	case http.StatusOK:
		var sc wire.Schedule
		if err := json.Unmarshal(body, &sc); err != nil {
			return errors.New("/schedule: unreadable answer")
		}
		if err := validate(&sc); err != nil {
			return err // keep running the schedule in use until it expires
		}
		if !sc.ServerTime.IsZero() {
			e.observeServerTime(sc.ServerTime, sent, received)
		}
		if err := e.store.writeAtomic(scheduleFile, body); err != nil {
			e.cfg.Log.Warn("cannot cache the schedule", "error", err)
		}
		s.persistValidUntil(sc.ValidUntil)
		s.apply(&sc, sc.ValidUntil)
		return nil
	case http.StatusNotModified:
		if t, err := time.Parse(time.RFC3339, h.Get(wire.HeaderScheduleValidUntil)); err == nil {
			s.extend(t)
		}
		return nil
	case http.StatusNotFound:
		return errNoSchedule
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: the gateway refuses to serve it", errTooLarge)
	}
	return &statusError{Path: "/schedule", Status: status, RetryAfter: retryAfter(h)}
}

func (s *scheduler) persistValidUntil(t time.Time) {
	if err := s.e.store.update(func(st *state) { st.ScheduleValidUntil = &t }); err != nil {
		s.e.cfg.Log.Warn("cannot save the schedule's validity", "error", err)
	}
}

// extend moves validUntil forward (never back).
func (s *scheduler) extend(t time.Time) {
	s.mu.Lock()
	later := t.After(s.validUntil)
	if later {
		s.validUntil, s.expiredLog = t, false
	}
	s.mu.Unlock()
	if later {
		s.persistValidUntil(t)
	}
}

// apply makes sc the schedule in use: runners of monitors that are new or changed start, those
// that are gone, paused or changed stop. A monitor that did not change keeps its runner.
func (s *scheduler) apply(sc *wire.Schedule, validUntil time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur, s.validUntil, s.expiredLog = sc, validUntil, false
	want := make(map[uuid.UUID]wire.ScheduledMonitor, len(sc.Monitors))
	for _, m := range sc.Monitors {
		if !m.Active {
			continue
		}
		if m.IntervalSeconds <= 0 || (m.Type != "" && m.Type != wire.TypeHTTP && m.Type != wire.TypeTCP) {
			// A type this executor does not know: an upgrade fixes it. It reports nothing for it.
			s.e.cfg.Log.Warn("a monitor of the schedule cannot be run", "monitor_id", m.MonitorID, "type", m.Type)
			continue
		}
		want[m.MonitorID] = m
	}
	for id, r := range s.runners {
		if m, ok := want[id]; !ok || !reflect.DeepEqual(m, r.m) {
			r.cancel()
			delete(s.runners, id)
		}
	}
	for id, m := range want {
		if _, ok := s.runners[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.runners[id] = &runner{m: m, cancel: cancel}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.runLoop(ctx, m) }()
	}
}

// slotAtOrAfter is the first slot of anchor + n * interval that is not before t.
func slotAtOrAfter(anchor int64, interval int, t time.Time) int64 {
	a := time.Unix(anchor, 0)
	if !t.After(a) {
		return anchor
	}
	step := time.Duration(interval) * time.Second
	return anchor + int64((t.Sub(a)+step-1)/step)*int64(interval)
}

// waitUntil sleeps until the corrected clock reaches t; the clock may be corrected meanwhile, so
// it looks again after every sleep. It returns false when ctx ends first.
func (s *scheduler) waitUntil(ctx context.Context, t time.Time) bool {
	for {
		d := t.Sub(s.e.now())
		if d <= 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.e.cfg.Clock.After(d):
		}
	}
}

func (s *scheduler) runLoop(ctx context.Context, m wire.ScheduledMonitor) {
	e := s.e
	step := int64(m.IntervalSeconds)
	interval := time.Duration(step) * time.Second
	next := slotAtOrAfter(m.Anchor, m.IntervalSeconds, e.now())
	for {
		slot := time.Unix(next, 0)
		if !s.waitUntil(ctx, slot) {
			return
		}
		late := e.now().Sub(slot)
		e.met.lateness.Observe(max(late, 0).Seconds())
		if late >= interval {
			// Asleep or stalled for a whole interval: you cannot check the past, start at the next slot.
			after := slotAtOrAfter(m.Anchor, m.IntervalSeconds, e.now())
			s.n.missedLate.Add((after - next) / step)
			next = after
			continue
		}
		if s.expired(slot) {
			s.n.missedExpired.Add(1)
		} else {
			s.check(ctx, m, next)
		}
		after := max(slotAtOrAfter(m.Anchor, m.IntervalSeconds, e.now()), next+step)
		s.n.missedBusy.Add((after-next)/step - 1)
		next = after
	}
}

// expired reports whether the schedule is out of date at slot, and says so once.
func (s *scheduler) expired(slot time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slot.Before(s.validUntil) {
		return false
	}
	if !s.expiredLog {
		s.expiredLog = true
		s.e.cfg.Log.Error("the schedule expired and Proreta cannot be reached; no checks are started until it is renewed", "valid_until", s.validUntil)
	}
	return true
}

// check runs the check of one slot and records its result.
func (s *scheduler) check(ctx context.Context, m wire.ScheduledMonitor, slot int64) {
	e := s.e
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-e.slots }()
	defer func() {
		if r := recover(); r != nil {
			e.cfg.Log.Error("a check panicked", "monitor_id", m.MonitorID, "panic_type", fmt.Sprintf("%T", r))
		}
	}()
	if m.TimeoutSeconds <= 0 {
		m.TimeoutSeconds = int(checks.DefaultTimeout / time.Second)
	}
	res := e.cfg.Prober.Probe(s.workCtx, jobRequest(wire.Job{
		Type: m.Type, URL: m.URL, Host: m.Host, Port: m.Port, Method: m.Method, Headers: m.Headers, Body: m.Body,
		ExpectedStatus: m.ExpectedStatus, TimeoutSeconds: m.TimeoutSeconds,
	}))
	if res.Cancelled {
		return // shutdown cut it short; the slot stays without a result
	}
	s.n.checks.Add(1)
	e.record(wire.Report{
		MonitorID: m.MonitorID, Epoch: m.Epoch, Slot: slot, OK: res.OK, StatusCode: res.StatusCode,
		LatencyMs: res.LatencyMs, ErrorKind: res.ErrorKind, TLSNotAfter: res.TLSNotAfter,
	})
}

// refreshEvery is how long to wait between refreshes, as the gateway asks.
func (s *scheduler) refreshEvery() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.cur.RefreshAfterSeconds <= 0 {
		return defaultRefreshInterval
	}
	return min(max(time.Duration(s.cur.RefreshAfterSeconds)*time.Second, minRefreshInterval), maxRefreshInterval)
}

// backoff is 1 s doubling to 60 s, with jitter, for the nth failure in a row.
func backoff(n int) time.Duration {
	d := min(time.Second<<min(n, 6), 60*time.Second)
	return d/2 + rand.N(d/2+1) //nolint:gosec // jitter, not a secret
}

// refreshLoop keeps the schedule fresh: on the gateway's interval, at once when a newer version
// is announced, and with backoff while it fails (the expiry of the schedule in use goes on).
func (s *scheduler) refreshLoop(ctx context.Context, rejected chan<- error) {
	wait := s.refreshEvery()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.e.cfg.Clock.After(wait):
		case <-s.kick:
		}
		err := s.refresh(ctx)
		switch {
		case err == nil:
			fails, wait = 0, s.refreshEvery()
			continue
		case errors.Is(err, ErrRejected):
			select {
			case rejected <- err:
			default:
			}
			return
		}
		fails++
		wait = backoff(fails)
		var se *statusError
		if errors.As(err, &se) {
			wait = max(wait, se.RetryAfter)
		}
		if fails == 1 || fails%10 == 0 {
			s.e.cfg.Log.Warn("cannot refresh the schedule", "error", err, "attempt", fails)
		}
	}
}

// record keeps a finished slot until the reporter has sent it.
func (e *Executor) record(r wire.Report) {
	e.buf.Append(r)
	if !r.OK {
		e.rep.poke() // failures go out at once, they start incidents
	} else if e.buf.Depth() >= wire.MaxResults {
		e.rep.poke()
	}
}
