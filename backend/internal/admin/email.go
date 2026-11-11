package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Audit actions of the email settings (also label values of proreta_admin_actions_total).
const (
	ActionEmailSettingsUpdated = "admin.email_settings_updated"
	ActionEmailTestSent        = "admin.email_test_sent"
)

// Errors of the email settings, mapped to HTTP problems in internal/api.
var (
	// ErrEmailNotTested means a switch cannot be turned on: the current connection settings
	// have no successful test message.
	ErrEmailNotTested = errors.New("admin: the email settings have not passed a test")
	// ErrEmailNotConfigured means there is no server to test.
	ErrEmailNotConfigured = errors.New("admin: no email server is configured")
	// ErrEmailTestPending means a test message is still on its way.
	ErrEmailTestPending = errors.New("admin: a test message is already on its way")
)

// emailTestLifetime is how long the queued test message may wait for jobs: past this it is
// dropped instead of arriving late, and the panel reports it as not delivered.
const emailTestLifetime = 2 * time.Minute

// EmailTest is the last test message of the current connection settings.
type EmailTest struct {
	// State is pending, succeeded or failed.
	State       string
	RequestedAt time.Time
	FinishedAt  *time.Time
	// Error is the failure category, never server text.
	Error    *string
	SMTPCode *int
}

// EmailSettings is what the panel shows. The password is never part of it.
type EmailSettings struct {
	Configured            bool
	Host                  string
	Port                  int
	TLS                   string
	From                  string
	Username              string
	PasswordSet           bool
	SendInvitations       bool
	SendEmailVerification bool
	AllowPasswordReset    bool
	Tested                bool
	TestedAt              *time.Time
	LastTest              *EmailTest
	UpdatedAt             time.Time
}

// EmailConnectionInput is a new connection. Password is nil to keep the stored one.
type EmailConnectionInput struct {
	Host     string
	Port     int
	TLS      string
	From     string
	Username string
	Password *string
}

// EmailFeatures are the three switches.
type EmailFeatures struct {
	SendInvitations       bool
	SendEmailVerification bool
	AllowPasswordReset    bool
}

// EmailSettings returns the current settings.
func (s *Service) EmailSettings(ctx context.Context, actor Actor) (EmailSettings, error) {
	var out EmailSettings
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = readEmailSettings(ctx, tx, actor)
		return err
	})
	if err != nil {
		return EmailSettings{}, mapError("get email settings", err)
	}
	return out, nil
}

func readEmailSettings(ctx context.Context, tx pgx.Tx, actor Actor) (EmailSettings, error) {
	var o EmailSettings
	var state string
	var requested, finished *time.Time
	var errKind *string
	var errCode *int32
	err := tx.QueryRow(ctx, `SELECT host, port, tls, from_header, username, password_set, send_invitations, send_email_verification,
			allow_password_reset, tested, tested_at, test_state, test_requested_at, test_finished_at, test_error, test_error_code, updated_at
		FROM admin_email_settings_get($1)`, actor.UserID).Scan(&o.Host, &o.Port, &o.TLS, &o.From, &o.Username, &o.PasswordSet,
		&o.SendInvitations, &o.SendEmailVerification, &o.AllowPasswordReset, &o.Tested, &o.TestedAt, &state, &requested, &finished,
		&errKind, &errCode, &o.UpdatedAt)
	if err != nil {
		return EmailSettings{}, err
	}
	o.Configured = o.Host != ""
	if state != "none" && requested != nil {
		t := &EmailTest{State: state, RequestedAt: *requested, FinishedAt: finished, Error: errKind}
		if errCode != nil {
			c := int(*errCode)
			t.SMTPCode = &c
		}
		o.LastTest = t
	}
	return o, nil
}

// UpdateEmailConnection validates and stores the connection settings. A change forgets the last
// test and turns the switches off.
func (s *Service) UpdateEmailConnection(ctx context.Context, actor Actor, in EmailConnectionInput) (EmailSettings, error) {
	if s.mailSettings == nil {
		return EmailSettings{}, errors.New("admin: email settings are not wired")
	}
	if ok, err := s.auth.IsPlatformAdmin(ctx, actor.UserID); err != nil {
		return EmailSettings{}, err
	} else if !ok {
		return EmailSettings{}, ErrForbidden
	}
	conn := mail.Connection{Host: in.Host, Port: in.Port, TLS: in.TLS, From: in.From, Username: in.Username}
	pw := ""
	if in.Password != nil {
		pw = *in.Password
	}
	if err := conn.Validate(pw); err != nil {
		return EmailSettings{}, err
	}
	var sealed []byte
	var keyID string
	if conn.Username != "" && in.Password != nil {
		if pw == "" {
			return EmailSettings{}, &validation.Error{Fields: []validation.FieldError{{Field: "password", Message: "must not be empty; omit it to keep the stored password"}}}
		}
		var err error
		if sealed, keyID, err = s.mailSettings.SealPassword(pw); err != nil {
			return EmailSettings{}, err
		}
	}
	var out EmailSettings
	var changed []string
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var off bool
		if err := tx.QueryRow(ctx, `SELECT changed, switches_disabled FROM admin_email_settings_update($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			actor.UserID, conn.Host, conn.Port, conn.TLS, conn.From, conn.Username, sealed, keyID, uuidv7.New(), actor.ip()).Scan(&changed, &off); err != nil {
			return err
		}
		var err error
		out, err = readEmailSettings(ctx, tx, actor)
		return err
	})
	if err != nil {
		return EmailSettings{}, mapError("update email settings", err)
	}
	if len(changed) > 0 {
		s.mailSettings.Invalidate()
		s.recorded(ctx, ActionEmailSettingsUpdated, "changed", strings.Join(changed, ","))
	}
	return out, nil
}

// DeleteEmailSettings removes the server, erases the password and turns every switch off.
func (s *Service) DeleteEmailSettings(ctx context.Context, actor Actor) error {
	var changed bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT admin_email_settings_clear($1, $2, $3)`, actor.UserID, uuidv7.New(), actor.ip()).Scan(&changed)
	})
	if err != nil {
		return mapError("clear email settings", err)
	}
	if s.mailSettings != nil {
		s.mailSettings.Invalidate()
	}
	if changed {
		s.recorded(ctx, ActionEmailSettingsUpdated, "changed", "removed")
	}
	return nil
}

// UpdateEmailFeatures sets the switches; turning one on needs a successful test of the current
// settings.
func (s *Service) UpdateEmailFeatures(ctx context.Context, actor Actor, f EmailFeatures) (EmailSettings, error) {
	var out EmailSettings
	var changed bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT admin_email_settings_set_features($1, $2, $3, $4, $5, $6)`,
			actor.UserID, f.SendInvitations, f.SendEmailVerification, f.AllowPasswordReset, uuidv7.New(), actor.ip()).Scan(&changed); err != nil {
			return err
		}
		var err error
		out, err = readEmailSettings(ctx, tx, actor)
		return err
	})
	if err != nil {
		return EmailSettings{}, mapError("set email features", err)
	}
	if changed {
		if s.mailSettings != nil {
			s.mailSettings.Invalidate()
		}
		s.recorded(ctx, ActionEmailSettingsUpdated, "changed", "switches")
	}
	return out, nil
}

// SendTestEmail queues a test message to the address in the transaction that marks the test as
// requested, so a pending test always has a message. Jobs sends it and records the outcome.
func (s *Service) SendTestEmail(ctx context.Context, actor Actor, to string) (EmailSettings, error) {
	if s.mail == nil {
		return EmailSettings{}, errors.New("admin: email queue is not wired")
	}
	if ok, err := s.auth.IsPlatformAdmin(ctx, actor.UserID); err != nil {
		return EmailSettings{}, err
	} else if !ok {
		return EmailSettings{}, ErrForbidden
	}
	to = strings.TrimSpace(to)
	if !mail.ValidRecipient(to) {
		return EmailSettings{}, &validation.Error{Fields: []validation.FieldError{{Field: "to", Message: "must be an email address such as you@example.com"}}}
	}
	_, domain, _ := strings.Cut(to, "@")
	var out EmailSettings
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var version string
		if err := tx.QueryRow(ctx, `SELECT admin_email_test_request($1, $2, $3, $4)::text`, actor.UserID, domain, uuidv7.New(), actor.ip()).Scan(&version); err != nil {
			return err
		}
		if err := s.mail.Enqueue(ctx, tx, mail.Request{Kind: mail.KindTest, To: to, ExpiresAt: time.Now().Add(emailTestLifetime)}); err != nil {
			return err
		}
		var err error
		out, err = readEmailSettings(ctx, tx, actor)
		return err
	})
	if err != nil {
		return EmailSettings{}, mapError("send test email", err)
	}
	s.recorded(ctx, ActionEmailTestSent, "to_domain", domain)
	return out, nil
}
