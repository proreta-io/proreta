package admin

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
)

// Audit actions of granting and revoking the platform-admin flag (also label values of
// proreta_admin_actions_total).
const (
	ActionPlatformAdminGranted = "admin.user.platform_admin_granted"
	ActionPlatformAdminRevoked = "admin.user.platform_admin_revoked"
)

// SetPlatformAdmin grants or revokes the platform-admin flag of a user and returns the user
// afterwards. Only an active user can be granted; the system admin (ErrSystemAdminRevoke) and the
// last active platform admin (ErrLastPlatformAdmin) cannot be revoked. Granting an admin or
// revoking a non-admin changes nothing and writes no audit row. The user's sessions stay: the
// flag is read on every request.
func (s *Service) SetPlatformAdmin(ctx context.Context, actor Actor, id uuid.UUID, enabled bool) (AccountDetail, error) {
	var d AccountDetail
	var changed bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT admin_set_platform_admin($1, $2, $3, $4, $5)`,
			actor.UserID, id, enabled, uuidv7.New(), actor.ip()).Scan(&changed); err != nil {
			return err
		}
		var err error
		d, err = getAccount(ctx, tx, actor, id)
		return err
	})
	if err != nil {
		err = mapError("set platform admin", err)
		if errors.Is(err, ErrSystemAdmin) {
			return AccountDetail{}, ErrSystemAdminRevoke
		}
		return AccountDetail{}, err
	}
	if changed && enabled {
		s.recorded(ctx, ActionPlatformAdminGranted, "target_user_id", id)
	} else if changed {
		s.recorded(ctx, ActionPlatformAdminRevoked, "target_user_id", id)
	}
	return d, nil
}
