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

// linkCandidate is what the email-link rule looks at, read under the user's row lock.
type linkCandidate struct {
	Active          bool
	HasIdentity     bool
	IsPlatformAdmin bool
	IsSystemAdmin   bool
	IsAdminLogin    bool
}

// canLinkByEmail is the rule for a provider sign-in whose verified email matches an existing
// account (internal/oauth refuses unverified provider emails, so that is given). Accounts that
// anyone could have registered an address for stay out: the admin logins, and an account that
// already has an identity (it signs in with that one).
func canLinkByEmail(c linkCandidate) bool {
	return c.Active && !c.HasIdentity && !c.IsPlatformAdmin && !c.IsSystemAdmin && !c.IsAdminLogin
}

// linkIdentityByEmail links the provider identity to the existing account with the same email, in
// one transaction: the email counts as verified, every session the account had ends (the sign-in
// creates its own afterwards), and memberships and data stay. The password stays only when the
// address was verified before: otherwise whoever typed the address first set it, and the owner of
// the address has just proved to be someone else, so it is removed together with the open reset
// and verification links. It returns false
// (nothing changed) when the rule says no, also when a concurrent sign-in won the account.
func (s *Service) linkIdentityByEmail(ctx context.Context, in OAuthLoginInput, userID uuid.UUID) (bool, error) {
	id := in.Identity
	linked := false
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		u, err := q.LockUserForLink(ctx, userID)
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		mine, err := q.ListIdentities(ctx, userID)
		if err != nil {
			return fmt.Errorf("list identities: %w", err)
		}
		if !canLinkByEmail(linkCandidate{
			Active: u.Status == userStatusActive, HasIdentity: len(mine) > 0,
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
		if err := q.MarkEmailVerified(ctx, authdb.MarkEmailVerifiedParams{ID: userID, VerifiedAt: &now}); err != nil {
			return fmt.Errorf("mark email verified: %w", err)
		}
		passwordRemoved := false
		if !u.EmailVerified && u.HasPassword {
			if err := q.ClearPassword(ctx, userID); err != nil {
				return fmt.Errorf("clear password: %w", err)
			}
			passwordRemoved = true
		}
		if !u.EmailVerified {
			if err := q.SupersedePasswordResets(ctx, authdb.SupersedePasswordResetsParams{UserID: userID, Now: &now}); err != nil {
				return fmt.Errorf("supersede password resets: %w", err)
			}
			if err := q.SupersedeEmailVerifications(ctx, authdb.SupersedeEmailVerificationsParams{UserID: userID, Now: &now}); err != nil {
				return fmt.Errorf("supersede email verifications: %w", err)
			}
		}
		// Whoever was signed in before may not be the owner of the address.
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
		linked = true
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: t, ActorUserID: &userID, Action: ActionIdentityLinked,
			EntityType: "user", EntityID: userID.String(), IP: in.IP,
			Metadata: map[string]any{"provider": id.Provider, "via": "sign_in", "sessionsRevoked": revoked, "passwordRemoved": passwordRemoved},
		})
	})
	if err != nil {
		return false, fmt.Errorf("link identity by email: %w", err)
	}
	return linked, nil
}
