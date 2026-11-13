package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/auth/authdb"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// ActionAccountClaimed is written when a provider-verified sign-in takes over an unverified
// password account. Metadata carries the provider and how many sessions ended, never the email.
const ActionAccountClaimed = "auth.account_claimed"

// claimCandidate is what the takeover rule looks at, read under the user's row lock.
type claimCandidate struct {
	Active          bool
	EmailVerified   bool
	HasIdentity     bool
	IsPlatformAdmin bool
	IsSystemAdmin   bool
	IsAdminLogin    bool
}

// canClaim is the takeover rule. The provider's email is verified by construction (internal/oauth
// refuses the rest). Only an account nobody proved the address of, with no other way in than a
// password, can be taken over: it may be a squatter's.
func canClaim(c claimCandidate) bool {
	return c.Active && !c.EmailVerified && !c.HasIdentity && !c.IsPlatformAdmin && !c.IsSystemAdmin && !c.IsAdminLogin
}

// claimAccount lets a provider-verified sign-in take over the unverified, identity-less account
// with the same email: the password is removed, every session ends, the identity is linked and the
// email is marked verified, in one transaction. Memberships stay. It returns false (nothing
// changed) when the rule says no, also when a concurrent sign-in won the account.
func (s *Service) claimAccount(ctx context.Context, in OAuthLoginInput, userID uuid.UUID) (bool, error) {
	id := in.Identity
	claimed := false
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		u, err := q.LockUserForClaim(ctx, userID)
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		mine, err := q.ListIdentities(ctx, userID)
		if err != nil {
			return fmt.Errorf("list identities: %w", err)
		}
		if !canClaim(claimCandidate{
			Active: u.Status == userStatusActive, EmailVerified: u.EmailVerifiedAt != nil, HasIdentity: len(mine) > 0,
			IsPlatformAdmin: u.IsPlatformAdmin, IsSystemAdmin: u.IsSystemAdmin, IsAdminLogin: normalizeLogin(u.Email) == s.adminLogin,
		}) {
			return nil
		}
		now := s.clock.Now()
		if err := q.InsertIdentity(ctx, identityParams(userID, id, now)); err != nil {
			if db.IsUniqueViolation(err, "auth_identities_pkey") {
				return nil // linked to someone else meanwhile: the caller answers account_exists
			}
			return fmt.Errorf("insert identity: %w", err)
		}
		// The squatter's password, and the forced change that went with it, go away.
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = NULL, password_changed_at = NULL, password_must_change = false,
			email_verified_at = $2, updated_at = $2 WHERE id = $1`, userID, now); err != nil {
			return fmt.Errorf("clear password: %w", err)
		}
		// A pending reset link would bring the password back.
		if err := q.SupersedePasswordResets(ctx, authdb.SupersedePasswordResetsParams{UserID: userID, Now: &now}); err != nil {
			return fmt.Errorf("supersede resets: %w", err)
		}
		revoked, err := revokeUserSessions(ctx, tx, userID)
		if err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		var tenantID *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT auth_login_tenant($1)`, userID).Scan(&tenantID); err != nil {
			return fmt.Errorf("find organization: %w", err)
		}
		t := s.defaultTenant
		if tenantID != nil {
			t = *tenantID
		}
		if err := db.SetTenant(ctx, tx, t); err != nil {
			return err
		}
		claimed = true
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: t, ActorUserID: &userID, Action: ActionAccountClaimed,
			EntityType: "user", EntityID: userID.String(), IP: in.IP,
			Metadata: map[string]any{"provider": id.Provider, "sessionsRevoked": revoked},
		})
	})
	if err != nil {
		return false, fmt.Errorf("claim account: %w", err)
	}
	return claimed, nil
}
