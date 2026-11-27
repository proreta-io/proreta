// Package executor is the Specula executor: it runs inside a customer's network, runs checks
// with the shared prober and reports the results to the gateway over HTTPS. In schedule mode it
// fetches the schedule and runs every check at its slot on its own clock; against a server that
// does not offer that it claims due checks (pull mode). It only makes outbound calls.
package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// ErrRejected means the gateway refused the token: it is wrong or was revoked. The executor stops.
var ErrRejected = errors.New("executor: the token was rejected")

// Defaults.
const (
	DefaultConcurrency  = 50
	DefaultPollInterval = 5 * time.Second
	helloInterval       = time.Minute
	flushInterval       = time.Second
	drainTimeout        = 15 * time.Second
	callTimeout         = 20 * time.Second
	// A result older than this is no longer accepted by the gateway (its window is one minute).
	maxResultAge = 50 * time.Second
	maxQueued    = 2000
)

// Config configures an Executor.
type Config struct {
	// BaseURL is the Proreta URL without a trailing slash, for example https://proreta.example.com.
	BaseURL string
	Token   string
	// Version and Hostname are reported in the heartbeat.
	Version  string
	Hostname string
	// Concurrency caps checks in flight; PollInterval is how often it asks for work.
	Concurrency  int
	PollInterval time.Duration
	// HTTP talks to the gateway; Prober runs the checks.
	HTTP   *http.Client
	Prober checks.Prober
	Log    *slog.Logger
	// Clock defaults to the system clock.
	Clock Clock
	// StateDir keeps the cached schedule and the clock offset. Empty means memory only: a restart
	// then needs the gateway before it can check anything.
	StateDir string
	// BufferMaxBytes and BufferMaxAge bound the result buffer; whichever is hit first drops the
	// oldest segment. Zero takes the defaults (256 MiB, 24 hours).
	BufferMaxBytes int64
	BufferMaxAge   time.Duration
}

// Executor runs checks for one Specula.
type Executor struct {
	cfg     Config
	slots   chan struct{}
	running sync.WaitGroup

	clk   *serverClock
	store *store
	sched *scheduler
	// instanceID is random per process; two ids from one token mean two executors share it.
	instanceID string

	// plainHello is set when the gateway rejected a hello with capabilities (an older gateway,
	// which hands out tcp jobs without them). Only the goroutine that says hello touches it.
	plainHello bool
	// noCounters is set when a hello with slotCounters was answered 400 (a gateway downgraded
	// mid-run): the counters stay off for the run, and the mode does not change. Same goroutine as plainHello.
	noCounters bool
	// pullOnly is set when the gateway does not serve a schedule: stop announcing schedule/1.
	pullOnly atomic.Bool
	modes    atomic.Pointer[[]string] // modes of the last hello answer

	mu      sync.Mutex
	pending []wire.Result
	// buf holds the finished slots of schedule mode until they are reported.
	buf *buffer
	rep *reporter
	met metrics
}

// New returns an Executor.
func New(cfg Config) *Executor {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: callTimeout}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	e := &Executor{
		cfg: cfg, slots: make(chan struct{}, cfg.Concurrency), instanceID: hex.EncodeToString(id),
		clk: &serverClock{local: cfg.Clock}, store: newStore(cfg.StateDir),
	}
	e.sched = newScheduler(e)
	e.rep = newReporter(e)
	e.met = newMetrics()
	return e
}

// loadInstanceID takes the id saved in the state directory, or saves a new one. Without a
// state directory (or one that cannot be written) the id lasts one process.
func (e *Executor) loadInstanceID() {
	if id := e.store.snapshot().InstanceID; id != "" {
		e.instanceID = id
		return
	}
	if err := e.store.update(func(s *state) { s.InstanceID = e.instanceID }); err != nil {
		e.cfg.Log.Warn("cannot save the instance id; it changes at the next start", "error", err)
	}
}

// logStartup says what the executor starts with. No secrets: counts and times only.
func (e *Executor) logStartup() {
	st := e.buf.Stats()
	attrs := []any{
		"instance_id", e.instanceID, "state_dir", e.store.enabled(),
		"buffer_results", st.Depth, "buffer_bytes", st.Bytes, "buffer_degraded", st.Degraded,
		"clock_offset", e.clk.Offset().Round(time.Millisecond).String(),
	}
	if slot := e.buf.OldestSlot(); slot > 0 {
		attrs = append(attrs, "oldest_unsent_at", time.Unix(slot, 0).UTC())
	}
	if issued := e.sched.issuedAt(); issued != nil {
		attrs = append(attrs, "schedule_issued_at", *issued, "schedule_valid_until", e.sched.validity(), "schedule_monitors", e.sched.running())
	} else {
		attrs = append(attrs, "schedule", "none")
	}
	e.cfg.Log.Info("executor state at start", attrs...)
}

// now is the server's time as far as the executor knows it; slots are computed from it.
func (e *Executor) now() time.Time { return e.clk.Now() }

// Run says hello, then runs checks until ctx is cancelled: from the schedule when the gateway
// offers it, otherwise by claiming jobs. It then waits for checks in flight and reports what it
// has. It returns ErrRejected when the token stops working.
func (e *Executor) Run(ctx context.Context) error {
	if err := e.store.load(); err != nil {
		e.cfg.Log.Warn("cannot read the saved state; starting without it", "error", err)
	}
	e.loadInstanceID()
	e.buf = openBuffer(bufferConfig{MaxBytes: e.cfg.BufferMaxBytes, MaxAge: e.cfg.BufferMaxAge}, e.store, e.cfg.Log, e.now)
	defer e.buf.Close()
	if off := e.store.snapshot().ClockOffsetNanos; off != 0 {
		e.clk.Restore(time.Duration(off))
	}
	// Checks and reports outlive the stop signal until the drain ends.
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()
	e.sched.start(ctx, workCtx)
	// A cached schedule starts checking before the gateway has been reached: a restart without a
	// connection must not leave a gap.
	e.sched.loadCache()
	defer e.sched.stop(cancelWork)
	e.logStartup()

	if err := e.helloUntilOK(ctx); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	if modes := e.modes.Load(); e.plainHello || modes == nil || !hasMode(*modes, wire.ModeSchedule) {
		e.sched.stop(cancelWork) // an older gateway, or a downgrade: only pull mode works
		return e.runPull(ctx, workCtx, cancelWork)
	}
	if err := e.sched.refresh(ctx); err != nil {
		switch {
		case errors.Is(err, ErrRejected):
			return err
		case errors.Is(err, errNoSchedule):
			e.cfg.Log.Warn("the gateway does not serve a schedule; using pull mode")
			e.sched.stop(cancelWork)
			e.pullOnly.Store(true)
			if err := e.hello(ctx); err != nil && errors.Is(err, ErrRejected) {
				return err
			}
			return e.runPull(ctx, workCtx, cancelWork)
		default:
			e.cfg.Log.Warn("cannot fetch the schedule; trying again", "error", err)
		}
	}
	return e.runSchedule(ctx, cancelWork)
}

func hasMode(modes []string, m string) bool {
	for _, x := range modes {
		if x == m {
			return true
		}
	}
	return false
}

// background runs the hello loop and the given loops until ctx ends or a token is rejected; the
// returned function stops them and reports what ended them.
func (e *Executor) background(ctx context.Context, loops ...func(ctx context.Context, rejected chan<- error)) (rejected <-chan error, stop func()) {
	bgCtx, cancel := context.WithCancel(ctx)
	rej := make(chan error, 1)
	var wg sync.WaitGroup
	all := append([]func(context.Context, chan<- error){func(c context.Context, r chan<- error) {
		e.loop(c, helloInterval, func() error { return e.hello(c) }, r)
	}}, loops...)
	for _, fn := range all {
		wg.Add(1)
		go func() { defer wg.Done(); fn(bgCtx, rej) }()
	}
	return rej, func() { cancel(); wg.Wait() }
}

// runSchedule runs until ctx ends. The checks are the scheduler's runners; this keeps the
// gateway side going: hello every minute and the schedule refresh.
func (e *Executor) runSchedule(ctx context.Context, cancelWork context.CancelFunc) error {
	rejected, stopBackground := e.background(ctx, e.sched.refreshLoop, e.rep.loop)
	defer stopBackground()
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-rejected:
	}
	stopBackground()
	// Let the checks in flight finish, then send what there is once.
	e.sched.stop(cancelWork)
	if runErr == nil {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
		defer cancel()
		if err := e.rep.drain(flushCtx, false); err != nil && !errors.Is(err, ErrRejected) {
			e.cfg.Log.Warn("final report failed; the results stay buffered", "error", err, "results", e.buf.Depth())
		}
	}
	return runErr
}

// runPull claims and runs jobs until ctx is cancelled or the token is rejected.
func (e *Executor) runPull(ctx, workCtx context.Context, cancelWork context.CancelFunc) error {
	rejected, stopBackground := e.background(ctx, func(c context.Context, r chan<- error) {
		e.loop(c, flushInterval, func() error { return e.flush(workCtx) }, r)
	})
	defer stopBackground()

	poll := time.NewTicker(e.cfg.PollInterval)
	defer poll.Stop()
	var runErr error
loop:
	for {
		if err := e.claim(ctx, workCtx); err != nil {
			if errors.Is(err, ErrRejected) {
				runErr = err
				break loop
			}
			e.cfg.Log.Warn("claim failed", "error", err)
		}
		select {
		case <-ctx.Done():
			break loop
		case err := <-rejected:
			runErr = err
			break loop
		case <-poll.C:
		}
	}

	stopBackground()
	// Stop claiming; give checks in flight time to finish, then cancel the rest.
	drained := make(chan struct{})
	go func() { e.running.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainTimeout):
		e.cfg.Log.Warn("drain timeout; cancelling checks in flight")
		cancelWork()
		<-drained
	}
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	if err := e.flush(flushCtx); err != nil && runErr == nil && !errors.Is(err, ErrRejected) {
		e.cfg.Log.Warn("final report failed; results are dropped", "error", err, "results", e.queued())
	}
	return runErr
}

// loop runs fn every interval until ctx is done; a rejected token ends the executor.
func (e *Executor) loop(ctx context.Context, interval time.Duration, fn func() error, rejected chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.cfg.Clock.After(interval):
			if err := fn(); err != nil {
				if errors.Is(err, ErrRejected) {
					select {
					case rejected <- err:
					default:
					}
					return
				}
				e.cfg.Log.Warn("background call failed", "error", err)
			}
		}
	}
}

func (e *Executor) helloUntilOK(ctx context.Context) error {
	wait := time.Second
	for {
		err := e.hello(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrRejected) {
			return err
		}
		e.cfg.Log.Warn("cannot reach Proreta; retrying", "error", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return nil
		case <-e.cfg.Clock.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (e *Executor) hello(ctx context.Context) error {
	var out wire.HelloResponse
	req := wire.HelloRequest{Version: e.cfg.Version, Hostname: e.cfg.Hostname}
	if !e.plainHello {
		req.Capabilities = []string{wire.CapTCP, wire.CapTCPProbe}
		if !e.pullOnly.Load() {
			req.Capabilities = append(req.Capabilities, wire.CapSchedule, wire.CapSchedule2)
			req.InstanceID = e.instanceID
			req.ScheduleIssuedAt = e.sched.issuedAt()
			if off, ok := e.clk.Measured(); ok {
				ms := off.Milliseconds()
				req.ClockOffsetMs = &ms
			}
			if e.buf != nil {
				depth, degraded := e.buf.Depth(), e.buf.Degraded()
				req.BufferDepth, req.BufferDegraded = &depth, &degraded
				if slot := e.buf.OldestSlot(); slot > 0 {
					t := time.Unix(slot, 0).UTC()
					req.OldestUnsentAt = &t
				}
			}
			// Only after a hello was answered with schedule/2: an older gateway rejects the field.
			if modes := e.modes.Load(); !e.noCounters && modes != nil && hasMode(*modes, wire.ModeSchedule2) {
				c := e.slotCounters()
				req.SlotCounters = &c
			}
		}
	}
	sent := e.cfg.Clock.Now()
	code, err := e.call(ctx, "/hello", req, &out)
	if code == http.StatusBadRequest && req.SlotCounters != nil {
		// Never a change of mode: the same hello without the counters, schedule/1 still announced.
		e.noCounters = true
		e.cfg.Log.Warn("the gateway rejected the slot counters; not sending them any more until restart")
		req.SlotCounters = nil
		sent = e.cfg.Clock.Now()
		code, err = e.call(ctx, "/hello", req, &out)
	}
	if code == http.StatusBadRequest && !e.plainHello {
		e.plainHello = true
		sent = e.cfg.Clock.Now()
		_, err = e.call(ctx, "/hello", wire.HelloRequest{Version: e.cfg.Version, Hostname: e.cfg.Hostname}, &out)
	}
	if err != nil {
		return err
	}
	received := e.cfg.Clock.Now()
	if skew := received.Sub(out.ServerTime); skew > 30*time.Second || skew < -30*time.Second {
		e.cfg.Log.Warn("the clock differs from Proreta's; results may be rejected", "skew", skew.Round(time.Second).String())
	}
	e.observeServerTime(out.ServerTime, sent, received)
	e.modes.Store(&out.Modes)
	e.sched.notifyVersion(out.ScheduleVersion)
	e.rep.poke() // the link works: send what waits
	return nil
}

// observeServerTime corrects the executor's clock and saves the offset for a restart without
// a connection.
func (e *Executor) observeServerTime(server, sent, received time.Time) {
	e.clk.Observe(server, sent, received)
	off := e.clk.Offset()
	if d := off - time.Duration(e.store.snapshot().ClockOffsetNanos); d > time.Second || d < -time.Second {
		if err := e.store.update(func(s *state) { s.ClockOffsetNanos = int64(off) }); err != nil {
			e.cfg.Log.Warn("cannot save the clock offset", "error", err)
		}
	}
}

// claim asks for as many jobs as there are free slots and starts them.
func (e *Executor) claim(ctx, workCtx context.Context) error {
	free := cap(e.slots) - len(e.slots)
	if free == 0 || ctx.Err() != nil {
		return nil
	}
	var out wire.ClaimResponse
	if _, err := e.call(ctx, "/claim", wire.ClaimRequest{Max: min(free, wire.MaxClaim)}, &out); err != nil {
		return err
	}
	for _, j := range out.Jobs {
		e.slots <- struct{}{}
		e.running.Add(1)
		go func() {
			defer func() { <-e.slots; e.running.Done() }()
			e.run(workCtx, j)
		}()
	}
	return nil
}

func (e *Executor) run(ctx context.Context, j wire.Job) {
	defer func() {
		if r := recover(); r != nil {
			// The lease expires and the check is retried; the panic value may hold the URL.
			e.cfg.Log.Error("a check panicked; its lease will expire", "monitor_id", j.MonitorID, "panic_type", fmt.Sprintf("%T", r))
		}
	}()
	if !j.LeaseUntil.After(e.cfg.Clock.Now()) {
		return
	}
	if j.Type != "" && j.Type != wire.TypeHTTP && j.Type != wire.TypeTCP {
		return // a type this executor does not know: the lease expires; an upgrade fixes it
	}
	res := e.cfg.Prober.Probe(ctx, jobRequest(j))
	if res.Cancelled {
		return // shutdown cut the check short; the lease expires and it runs again
	}
	e.enqueue(wire.Result{
		MonitorID: j.MonitorID, LeaseToken: j.LeaseToken, CheckedAt: res.CheckedAt, OK: res.OK, StatusCode: res.StatusCode,
		LatencyMs: res.LatencyMs, ErrorKind: res.ErrorKind, TLSNotAfter: res.TLSNotAfter,
	})
}

func (e *Executor) enqueue(r wire.Result) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = append(e.pending, r)
	if over := len(e.pending) - maxQueued; over > 0 {
		e.pending = e.pending[over:]
		e.cfg.Log.Warn("too many unreported results; the oldest were dropped", "dropped", over)
	}
}

func (e *Executor) queued() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}

// flush reports the queued results. They stay queued when the gateway cannot be reached or is
// busy, and are dropped when too old to be accepted.
func (e *Executor) flush(ctx context.Context) error {
	for {
		batch := e.takeBatch()
		if len(batch) == 0 {
			return nil
		}
		var out wire.ResultsResponse
		status, err := e.call(ctx, "/results", wire.ResultsRequest{Results: batch}, &out)
		if err != nil {
			if status == http.StatusBadRequest {
				e.cfg.Log.Error("the gateway refused a batch of results; dropped", "results", len(batch))
				continue
			}
			e.requeue(batch)
			return err
		}
		for _, o := range out.Outcomes {
			if o.Outcome != wire.OutcomeRecorded {
				e.cfg.Log.Info("a result was not recorded", "monitor_id", o.MonitorID, "outcome", o.Outcome)
			}
		}
	}
}

func (e *Executor) takeBatch() []wire.Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := e.cfg.Clock.Now().Add(-maxResultAge)
	fresh := e.pending[:0]
	for _, r := range e.pending {
		if r.CheckedAt.After(cutoff) {
			fresh = append(fresh, r)
		}
	}
	e.pending = fresh
	n := min(len(e.pending), wire.MaxResults)
	batch := append([]wire.Result(nil), e.pending[:n]...)
	e.pending = e.pending[n:]
	return batch
}

func (e *Executor) requeue(batch []wire.Result) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = append(append([]wire.Result(nil), batch...), e.pending...)
}

// statusError is an answer other than the one expected. RetryAfter is the server's wish, if any.
type statusError struct {
	Path       string
	Status     int
	RetryAfter time.Duration
}

func (e *statusError) Error() string { return fmt.Sprintf("%s: status %d", e.Path, e.Status) }

// do sends one request and returns the status, headers and body of the answer. A 401 is
// ErrRejected; every other status is for the caller to judge.
func (e *Executor) do(ctx context.Context, method, path string, in any, header http.Header, limit int64) (int, http.Header, []byte, error) {
	var rd io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, nil, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(e.cfg.BaseURL, "/")+wire.BasePath+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+e.cfg.Token)
	req.Header.Set("User-Agent", "proreta-specula/"+e.cfg.Version)
	resp, err := e.cfg.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("%s: %w", path, err)
	}
	if int64(len(body)) > limit {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("%s: answer larger than %d bytes", path, limit)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, resp.Header, body, ErrRejected
	}
	return resp.StatusCode, resp.Header, body, nil
}

// retryAfter reads a Retry-After header in seconds (the form the gateway sends), capped.
func retryAfter(h http.Header) time.Duration {
	n, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || n < 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, 5*time.Minute)
}

// call posts in as JSON and decodes a 200 answer into out. It returns the HTTP status (0 when no
// answer came). A 401 is ErrRejected, any other status a *statusError.
func (e *Executor) call(ctx context.Context, path string, in, out any) (int, error) {
	status, h, body, err := e.do(ctx, http.MethodPost, path, in, nil, 4<<20)
	if err != nil {
		return status, err
	}
	if status != http.StatusOK {
		return status, &statusError{Path: path, Status: status, RetryAfter: retryAfter(h)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return status, fmt.Errorf("%s: unreadable answer", path)
	}
	return status, nil
}

// jobRequest is the check a job describes.
func jobRequest(j wire.Job) checks.Request {
	req := checks.Request{
		TCP: j.Type == wire.TypeTCP, Host: j.Host, Port: j.Port,
		URL: j.URL, Method: j.Method, Body: []byte(j.Body), Timeout: time.Duration(j.TimeoutSeconds) * time.Second,
		ExpectedStatus: j.ExpectedStatus,
	}
	if p := j.Probe; p != nil && j.Type == wire.TypeTCP {
		req.Send, req.ExpectMode, req.ExpectValue = p.Send, p.ExpectMode, p.ExpectValue
		req.ReadTimeout, req.MaxResponse = time.Duration(p.ReadTimeoutSeconds)*time.Second, p.MaxResponseBytes
	}
	if len(j.Headers) > 0 {
		req.Header = make(http.Header, len(j.Headers))
		for _, h := range j.Headers {
			req.Header.Add(h.Name, h.Value)
		}
	}
	return req
}
