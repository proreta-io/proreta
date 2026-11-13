package admin

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platformsettings"
)

// ActionRetentionSettingsUpdated is the audit action (also a label value of
// proreta_admin_actions_total) of a change to the retention settings.
const ActionRetentionSettingsUpdated = "admin.retention_settings.updated"

// RetentionSettings is what the panel shows about how long finished data is kept.
type RetentionSettings struct {
	// DeliveryDays and RollupMonths are what the jobs worker applies now.
	DeliveryDays, RollupMonths int
	// DeliveryDefault and RollupDefault are true while no choice is stored for that value.
	DeliveryDefault, RollupDefault bool
	// UpdatedAt is the last change made in the panel; nil when it never was.
	UpdatedAt *time.Time
}

// Retention returns the current retention settings.
func (s *Service) Retention(ctx context.Context, actor Actor) (RetentionSettings, error) {
	var out RetentionSettings
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = readRetention(ctx, tx, actor)
		return err
	})
	if err != nil {
		return RetentionSettings{}, mapError("get retention settings", err)
	}
	return out, nil
}

func readRetention(ctx context.Context, tx pgx.Tx, actor Actor) (RetentionSettings, error) {
	var d, m *int
	var out RetentionSettings
	if err := tx.QueryRow(ctx, `SELECT delivery_days, rollup_months, updated_at FROM admin_retention_get($1)`, actor.UserID).Scan(&d, &m, &out.UpdatedAt); err != nil {
		return RetentionSettings{}, err
	}
	out.DeliveryDays, out.RollupMonths = platformsettings.EffectiveRetention(d, m)
	out.DeliveryDefault, out.RollupDefault = d == nil, m == nil
	return out, nil
}

// SetRetention stores both choices; nil returns that value to its default. It reports the
// settings after the change.
func (s *Service) SetRetention(ctx context.Context, actor Actor, deliveryDays, rollupMonths *int) (RetentionSettings, error) {
	if err := platformsettings.ValidateRetention(deliveryDays, rollupMonths); err != nil {
		return RetentionSettings{}, err
	}
	var out RetentionSettings
	var changed bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT admin_retention_set($1, $2, $3, $4, $5)`, actor.UserID, deliveryDays, rollupMonths, uuidv7.New(), actor.ip()).Scan(&changed); err != nil {
			return err
		}
		var err error
		out, err = readRetention(ctx, tx, actor)
		return err
	})
	if err != nil {
		return RetentionSettings{}, mapError("set retention settings", err)
	}
	if changed {
		s.recorded(ctx, ActionRetentionSettingsUpdated, "deliveryDays", out.DeliveryDays, "rollupMonths", out.RollupMonths)
	}
	return out, nil
}
