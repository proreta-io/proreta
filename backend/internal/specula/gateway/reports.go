package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proreta-io/proreta/backend/internal/checkresult"
	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// maxReportsBody bounds one reports call: 100 reports are about 30 KB.
const maxReportsBody = 256 << 10

// lateAlertAfter is how old a slot is, when it is received, for its alerts to say they are late.
const lateAlertAfter = 10 * time.Minute

func (g *Handler) reports(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReportsBody)
	var in wire.ReportsRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			problem(w, r, http.StatusRequestEntityTooLarge, "payload-too-large", "Request body too large")
			return
		}
		problem(w, r, http.StatusBadRequest, "bad-request", "Invalid JSON body")
		return
	}
	switch n := len(in.Results); {
	case n == 0:
		problem(w, r, http.StatusBadRequest, "bad-request", fmt.Sprintf("send between 1 and %d results", wire.MaxResults))
		return
	case n > wire.MaxResults:
		problem(w, r, http.StatusRequestEntityTooLarge, "payload-too-large", fmt.Sprintf("send at most %d results", wire.MaxResults))
		return
	}
	id := identity(r)
	ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
	defer cancel()
	now := g.d.Clock.Now().UTC()
	outcomes := make([]wire.ReportOutcome, len(in.Results))
	err := db.WithTenantTx(ctx, g.d.Pool, id.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+lockTimeout+"'"); err != nil {
			return err
		}
		epochs, err := loadEpochs(ctx, tx, id, in.Results)
		if err != nil {
			return err
		}
		for i, x := range in.Results {
			outcomes[i] = wire.ReportOutcome{MonitorID: x.MonitorID, Slot: x.Slot}
			outcomes[i].Outcome, err = g.recordReport(ctx, tx, id, x, epochs, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available: nothing was written, resend
			w.Header().Set("Retry-After", retryAfterSec)
			problem(w, r, http.StatusServiceUnavailable, "busy", "Busy, retry")
			return
		}
		g.fail(w, r, "reports", err)
		return
	}
	for _, o := range outcomes {
		g.metrics.results.WithLabelValues(o.Outcome).Inc()
	}
	out := wire.ReportsResponse{Outcomes: outcomes}
	if g.d.ScheduleVersion != nil {
		if out.ScheduleVersion, err = g.d.ScheduleVersion(r.Context(), id); err != nil {
			g.fail(w, r, "reports", err)
			return
		}
	}
	write(w, http.StatusOK, out)
}

// recordReport decides one report and stores it in its own savepoint. A returned error fails the
// whole batch (nothing is kept); every per-report refusal is an outcome.
func (g *Handler) recordReport(ctx context.Context, tx pgx.Tx, id specula.Identity, x wire.Report, epochs map[uuid.UUID]map[int]epoch, now time.Time) (string, error) {
	if !validReport(x) {
		return wire.OutcomeRejected, nil
	}
	byEpoch, known := epochs[x.MonitorID]
	if !known {
		return wire.OutcomeRejected, nil // not this Specula's monitor: no hint of whether it exists elsewhere
	}
	e, ok := byEpoch[x.Epoch]
	if !ok {
		return wire.OutcomeSuperseded, nil
	}
	if o := slotOutcome(e, x.Slot, now); o != wire.OutcomeRecorded {
		return o, nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	res := checks.Result{
		OK: x.OK, StatusCode: x.StatusCode, LatencyMs: x.LatencyMs, ErrorKind: x.ErrorKind,
		TLSNotAfter: x.TLSNotAfter, CheckedAt: time.Unix(x.Slot, 0).UTC(),
	}
	permanent, err := g.d.Recorder.WriteSlot(ctx, sp, id.TenantID, x.MonitorID, id.LocationID, res, now.Sub(res.CheckedAt) > lateAlertAfter)
	switch {
	case errors.Is(err, checkresult.ErrDuplicate):
		return wire.OutcomeDuplicate, sp.Rollback(ctx)
	case err != nil:
		_ = sp.Rollback(ctx)
		if !permanentItemError(err) {
			return "", err
		}
		g.d.Log.ErrorContext(ctx, "specula report rejected: the database refused it", "monitor_id", x.MonitorID, "error", err)
		return wire.OutcomeRejected, nil
	}
	if permanent != "" {
		g.d.Log.ErrorContext(ctx, "applying a Specula report failed permanently; result kept",
			"monitor_id", x.MonitorID, "error_kind", permanent)
	}
	return wire.OutcomeRecorded, sp.Commit(ctx)
}

func validReport(x wire.Report) bool {
	switch {
	case x.MonitorID == uuid.Nil || x.Epoch < 1:
		return false
	case x.OK != (x.ErrorKind == ""):
		return false
	case x.ErrorKind != "" && !knownKinds[x.ErrorKind]:
		return false
	case x.StatusCode != 0 && (x.StatusCode < 100 || x.StatusCode > 599):
		return false
	case x.LatencyMs < 0 || x.LatencyMs > maxLatencyMs:
		return false
	}
	return true
}

// loadEpochs reads every epoch of the batch's monitors at the token's own location and tenant
// (the tenant by row level security, the location by the query). A monitor with no row, deleted or
// not placed here, is absent from the map.
func loadEpochs(ctx context.Context, tx pgx.Tx, id specula.Identity, reports []wire.Report) (map[uuid.UUID]map[int]epoch, error) {
	ids := make([]uuid.UUID, 0, len(reports))
	for _, x := range reports {
		ids = append(ids, x.MonitorID)
	}
	rows, err := tx.Query(ctx, `
		SELECT e.monitor_id, e.epoch, e.anchor, e.interval_seconds, e.active_from, e.active_until
		FROM monitor_schedule_epochs AS e
		JOIN monitors AS m ON m.tenant_id = e.tenant_id AND m.id = e.monitor_id AND m.deleted_at IS NULL
		WHERE e.tenant_id = $1 AND e.location_id = $2 AND e.monitor_id = ANY($3)`, id.TenantID, id.LocationID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]map[int]epoch{}
	for rows.Next() {
		var mon uuid.UUID
		var e epoch
		var interval int32
		if err := rows.Scan(&mon, &e.Number, &e.Anchor, &interval, &e.From, &e.Until); err != nil {
			return nil, err
		}
		e.Interval = int64(interval)
		if out[mon] == nil {
			out[mon] = map[int]epoch{}
		}
		out[mon][e.Number] = e
	}
	return out, rows.Err()
}
