package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/auth/authdb"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/tenants"
)

// ActionPasswordChanged is audited when a user changes its own password.
const ActionPasswordChanged = "auth.password_changed"

// accountLimiterKey is the per-account limiter key of a password change. It lives in the
// login limiter under a prefix a login (which cannot contain ":") never has.
func accountLimiterKey(userID string) string { return "account:" + userID }

// ChangePasswordInput is a user changing its own password.
type ChangePasswordInput struct {
	Current   string
	New       string
	IP        netip.Addr
	UserAgent string
}

// ChangePassword verifies the current password, stores the new one and replaces every session of
// the user with one new session in the current organization: the other devices are signed out,
// the caller stays signed in with a new token and CSRF token. Policy failures and a wrong
// current password are *validation.Error. A user without a password gets ErrNoPassword.
//
// Verification uses the login's bounded hashing queue and counts against the per-IP limiter
// (shared with logins and registrations) and a per-account one; a wrong current password stays
// counted, a success is refunded. The user row is locked and compared with what was verified, so
// a concurrent reset or change makes the attempt fail instead of overwriting it.
func (s *Service) ChangePassword(ctx context.Context, p Principal, in ChangePasswordInput) (LoginResult, error) {
	if s.dummy == nil {
		return LoginResult{}, errors.New("auth: BootstrapAdmin has not run")
	}
	var cred authdb.GetUserCredentialRow
	err := db.WithTenantTx(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		cred, err = authdb.New(tx).GetUserCredential(ctx, p.UserID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && cred.Status != userStatusActive {
		return LoginResult{}, ErrUnauthenticated
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("auth: change password: get user: %w", err)
	}
	if !s.passwordAllowed(cred.Email) {
		return LoginResult{}, ErrPasswordLoginDisabled
	}
	if cred.PasswordHash == nil {
		return LoginResult{}, ErrNoPassword
	}
	var verr validation.Error
	if msg := passwordPolicy(in.New, cred.Email, true); msg != "" {
		verr.Add("newPassword", msg)
	} else if in.New == in.Current {
		verr.Add("newPassword", "must differ from the current password")
	}
	if err := verr.Err(); err != nil {
		s.passwordChanges.WithLabelValues("invalid").Inc()
		return LoginResult{}, err
	}

	ipKey := limiterIPKey(in.IP)
	accountKey := accountLimiterKey(p.UserID.String())
	if ok, wait := s.ipLimit.Reserve(ipKey); !ok {
		return LoginResult{}, s.passwordRateLimited(ctx, wait)
	}
	if ok, wait := s.loginLimit.Reserve(accountKey); !ok {
		s.ipLimit.Refund(ipKey)
		return LoginResult{}, s.passwordRateLimited(ctx, wait)
	}
	refund := func() {
		s.ipLimit.Refund(ipKey)
		s.loginLimit.Refund(accountKey)
	}

	current, perr := crypto.ParseArgon2idHash(*cred.PasswordHash)
	if perr != nil {
		refund()
		s.log.ErrorContext(ctx, "stored password hash is invalid", "user_id", p.UserID)
		return LoginResult{}, fmt.Errorf("auth: change password: stored hash: %w", perr)
	}
	// One slot of the queue for both operations, so a change never holds more memory than a login.
	if err := s.acquireHash(ctx); err != nil {
		refund()
		return LoginResult{}, s.passwordRateLimitedErr(ctx, err)
	}
	match := current.Verify(in.Current)
	var phc string
	if match {
		phc, err = crypto.HashPassword(in.New, s.dummy.Params)
	}
	<-s.hashSem
	if err != nil {
		refund()
		return LoginResult{}, fmt.Errorf("auth: change password: hash: %w", err)
	}
	if !match {
		return LoginResult{}, s.wrongCurrentPassword(ctx, p)
	}

	var res LoginResult
	err = db.WithTenantTx(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		q := authdb.New(tx)
		now := s.clock.Now()
		if _, err := q.LockLiveSession(ctx, authdb.LockLiveSessionParams{ID: p.SessionID, Now: now}); errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		} else if err != nil {
			return fmt.Errorf("lock session: %w", err)
		}
		u, err := q.LockUserForPasswordChange(ctx, p.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		if u.Status != userStatusActive {
			return ErrUnauthenticated
		}
		if u.PasswordHash == nil || *u.PasswordHash != *cred.PasswordHash {
			return errCredentialChanged
		}
		if err := q.SetUserPassword(ctx, authdb.SetUserPasswordParams{ID: p.UserID, PasswordHash: &phc, ChangedAt: &now}); err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		n, err := revokeUserSessions(ctx, tx, p.UserID)
		if err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		role, err := q.GetMembershipRole(ctx, authdb.GetMembershipRoleParams{TenantID: p.TenantID, UserID: p.UserID})
		if err != nil {
			return fmt.Errorf("get membership: %w", err)
		}
		t, err := tenants.Get(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		sess, err := s.insertSession(ctx, q, p.TenantID, p.UserID, in.IP, in.UserAgent)
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			TenantID: p.TenantID, ActorUserID: &p.UserID, Action: ActionPasswordChanged,
			EntityType: "user", EntityID: p.UserID.String(), IP: in.IP,
			Metadata: map[string]any{"sessionsRevoked": n},
		}); err != nil {
			return err
		}
		res = LoginResult{Token: sess.token, SessionInfo: SessionInfo{
			User:   User{ID: p.UserID, Login: u.Email, DisplayName: cred.DisplayName, IsPlatformAdmin: cred.IsPlatformAdmin, IsSystemAdmin: cred.IsSystemAdmin},
			Tenant: t, Role: role, CSRFToken: sess.csrf, ExpiresAt: sess.expiresAt,
		}}
		return nil
	})
	switch {
	case errors.Is(err, errCredentialChanged):
		return LoginResult{}, s.wrongCurrentPassword(ctx, p)
	case err != nil:
		refund()
		if errors.Is(err, ErrUnauthenticated) {
			return LoginResult{}, err
		}
		return LoginResult{}, fmt.Errorf("auth: change password: %w", err)
	}
	s.ipLimit.Refund(ipKey)
	s.loginLimit.Reset(accountKey)
	s.passwordChanges.WithLabelValues("succeeded").Inc()
	s.log.InfoContext(ctx, "password changed", "user_id", p.UserID, "tenant_id", p.TenantID)
	return res, nil
}

// wrongCurrentPassword is the answer to a failed verification; the attempt stays counted.
func (s *Service) wrongCurrentPassword(ctx context.Context, p Principal) error {
	s.passwordChanges.WithLabelValues("wrong_password").Inc()
	s.log.WarnContext(ctx, "password change with a wrong current password", "user_id", p.UserID)
	var verr validation.Error
	verr.Add("currentPassword", "is not correct")
	return verr.Err()
}

func (s *Service) passwordRateLimited(ctx context.Context, wait time.Duration) error {
	return s.passwordRateLimitedErr(ctx, &RateLimitedError{RetryAfter: wait})
}

func (s *Service) passwordRateLimitedErr(ctx context.Context, err error) error {
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		s.passwordChanges.WithLabelValues("rate_limited").Inc()
		// Not audited, like the login limit.
		s.log.WarnContext(ctx, "password change rate limited")
	}
	return err
}
