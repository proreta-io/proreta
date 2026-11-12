// Package checkresult records the outcome of one claimed check: the result row, the completed
// lease and the incident state machine. The worker and the Specula gateway both use it, so a
// check run by either ends in exactly the same rows.
package checkresult

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/incidents/machine"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/results"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
)

// Recorder writes outcomes.
type Recorder struct {
	Scheduler *scheduler.Service
	Incidents *incidents.Service
	Clock     clock.Clock
	// Jitter spreads next runs around the interval.
	Jitter func() time.Duration
}

// Write records one outcome in tx, a tenant transaction of lease.TenantID, and reports false
// when the lease was stale (the caller must roll the savepoint back). The result row is inserted
// before the lease check so that this transaction locks the monitor row (foreign key) before its
// schedule row, the same order as pause, resume and delete in the api: the opposite order could
// deadlock with a resume.
//
// permanent is non-empty when the state machine failed for a reason that a retry would not fix;
// the result and the completed lease are then kept and ok is still true.
func (r Recorder) Write(ctx context.Context, tx pgx.Tx, lease scheduler.Lease, interval time.Duration, res checks.Result) (ok bool, permanent string, err error) {
	if err := results.Insert(ctx, tx, results.Row{
		TenantID: lease.TenantID, MonitorID: lease.MonitorID, LocationID: lease.LocationID, CheckedAt: res.CheckedAt,
		OK: res.OK, StatusCode: res.StatusCode, LatencyMs: res.LatencyMs, ErrorKind: res.ErrorKind, TLSNotAfter: res.TLSNotAfter,
	}); err != nil {
		return false, "", err
	}
	ok, err = r.Scheduler.Complete(ctx, tx, lease, res.CheckedAt.Add(interval).Add(r.Jitter()))
	if err != nil || !ok {
		return false, "", err
	}
	// The state machine runs in a nested savepoint: a permanent error undoes only its part, the
	// result and the completed lease are kept, so the job is not repeated just to fail again.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, "", err
	}
	_, err = r.Incidents.ApplyResult(ctx, sp, incidents.ResultInput{
		TenantID: lease.TenantID, MonitorID: lease.MonitorID, LocationID: lease.LocationID, Now: r.Clock.Now(),
		Result: machine.Result{OK: res.OK, ErrorKind: res.ErrorKind, TLSNotAfter: res.TLSNotAfter, CheckedAt: res.CheckedAt},
	})
	if err == nil {
		return true, "", sp.Commit(ctx)
	}
	kind := PermanentKind(err)
	if kind == "" {
		return false, "", err
	}
	if err := sp.Rollback(ctx); err != nil {
		return false, "", err
	}
	return true, kind, nil
}

// PermanentKind names a permanent ApplyResult error, or returns "" for any other error.
func PermanentKind(err error) string {
	switch {
	case errors.Is(err, incidents.ErrMonitorNotFound):
		return "monitor_not_found"
	case errors.Is(err, incidents.ErrInconsistentState):
		return "inconsistent_state"
	}
	return ""
}
