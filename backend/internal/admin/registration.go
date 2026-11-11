package admin

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
)

// ActionRegistrationSettingsUpdated is the audit action (also a label value of
// proreta_admin_actions_total) of a change to the registration switch.
const ActionRegistrationSettingsUpdated = "admin.registration_settings.updated"

// Sources of the effective registration value.
const (
	RegistrationFromDatabase    = "database"
	RegistrationFromEnvironment = "environment"
)

// RegistrationSettings is what the panel shows about self-service registration.
type RegistrationSettings struct {
	// Effective is what the api applies now.
	Effective bool
	// Source is RegistrationFromDatabase when the admin chose a value, else the environment.
	Source string
	// EnvironmentValue is PRORETA_REGISTRATION_ENABLED, used when no choice is stored.
	EnvironmentValue bool
	// UpdatedAt is the last change made in the panel; nil when it never was.
	UpdatedAt *time.Time
}

// Registration returns the current registration setting.
func (s *Service) Registration(ctx context.Context, actor Actor) (RegistrationSettings, error) {
	var out RegistrationSettings
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.readRegistration(ctx, tx, actor)
		return err
	})
	if err != nil {
		return RegistrationSettings{}, mapError("get registration setting", err)
	}
	return out, nil
}

func (s *Service) readRegistration(ctx context.Context, tx pgx.Tx, actor Actor) (RegistrationSettings, error) {
	var choice *bool
	var out RegistrationSettings
	if err := tx.QueryRow(ctx, `SELECT registration_enabled, updated_at FROM admin_registration_get($1)`, actor.UserID).Scan(&choice, &out.UpdatedAt); err != nil {
		return RegistrationSettings{}, err
	}
	out.EnvironmentValue = s.registration.Environment()
	out.Effective, out.Source = out.EnvironmentValue, RegistrationFromEnvironment
	if choice != nil {
		out.Effective, out.Source = *choice, RegistrationFromDatabase
	}
	return out, nil
}

// SetRegistration stores the admin's choice; nil returns to the environment value. It reports the
// settings after the change.
func (s *Service) SetRegistration(ctx context.Context, actor Actor, enabled *bool) (RegistrationSettings, error) {
	var out RegistrationSettings
	var changed bool
	var from, to string
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT from_value, to_value FROM admin_registration_set($1, $2, $3, $4)`, actor.UserID, enabled, uuidv7.New(), actor.ip())
		if err != nil {
			return err
		}
		if rows.Next() {
			changed = true
			if err := rows.Scan(&from, &to); err != nil {
				rows.Close()
				return err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out, err = s.readRegistration(ctx, tx, actor)
		return err
	})
	if err != nil {
		return RegistrationSettings{}, mapError("set registration setting", err)
	}
	if changed {
		s.registration.Invalidate()
		s.recorded(ctx, ActionRegistrationSettingsUpdated, "from", from, "to", to)
	}
	return out, nil
}
