// Package executor is the Specula executor: it runs inside a customer's network, claims due
// checks from the gateway over HTTPS, runs them with the shared prober and reports the results.
// It only makes outbound calls and keeps nothing on disk.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// ErrRejected means the gateway refused the token: it is wrong or was revoked. The executor stops.
var ErrRejected = errors.New("executor: the token was rejected")

// Defaults.
const (
	DefaultConcurrency  = 20
	DefaultPollInterval = 5 * time.Second
	helloInterval       = time.Minute
	flushInterval       = time.Second
	drainTimeout        = 15 * time.Second
	callTimeout         = 20 * time.Second
	// A result older than this is no longer accepted by the gateway.
	maxResultAge = 9 * time.Minute
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
	Now    func() time.Time
}

// Executor runs checks for one Specula.
type Executor struct {
	cfg     Config
	slots   chan struct{}
	running sync.WaitGroup

	mu      sync.Mutex
	pending []wire.Result
}

// New returns an Executor.
func New(cfg Config) *Executor {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: callTimeout}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Executor{cfg: cfg, slots: make(chan struct{}, cfg.Concurrency)}
}

// Run says hello, then claims and runs checks until ctx is cancelled. It then waits for checks in
// flight and reports what it has. It returns ErrRejected when the token stops working.
func (e *Executor) Run(ctx context.Context) error {
	if err := e.helloUntilOK(ctx); err != nil {
		return err
	}
	// Checks and reports outlive the stop signal until the drain ends.
	workCtx, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()

	// The background calls stop when the main loop ends, for a signal or a rejected token alike.
	bgCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	rejected := make(chan error, 1)
	var bg sync.WaitGroup
	bg.Add(2)
	go func() {
		defer bg.Done()
		e.loop(bgCtx, helloInterval, func() error { return e.hello(workCtx) }, rejected)
	}()
	go func() {
		defer bg.Done()
		e.loop(bgCtx, flushInterval, func() error { return e.flush(workCtx) }, rejected)
	}()

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
	bg.Wait()
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	if err := e.flush(flushCtx); err != nil && runErr == nil && !errors.Is(err, ErrRejected) {
		e.cfg.Log.Warn("final report failed; results are dropped", "error", err, "results", e.queued())
	}
	return runErr
}

// loop runs fn every interval until ctx is done; a rejected token ends the executor.
func (e *Executor) loop(ctx context.Context, interval time.Duration, fn func() error, rejected chan<- error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
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
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (e *Executor) hello(ctx context.Context) error {
	var out wire.HelloResponse
	if _, err := e.call(ctx, "/hello", wire.HelloRequest{Version: e.cfg.Version, Hostname: e.cfg.Hostname}, &out); err != nil {
		return err
	}
	if skew := e.cfg.Now().Sub(out.ServerTime); skew > 30*time.Second || skew < -30*time.Second {
		e.cfg.Log.Warn("the clock differs from Proreta's; results may be rejected", "skew", skew.Round(time.Second).String())
	}
	return nil
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
	if !j.LeaseUntil.After(e.cfg.Now()) {
		return
	}
	req := checks.Request{
		URL: j.URL, Method: j.Method, Body: []byte(j.Body), Timeout: time.Duration(j.TimeoutSeconds) * time.Second,
		ExpectedStatus: j.ExpectedStatus,
	}
	if len(j.Headers) > 0 {
		req.Header = make(http.Header, len(j.Headers))
		for _, h := range j.Headers {
			req.Header.Add(h.Name, h.Value)
		}
	}
	res := e.cfg.Prober.Probe(ctx, req)
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
	cutoff := e.cfg.Now().Add(-maxResultAge)
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

// call posts in as JSON and decodes a 200 answer into out. It returns the HTTP status (0 when no
// answer came). A 401 is ErrRejected.
func (e *Executor) call(ctx context.Context, path string, in, out any) (int, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.cfg.BaseURL, "/")+wire.BasePath+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.Token)
	req.Header.Set("User-Agent", "proreta-specula/"+e.cfg.Version)
	resp, err := e.cfg.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp.StatusCode, ErrRejected
	case resp.StatusCode != http.StatusOK:
		return resp.StatusCode, fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("%s: unreadable answer", path)
	}
	return resp.StatusCode, nil
}
