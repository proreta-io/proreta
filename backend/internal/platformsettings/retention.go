package platformsettings

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// How long finished notification deliveries and hourly rollups are kept. The defaults apply until
// the platform admin chooses a value. Rollups are kept at least 4 months because the status page
// shows 90 days, and at most 5 years. The same bounds are enforced in the database.
const (
	DefaultDeliveryDays = 30
	MinDeliveryDays     = 7
	MaxDeliveryDays     = 365

	DefaultRollupMonths = 13
	MinRollupMonths     = 4
	MaxRollupMonths     = 60
)

// ValidateRetention checks the admin's choices; nil means the default.
func ValidateRetention(deliveryDays, rollupMonths *int) error {
	var e validation.Error
	if v := deliveryDays; v != nil && (*v < MinDeliveryDays || *v > MaxDeliveryDays) {
		e.Add("deliveryDays", "must be between 7 and 365 days")
	}
	if v := rollupMonths; v != nil && (*v < MinRollupMonths || *v > MaxRollupMonths) {
		e.Add("rollupMonths", "must be between 4 and 60 months")
	}
	return e.Err()
}

// EffectiveRetention applies the defaults to the stored choices.
func EffectiveRetention(deliveryDays, rollupMonths *int) (days, months int) {
	days, months = DefaultDeliveryDays, DefaultRollupMonths
	if deliveryDays != nil {
		days = *deliveryDays
	}
	if rollupMonths != nil {
		months = *rollupMonths
	}
	return days, months
}

// ReadRetention reads the effective retentions for the jobs worker, on every call: the purge
// tasks run rarely, so there is nothing to cache.
func ReadRetention(ctx context.Context, tx pgx.Tx) (days, months int, err error) {
	var d, m *int
	if err := tx.QueryRow(ctx, `SELECT delivery_days, rollup_months FROM platform_retention_overrides()`).Scan(&d, &m); err != nil {
		return 0, 0, err
	}
	days, months = EffectiveRetention(d, m)
	return days, months, nil
}
