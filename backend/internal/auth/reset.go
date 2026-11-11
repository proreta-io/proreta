package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/auth/authdb"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Audit actions of the password reset. Metadata never holds an address or a token.
const (
	ActionPasswordResetRequested = "auth.password_reset_requested"
	ActionPasswordResetCompleted = "auth.password_reset_completed"
)

// Password reset policy.
const (
	// resetTTL is how long an emailed link works.
	resetTTL = time.Hour
	// maxResetsPerHour caps the links issued for one account in an hour; the rest are dropped
	// silently, like every other request that sends nothing.
	maxResetsPerHour = 3
	resetExpiresText = "1 hour"
)

// ErrResetInvalid means the reset token is malformed, unknown, expired, used, replaced by a newer
// link or issued for an account that cannot be reset any more. They all look alike.
var ErrResetInvalid = errors.New("auth: reset link is not valid")

// PasswordResetEnabled reports whether the forgot-password flow exists on this installation: it
// needs email to send the link and the password form to use the new password.
func (s *Service) PasswordResetEnabled() bool {
	return s.mail.Enabled() && !s.passwordLoginDisabled
}

// RequestPasswordReset emails a reset link to the account with that address, if there is one that
// can be reset (active, with a password, not the system admin). It answers the same (nil)
// whether the address has an account or not, so it cannot be used to find out who is
// registered; the only differences are a few milliseconds of database work and the mail itself.
// A malformed address is a validation error, which says nothing about accounts. Every request
// counts against the per-client-IP limit (*RateLimitedError) and, silently, a per-address
// budget and a per-account budget of three links an hour.
func (s *Service) RequestPasswordReset(ctx context.Context, rawEmail string, ip netip.Addr) error {
	if !s.PasswordResetEnabled() {
		return ErrEmailDisabled
	}
	email, msg := normalizeEmail(rawEmail)
	if msg != "" {
		var verr validation.Error
		verr.Add("email", msg)
		return verr.Err()
	}
	if ok, wait := s.resetLimit.Reserve(limiterIPKey(ip)); !ok {
		s.log.WarnContext(ctx, "password reset requests rate limited")
		return &RateLimitedError{RetryAfter: wait}
	}
	if ok, _ := s.resetAddrLimit.Reserve("reset:" + email); !ok {
		s.log.WarnContext(ctx, "password reset requests for one address rate limited")
		return nil
	}
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		u, err := q.GetUserForReset(ctx, email)
		eligible := err == nil && u.Status == userStatusActive && u.HasPassword && !u.IsPlatformAdmin && isEmailAddress(u.Email)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get user: %w", err)
		}
		// The same token work for everybody, so the two paths differ as little as possible.
		token, terr := newToken()
		if terr != nil {
			return terr
		}
		hash := hashToken(token)
		if !eligible {
			return nil
		}
		now := s.clock.Now()
		recent, err := q.CountRecentPasswordResets(ctx, authdb.CountRecentPasswordResetsParams{UserID: u.ID, Since: now.Add(-time.Hour)})
		if err != nil {
			return fmt.Errorf("count resets: %w", err)
		}
		if recent >= maxResetsPerHour {
			s.log.WarnContext(ctx, "password reset requests for one account rate limited", "user_id", u.ID)
			return nil
		}
		expires := now.Add(resetTTL)
		if err := q.SupersedePasswordResets(ctx, authdb.SupersedePasswordResetsParams{UserID: u.ID, Now: &now}); err != nil {
			return fmt.Errorf("supersede resets: %w", err)
		}
		if err := q.InsertPasswordReset(ctx, authdb.InsertPasswordResetParams{TokenHash: hash, UserID: u.ID, Email: u.Email, Now: now, ExpiresAt: expires}); err != nil {
			return fmt.Errorf("insert reset: %w", err)
		}
		if err := s.mail.Enqueue(ctx, tx, mail.Request{
			Kind: mail.KindPasswordReset, To: u.Email, ExpiresAt: expires,
			Params: map[string]string{"token": token, "email": u.Email, "expires": resetExpiresText},
		}); err != nil {
			return err
		}
		return s.auditForUser(ctx, tx, u.ID, nil, ActionPasswordResetRequested, ip, nil)
	})
	if err != nil {
		s.resetLimit.Refund(limiterIPKey(ip))
		return fmt.Errorf("auth: request password reset: %w", err)
	}
	return nil
}

// auditForUser writes an audit row for an action that has no organization of its own (the user is
// not signed in): it goes to the organization the user used last, and is skipped when the user
// has none.
func (s *Service) auditForUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID, actor *uuid.UUID, action string, ip netip.Addr, meta map[string]any) error {
	var tenantID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT auth_login_tenant($1)`, userID).Scan(&tenantID); err != nil {
		return fmt.Errorf("find organization: %w", err)
	}
	if tenantID == nil {
		return nil
	}
	if err := db.SetTenant(ctx, tx, *tenantID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{
		TenantID: *tenantID, ActorUserID: actor, Action: action,
		EntityType: "user", EntityID: userID.String(), IP: ip, Metadata: meta,
	})
}

// ResetPassword sets a new password with the token of an emailed link and signs the account out
// everywhere. The link is checked before the (expensive, memory-bound) hash, the hash shares the
// login's bounded queue (*RateLimitedError when it is full), and the token is consumed in the
// transaction that changes the password, so it works once. It does not sign the user in. Failed
// attempts count against the per-client-IP limit.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string, ip netip.Addr) error {
	if !s.PasswordResetEnabled() {
		return ErrEmailDisabled
	}
	if s.dummy == nil {
		return errors.New("auth: BootstrapAdmin has not run")
	}
	key := limiterIPKey(ip)
	if ok, wait := s.resetLimit.Reserve(key); !ok {
		s.log.WarnContext(ctx, "password reset attempts rate limited")
		return &RateLimitedError{RetryAfter: wait}
	}
	var verr validation.Error
	switch n := utf8.RuneCountInString(newPassword); {
	case n < minPasswordRunes:
		verr.Add("password", fmt.Sprintf("must be at least %d characters", minPasswordRunes))
	case n > maxPasswordRunes:
		verr.Add("password", fmt.Sprintf("must be at most %d characters", maxPasswordRunes))
	}
	if err := verr.Err(); err != nil {
		s.resetLimit.Refund(key)
		return err
	}
	if !validTokenFormat(token) {
		return ErrResetInvalid
	}
	now := s.clock.Now()
	var open authdb.PeekPasswordResetRow
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		open, err = authdb.New(tx).PeekPasswordReset(ctx, authdb.PeekPasswordResetParams{TokenHash: hashToken(token), Now: now})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrResetInvalid
	}
	if err != nil {
		s.resetLimit.Refund(key)
		return fmt.Errorf("auth: look up reset: %w", err)
	}
	if strings.EqualFold(newPassword, open.Email) {
		s.resetLimit.Refund(key)
		verr.Add("password", "must not be the same as the email")
		return verr.Err()
	}
	if err := s.acquireHash(ctx); err != nil {
		s.resetLimit.Refund(key)
		return err
	}
	phc, err := crypto.HashPassword(newPassword, s.dummy.Params)
	<-s.hashSem
	if err != nil {
		s.resetLimit.Refund(key)
		return fmt.Errorf("auth: reset password: hash: %w", err)
	}

	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		row, err := q.ConsumePasswordReset(ctx, authdb.ConsumePasswordResetParams{TokenHash: hashToken(token), Now: &now})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrResetInvalid
		}
		if err != nil {
			return fmt.Errorf("consume reset: %w", err)
		}
		u, err := q.LockUserForReset(ctx, row.UserID)
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		if u.Status != userStatusActive || u.IsPlatformAdmin || !u.HasPassword || u.Email != row.Email {
			return ErrResetInvalid
		}
		// A password the user chose through the emailed link also ends a forced change.
		if err := q.SetUserPassword(ctx, authdb.SetUserPasswordParams{ID: row.UserID, PasswordHash: &phc, ChangedAt: &now}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		// Any other open link of the account dies with this one.
		if err := q.SupersedePasswordResets(ctx, authdb.SupersedePasswordResetsParams{UserID: row.UserID, Now: &now}); err != nil {
			return fmt.Errorf("supersede resets: %w", err)
		}
		// The link went to this mailbox, which proves it is the account's.
		if u.EmailVerifiedAt == nil {
			if _, err := q.MarkEmailVerifiedFor(ctx, authdb.MarkEmailVerifiedForParams{ID: row.UserID, Email: row.Email, Now: &now}); err != nil {
				return fmt.Errorf("mark verified: %w", err)
			}
		}
		revoked, err := revokeUserSessions(ctx, tx, row.UserID)
		if err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		return s.auditForUser(ctx, tx, row.UserID, &row.UserID, ActionPasswordResetCompleted, ip, map[string]any{"sessionsRevoked": revoked})
	})
	if errors.Is(err, ErrResetInvalid) {
		return err
	}
	if err != nil {
		s.resetLimit.Refund(key)
		return fmt.Errorf("auth: reset password: %w", err)
	}
	s.resetLimit.Refund(key)
	return nil
}
