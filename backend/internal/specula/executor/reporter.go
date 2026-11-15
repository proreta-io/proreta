package executor

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

const (
	// Successes are sent in batches this often; a failure is sent at once.
	successEvery = 10 * time.Second
	// failureGap is the shortest time between two sends caused by failures.
	failureGap = time.Second
	// replayGap between two batches of a backlog keeps the rate at 5 batches (500 results) per second.
	replayGap = 200 * time.Millisecond
	// badAnswerLimit: a batch the gateway answers with an unusable body this often is dropped.
	badAnswerLimit = 3
)

// reporter sends the buffer to the gateway, oldest first, one batch at a time. Every outcome of
// a 200 answer is final, so the batch is then removed. A failure of the whole call leaves it in
// place and the same batch is tried again; the gateway answers duplicate for what it already stored.
type reporter struct {
	e    *Executor
	kick chan struct{}

	mu       sync.Mutex
	outcomes map[string]int64 // by outcome, as answered
	sent     int64            // results the gateway answered for
	retries  int64            // calls that failed as a whole
	dropped  int64            // results given up on (bad requests, unusable answers)
	stopped  bool             // the gateway has no /reports: an older server
}

func newReporter(e *Executor) *reporter {
	return &reporter{e: e, kick: make(chan struct{}, 1), outcomes: map[string]int64{}}
}

// poke asks for a send soon: a failure was recorded, or the link is back.
func (r *reporter) poke() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// ReporterStats is what the metrics need.
type ReporterStats struct {
	Outcomes map[string]int64
	Sent     int64
	Retries  int64
	Dropped  int64
}

func (r *reporter) Stats() ReporterStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := make(map[string]int64, len(r.outcomes))
	for k, v := range r.outcomes {
		o[k] = v
	}
	return ReporterStats{Outcomes: o, Sent: r.sent, Retries: r.retries, Dropped: r.dropped}
}

type sendResult int

const (
	sentOK     sendResult = iota // answered; the batch is done
	sendRetry                    // the call failed as a whole; the same batch later
	sendShrink                   // refused for its size or content; smaller batches
	sendStop                     // no /reports here
)

// loop sends on a timer and on pokes until ctx ends or the token is rejected.
func (r *reporter) loop(ctx context.Context, rejected chan<- error) {
	e := r.e
	var last time.Time
	wait := successEvery
	for {
		poked := false
		select {
		case <-ctx.Done():
			return
		case <-e.cfg.Clock.After(wait):
		case <-r.kick:
			poked = true
		}
		if poked { // coalesce failures that follow each other
			if d := failureGap - e.cfg.Clock.Now().Sub(last); d > 0 {
				select {
				case <-ctx.Done():
					return
				case <-e.cfg.Clock.After(d):
				}
			}
		}
		err := r.drain(ctx, true)
		last = e.cfg.Clock.Now()
		if errors.Is(err, ErrRejected) {
			select {
			case rejected <- err:
			default:
			}
			return
		}
		if errors.Is(err, errNoReports) {
			e.cfg.Log.Error("the gateway does not accept reports; results stay in the buffer")
			r.mu.Lock()
			r.stopped = true
			r.mu.Unlock()
			return
		}
		wait = successEvery
	}
}

var errNoReports = errors.New("executor: the gateway has no /reports")

// drain sends until the buffer is empty. With retry it waits and tries again when the gateway is
// unreachable or busy (the buffer's own limits bound how long that can go on); without, the
// first failure ends it, which is what the shutdown wants.
func (r *reporter) drain(ctx context.Context, retry bool) error {
	e := r.e
	size, fails, badAnswers := wire.MaxResults, 0, 0
	for ctx.Err() == nil {
		e.buf.Enforce()
		batch := e.buf.Peek(size)
		if len(batch.Reports) == 0 {
			return nil
		}
		res, wait, err := r.send(ctx, batch, &badAnswers)
		switch res {
		case sentOK:
			e.buf.Ack(batch)
			fails, badAnswers = 0, 0
			if e.buf.Depth() > 0 {
				if err := r.sleep(ctx, replayGap); err != nil {
					return nil
				}
			}
			size = min(size*2, wire.MaxResults)
		case sendShrink:
			if len(batch.Reports) == 1 {
				e.cfg.Log.Error("the gateway refused a result; it is dropped", "monitor_id", batch.Reports[0].MonitorID, "slot", batch.Reports[0].Slot)
				e.buf.Ack(batch)
				r.mu.Lock()
				r.dropped++
				r.mu.Unlock()
				size = wire.MaxResults
				continue
			}
			size = max(1, len(batch.Reports)/2)
		case sendRetry:
			fails++
			r.mu.Lock()
			r.retries++
			r.mu.Unlock()
			if badAnswers >= badAnswerLimit {
				e.cfg.Log.Error("the gateway keeps answering with an unusable body; the batch is dropped", "results", len(batch.Reports))
				e.buf.Ack(batch)
				r.mu.Lock()
				r.dropped += int64(len(batch.Reports))
				r.mu.Unlock()
				fails, badAnswers = 0, 0
				continue
			}
			if !retry {
				return err
			}
			if fails == 1 || fails%10 == 0 {
				e.cfg.Log.Warn("cannot report results; they stay buffered", "error", err, "attempt", fails, "waiting", e.buf.Depth())
			}
			if err := r.sleep(ctx, max(backoff(fails), wait)); err != nil {
				return nil
			}
		case sendStop:
			return err
		}
		if err != nil && (errors.Is(err, ErrRejected) || errors.Is(err, errNoReports)) {
			return err
		}
	}
	return nil
}

func (r *reporter) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.e.cfg.Clock.After(d):
		return nil
	}
}

// send posts one batch. wait is the server's Retry-After.
func (r *reporter) send(ctx context.Context, batch Batch, badAnswers *int) (sendResult, time.Duration, error) {
	var out wire.ReportsResponse
	_, err := r.e.call(ctx, "/reports", wire.ReportsRequest{Results: batch.Reports}, &out)
	if err != nil {
		var se *statusError
		switch {
		case errors.Is(err, ErrRejected):
			return sendStop, 0, err
		case errors.As(err, &se):
			switch se.Status {
			case http.StatusNotFound:
				return sendStop, 0, errNoReports
			case http.StatusRequestEntityTooLarge, http.StatusBadRequest:
				return sendShrink, 0, err
			}
			return sendRetry, se.RetryAfter, err // 429, 503 and the rest: nothing was written
		}
		return sendRetry, 0, err
	}
	if len(out.Outcomes) != len(batch.Reports) {
		*badAnswers++
		return sendRetry, 0, errors.New("/reports: the answer does not match the request")
	}
	r.mu.Lock()
	skipped := map[string]int{}
	for _, o := range out.Outcomes {
		r.outcomes[o.Outcome]++
		if o.Outcome != wire.OutcomeRecorded && o.Outcome != wire.OutcomeDuplicate {
			skipped[o.Outcome]++
		}
	}
	r.sent += int64(len(batch.Reports))
	r.mu.Unlock()
	if len(skipped) > 0 {
		r.e.cfg.Log.Info("the gateway did not record some results; they are final", "outcomes", skipped)
	}
	r.e.sched.notifyVersion(out.ScheduleVersion)
	return sentOK, 0, nil
}
