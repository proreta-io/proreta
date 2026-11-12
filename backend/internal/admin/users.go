package admin

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Audit actions of the user operations (also label values of proreta_admin_actions_total).
const (
	ActionUserDisabled        = "admin.user.disabled"
	ActionUserEnabled         = "admin.user.enabled"
	ActionUserSessionsRevoked = "admin.user.sessions_revoked"
)

// User statuses.
const (
	UserActive   = "active"
	UserDisabled = "disabled"
)

// Account is a user across organizations as the admin sees it.
type Account struct {
	ID                 uuid.UUID
	Login              string
	DisplayName        string
	Status             string
	IsPlatformAdmin    bool
	IsSystemAdmin      bool
	EmailVerified      bool
	MustChangePassword bool
	CreatedAt          time.Time
	OrganizationCount  int
}

// AccountList is one page of accounts and the number of all matches.
type AccountList struct {
	Items []Account
	Total int
}

// Membership is one organization a user belongs to. Status is active, suspended or deleted.
type Membership struct {
	OrganizationID     uuid.UUID
	OrganizationName   string
	OrganizationSlug   string
	OrganizationStatus string
	Role               string
	JoinedAt           time.Time
}

// AccountDetail is an account with its memberships and its open sessions.
type AccountDetail struct {
	Account
	Memberships  []Membership
	OpenSessions int
}

const accountColumns = `id, login, display_name, status, is_platform_admin, is_system_admin, email_verified, must_change_password, created_at, organization_count`

func (a *Account) dest() []any {
	return []any{&a.ID, &a.Login, &a.DisplayName, &a.Status, &a.IsPlatformAdmin, &a.IsSystemAdmin, &a.EmailVerified, &a.MustChangePassword, &a.CreatedAt, &a.OrganizationCount}
}

// ListAccounts returns a page of all users, newest first. Query matches the email or the display
// name; status (empty for any) keeps only active or disabled users.
func (s *Service) ListAccounts(ctx context.Context, actor Actor, page Page, status string) (AccountList, error) {
	page = page.normalized()
	if status != "" && status != UserActive && status != UserDisabled {
		var verr validation.Error
		verr.Add("status", "must be active or disabled")
		return AccountList{}, verr.Err()
	}
	list := AccountList{Items: []Account{}}
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		list.Items, list.Total = []Account{}, 0
		rows, err := tx.Query(ctx, `SELECT `+accountColumns+`, total_count FROM admin_list_all_users($1, $2, $3, $4, $5)`,
			actor.UserID, page.Query, status, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Account
			if err := rows.Scan(append(a.dest(), &list.Total)...); err != nil {
				return err
			}
			list.Items = append(list.Items, a)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(list.Items) == 0 && page.Offset > 0 {
			return tx.QueryRow(ctx, `SELECT COALESCE(max(total_count), 0) FROM admin_list_all_users($1, $2, $3, 1, 0)`,
				actor.UserID, page.Query, status).Scan(&list.Total)
		}
		return nil
	})
	if err != nil {
		return AccountList{}, mapError("list all users", err)
	}
	return list, nil
}

// GetAccount returns one user with every membership and the number of open sessions.
func (s *Service) GetAccount(ctx context.Context, actor Actor, id uuid.UUID) (AccountDetail, error) {
	var d AccountDetail
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = getAccount(ctx, tx, actor, id)
		return err
	})
	if err != nil {
		return AccountDetail{}, mapError("get user", err)
	}
	return d, nil
}

func getAccount(ctx context.Context, tx pgx.Tx, actor Actor, id uuid.UUID) (AccountDetail, error) {
	d := AccountDetail{Memberships: []Membership{}}
	if err := tx.QueryRow(ctx, `SELECT `+accountColumns+`, open_sessions FROM admin_get_user($1, $2)`, actor.UserID, id).
		Scan(append(d.dest(), &d.OpenSessions)...); err != nil {
		return AccountDetail{}, err
	}
	rows, err := tx.Query(ctx, `SELECT organization_id, organization_name, organization_slug, organization_status, role, joined_at
		FROM admin_list_user_memberships($1, $2)`, actor.UserID, id)
	if err != nil {
		return AccountDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrganizationID, &m.OrganizationName, &m.OrganizationSlug, &m.OrganizationStatus, &m.Role, &m.JoinedAt); err != nil {
			return AccountDetail{}, err
		}
		d.Memberships = append(d.Memberships, m)
	}
	return d, rows.Err()
}

// SetAccountStatus disables or enables a user and returns the user afterwards. Disabling also
// deletes all of the user's sessions; the system admin is refused with ErrSystemAdmin. Setting
// the status the user already has changes nothing and writes no audit row.
func (s *Service) SetAccountStatus(ctx context.Context, actor Actor, id uuid.UUID, status string) (AccountDetail, error) {
	var d AccountDetail
	var changed bool
	var revoked int
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT changed, sessions_revoked FROM admin_set_user_status($1, $2, $3, $4, $5)`,
			actor.UserID, id, status, uuidv7.New(), actor.ip()).Scan(&changed, &revoked); err != nil {
			return err
		}
		var err error
		d, err = getAccount(ctx, tx, actor, id)
		return err
	})
	if err != nil {
		return AccountDetail{}, mapError("set user status", err)
	}
	if changed && status == UserDisabled {
		s.recorded(ctx, ActionUserDisabled, "target_user_id", id, "sessions_revoked", revoked)
	} else if changed {
		s.recorded(ctx, ActionUserEnabled, "target_user_id", id)
	}
	return d, nil
}

// RevokeAccountSessions signs a user out everywhere and returns the number of open sessions
// deleted. An audit row is written only when it is above zero.
func (s *Service) RevokeAccountSessions(ctx context.Context, actor Actor, id uuid.UUID) (int, error) {
	var n int
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT admin_revoke_user_sessions($1, $2, $3, $4)`, actor.UserID, id, uuidv7.New(), actor.ip()).Scan(&n)
	})
	if err != nil {
		return 0, mapError("revoke user sessions", err)
	}
	if n > 0 {
		s.recorded(ctx, ActionUserSessionsRevoked, "target_user_id", id, "sessions_revoked", n)
	}
	return n, nil
}
