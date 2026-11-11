package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/auth/authdb"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// Audit actions of email verification.
const (
	ActionEmailVerified              = "auth.email_verified"
	ActionEmailVerificationRequested = "auth.email_verification_requested"
)

// Email verification policy.
const (
	// verificationTTL is how long an emailed link works.
	verificationTTL = 24 * time.Hour
	// resendInterval is the least time between two links for one user, and maxVerificationsPerDay
	// the most links one user can have issued in 24 hours.
	resendInterval           = time.Minute
	maxVerificationsPerDay   = 5
	verificationExpiresLabel = "24 hours"
)

// Errors of email verification, mapped to problems in internal/api.
var (
	// ErrVerificationInvalid means the token is malformed, unknown, expired, used, replaced by a
	// newer link, or issued for an address the account no longer has. They all look alike.
	ErrVerificationInvalid = errors.New("auth: verification link is not valid")
	// ErrEmailDisabled means there is nothing to verify or no way to send: email is not
	// configured, or the login is not an email address (the system admin).
	ErrEmailDisabled = errors.New("auth: email is not available")
)

// EmailVerificationEnabled reports whether the platform admin turned address verification on:
// registrations get a verification email and the links work.
func (s *Service) EmailVerificationEnabled(ctx context.Context) bool {
	return s.mail.Features(ctx).Verification
}

// isEmailAddress tells a registered email from a system login such as `admin`: registration
// requires an @, a system login never has one.
func isEmailAddress(login string) bool { return strings.Contains(login, "@") }

// issueVerification replaces the user's open links by a new one and queues the message, in tx.
// It does nothing when email is off or the login is not an address. Only the hash of the token
// is stored; the token itself exists in the sealed mail_outbox payload until the mail is sent.
func (s *Service) issueVerification(ctx context.Context, tx pgx.Tx, q *authdb.Queries, userID uuid.UUID, email string) error {
	if !s.EmailVerificationEnabled(ctx) || !isEmailAddress(email) {
		return nil
	}
	token, err := newToken()
	if err != nil {
		return err
	}
	now := s.clock.Now()
	expires := now.Add(verificationTTL)
	if err := q.SupersedeEmailVerifications(ctx, authdb.SupersedeEmailVerificationsParams{UserID: userID, Now: &now}); err != nil {
		return fmt.Errorf("supersede verifications: %w", err)
	}
	if err := q.InsertEmailVerification(ctx, authdb.InsertEmailVerificationParams{
		TokenHash: hashToken(token), UserID: userID, Email: email, Now: now, ExpiresAt: expires,
	}); err != nil {
		return fmt.Errorf("insert verification: %w", err)
	}
	return s.mail.Enqueue(ctx, tx, mail.Request{
		Kind: mail.KindVerifyEmail, To: email, ExpiresAt: expires,
		Params: map[string]string{"token": token, "email": email, "expires": verificationExpiresLabel},
	})
}

// ResendVerification sends the signed-in user a new verification link. It answers
// ErrEmailDisabled when email is off or the login is not an address, does nothing for an
// address that is already verified, and is limited per user to one link a minute and five a day
// (*RateLimitedError).
func (s *Service) ResendVerification(ctx context.Context, p Principal, ip netip.Addr) error {
	if !s.EmailVerificationEnabled(ctx) {
		return ErrEmailDisabled
	}
	err := db.WithTenantTx(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		u, err := q.LockUserForVerification(ctx, p.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		if u.Status != userStatusActive {
			return ErrUnauthenticated
		}
		// The system admin's login is a credential, not a mailbox, even when it looks like an address.
		if u.IsPlatformAdmin || !isEmailAddress(u.Email) {
			return ErrEmailDisabled
		}
		if u.EmailVerifiedAt != nil {
			return nil
		}
		now := s.clock.Now()
		recent, err := q.CountRecentEmailVerifications(ctx, authdb.CountRecentEmailVerificationsParams{UserID: p.UserID, Since: now.Add(-24 * time.Hour)})
		if err != nil {
			return fmt.Errorf("count verifications: %w", err)
		}
		if wait := verificationWait(now, recent.Issued, recent.Newest); wait > 0 {
			return &RateLimitedError{RetryAfter: wait}
		}
		if err := s.issueVerification(ctx, tx, q, p.UserID, u.Email); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: p.TenantID, ActorUserID: &p.UserID, Action: ActionEmailVerificationRequested,
			EntityType: "user", EntityID: p.UserID.String(), IP: ip,
		})
	})
	var rl *RateLimitedError
	switch {
	case errors.As(err, &rl):
		s.log.WarnContext(ctx, "verification resend rate limited")
		return err
	case err != nil && !errors.Is(err, ErrUnauthenticated) && !errors.Is(err, ErrEmailDisabled):
		return fmt.Errorf("auth: resend verification: %w", err)
	}
	return err
}

// verificationWait is how long the user must wait for another link: until a minute after the
// newest one, or until the oldest of the five in the window ages out (approximated by a full
// minute-spaced day when the daily cap is reached).
func verificationWait(now time.Time, issued int32, newest time.Time) time.Duration {
	if issued == 0 {
		return 0
	}
	if issued >= maxVerificationsPerDay {
		return time.Hour
	}
	if since := now.Sub(newest); since < resendInterval {
		return resendInterval - since
	}
	return 0
}

// VerifyEmail consumes a verification token and marks the address it was issued for as
// verified. It needs no session: the token, sent only to that mailbox, is the proof. Failures
// count against the per-client-IP verification limit (*RateLimitedError). A token works once,
// only for the newest link, only until it expires and only while the account still has that
// address; everything else is ErrVerificationInvalid.
func (s *Service) VerifyEmail(ctx context.Context, token string, ip netip.Addr) error {
	key := limiterIPKey(ip)
	if ok, wait := s.verifyLimit.Reserve(key); !ok {
		s.log.WarnContext(ctx, "email verification attempts rate limited")
		return &RateLimitedError{RetryAfter: wait}
	}
	if !validTokenFormat(token) {
		return ErrVerificationInvalid
	}
	now := s.clock.Now()
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		row, err := q.ConsumeEmailVerification(ctx, authdb.ConsumeEmailVerificationParams{TokenHash: hashToken(token), Now: &now})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVerificationInvalid
		}
		if err != nil {
			return fmt.Errorf("consume verification: %w", err)
		}
		u, err := q.LockUserForVerification(ctx, row.UserID)
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		if u.Status != userStatusActive || u.Email != row.Email {
			return ErrVerificationInvalid
		}
		if u.EmailVerifiedAt == nil {
			if _, err := q.MarkEmailVerifiedFor(ctx, authdb.MarkEmailVerifiedForParams{ID: row.UserID, Email: row.Email, Now: &now}); err != nil {
				return fmt.Errorf("mark verified: %w", err)
			}
		}
		// The user has no tenant here; the row goes to the organization they used last.
		var tenantID *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT auth_login_tenant($1)`, row.UserID).Scan(&tenantID); err != nil {
			return fmt.Errorf("find organization: %w", err)
		}
		if tenantID == nil {
			return nil
		}
		if err := db.SetTenant(ctx, tx, *tenantID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: *tenantID, ActorUserID: &row.UserID, Action: ActionEmailVerified,
			EntityType: "user", EntityID: row.UserID.String(), IP: ip,
		})
	})
	if errors.Is(err, ErrVerificationInvalid) {
		return err
	}
	if err != nil {
		s.verifyLimit.Refund(key)
		return fmt.Errorf("auth: verify email: %w", err)
	}
	s.verifyLimit.Refund(key)
	return nil
}

// MailFeatures says which emails the platform admin turned on.
func (s *Service) MailFeatures(ctx context.Context) mail.Features { return s.mail.Features(ctx) }
