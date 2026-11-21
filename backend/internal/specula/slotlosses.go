package specula

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// LossTotal is the count of one kind and reason over a range.
type LossTotal struct {
	Kind, Reason string
	Count        int64
}

// LossBucket is what one UTC hour had.
type LossBucket struct {
	Hour            time.Time
	Missed, Dropped int64
}

// SlotLosses returns what a Specula reported as missed or dropped since from (cut to the hour):
// totals ordered as wire.LossReasons, and the hours that had any, oldest first. The caller has
// already checked that the Specula is visible to the user.
func (s *Service) SlotLosses(ctx context.Context, tenantID, id uuid.UUID, from time.Time) ([]LossTotal, []LossBucket, error) {
	var totals []LossTotal
	var buckets []LossBucket
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT hour, kind, reason, count FROM specula_slot_losses
			WHERE specula_id = $1 AND hour >= date_trunc('hour', $2::timestamptz, 'UTC') ORDER BY hour`, id, from)
		if err != nil {
			return err
		}
		type row struct {
			hour         time.Time
			kind, reason string
			count        int64
		}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			return x, r.Scan(&x.hour, &x.kind, &x.reason, &x.count)
		})
		if err != nil {
			return err
		}
		sums := map[[2]string]int64{}
		for _, x := range got {
			sums[[2]string{x.kind, x.reason}] += x.count
			if n := len(buckets); n == 0 || !buckets[n-1].Hour.Equal(x.hour) {
				buckets = append(buckets, LossBucket{Hour: x.hour.UTC()})
			}
			b := &buckets[len(buckets)-1]
			if x.kind == wire.LossMissed {
				b.Missed += x.count
			} else {
				b.Dropped += x.count
			}
		}
		for _, kind := range []string{wire.LossMissed, wire.LossDropped} {
			for _, reason := range wire.LossReasons[kind] {
				if n := sums[[2]string{kind, reason}]; n > 0 {
					totals = append(totals, LossTotal{Kind: kind, Reason: reason, Count: n})
				}
			}
		}
		return nil
	})
	return totals, buckets, wrap(err)
}
