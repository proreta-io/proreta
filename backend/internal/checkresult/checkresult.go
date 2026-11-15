// Package checkresult records the outcome of one claimed check: the result row, the completed
// lease and the incident state machine. The worker and the Specula gateway both use it, so a
// check run by either ends in exactly the same rows.
package checkresult

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	permanent, err = r.apply(ctx, tx, lease.TenantID, lease.MonitorID, lease.LocationID, r.Clock.Now(), false, res)
	return err == nil, permanent, err
}

// apply runs the state machine in a nested savepoint: a permanent error undoes only its part, the
// result (and a completed lease) are kept, so the job is not repeated just to fail again.
func (r Recorder) apply(ctx context.Context, tx pgx.Tx, tenantID, monitorID, locationID uuid.UUID, now time.Time, late bool, res checks.Result) (string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	_, err = r.Incidents.ApplyResult(ctx, sp, incidents.ResultInput{
		TenantID: tenantID, MonitorID: monitorID, LocationID: locationID, Now: now, Late: late,
		Result: machine.Result{OK: res.OK, ErrorKind: res.ErrorKind, TLSNotAfter: res.TLSNotAfter, CheckedAt: res.CheckedAt},
	})
	if err == nil {
		return "", sp.Commit(ctx)
	}
	kind := PermanentKind(err)
	if kind == "" {
		return "", err
	}
	if err := sp.Rollback(ctx); err != nil {
		return "", err
	}
	return kind, nil
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

// ErrDuplicate means the slot already has a result (unique violation of the primary key). The
// caller's savepoint must be rolled back.
var ErrDuplicate = errors.New("checkresult: slot already recorded")

// rollupLookback is how many closed hours the regular rollup run recomputes; a row in an older
// hour needs a dirty mark.
const rollupLookback = 3 * time.Hour

// WriteSlot records the result of one slot of an autonomous executor in tx, a tenant transaction
// of tenantID: the row is stored at res.CheckedAt (the slot), a row in an hour the rollup no
// longer revisits marks that hour dirty, and the incident state machine runs as for any result.
// There is no lease. late says the slot is a replay (see incidents.ResultInput.Late). The caller has validated the slot against the monitor's epoch. permanent
// has the meaning it has for Write.
func (r Recorder) WriteSlot(ctx context.Context, tx pgx.Tx, tenantID, monitorID, locationID uuid.UUID, res checks.Result, late bool) (permanent string, err error) {
	if err := results.Insert(ctx, tx, results.Row{
		TenantID: tenantID, MonitorID: monitorID, LocationID: locationID, CheckedAt: res.CheckedAt,
		OK: res.OK, StatusCode: res.StatusCode, LatencyMs: res.LatencyMs, ErrorKind: res.ErrorKind, TLSNotAfter: res.TLSNotAfter,
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", ErrDuplicate
		}
		return "", err
	}
	now := r.Clock.Now()
	if res.CheckedAt.Before(now.UTC().Truncate(time.Hour).Add(-rollupLookback)) {
		if _, err := tx.Exec(ctx, `SELECT rollup_mark_dirty($1)`, res.CheckedAt); err != nil {
			return "", err
		}
	}
	return r.apply(ctx, tx, tenantID, monitorID, locationID, now, late, res)
}
