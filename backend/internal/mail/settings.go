package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// Features says which emails the installation sends. It is read from email_settings, which the
// platform admin edits in the admin panel; everything is off until the admin has configured a
// server, tested it and turned a switch on.
type Features struct {
	// Enabled means a server is configured and its current settings passed a test message. Only
	// then does any flow queue mail.
	Enabled bool
	// Invitations makes "send by email" the default of the invite dialog; an invitation can be
	// emailed whenever Enabled.
	Invitations bool
	// Verification sends the address verification message at registration.
	Verification bool
	// PasswordReset offers the forgot-password flow.
	PasswordReset bool
}

// FeatureSource tells what is enabled right now.
type FeatureSource interface {
	Features(ctx context.Context) Features
}

// FeaturesTTL is how long a process keeps the features it read. A change made in the admin panel
// reaches the other api replicas within this time; the replica that served the change forgets at
// once.
const FeaturesTTL = 5 * time.Second

// readTimeout bounds every settings read.
const readTimeout = 3 * time.Second

// passwordAAD binds the sealed SMTP password to its purpose.
var passwordAAD = []byte("email_settings:password")

// Settings reads the email settings. The api uses Features; jobs uses Connection and RecordTest;
// the admin panel (package admin) seals passwords and invalidates the cache with it.
type Settings struct {
	pool *db.Pool
	keys *crypto.Keyring
	log  *slog.Logger
	now  func() time.Time

	mu       sync.Mutex
	cached   Features
	cachedAt time.Time
	have     bool
}

// NewSettings returns a Settings. log may be nil.
func NewSettings(pool *db.Pool, keys *crypto.Keyring, log *slog.Logger) *Settings {
	if log == nil {
		log = slog.Default()
	}
	return &Settings{pool: pool, keys: keys, log: log, now: time.Now}
}

// Features returns what is enabled, cached for FeaturesTTL. On a database error it keeps the last
// answer for another TTL and, with none, answers "everything off": a flow that cannot tell
// whether email works does not send. A nil Settings has everything off.
func (s *Settings) Features(ctx context.Context) Features {
	if s == nil {
		return Features{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have && s.now().Sub(s.cachedAt) < FeaturesTTL {
		return s.cached
	}
	f, err := s.readFeatures(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.WarnContext(ctx, "reading the email settings failed", "error", err)
		}
		if !s.have {
			return Features{}
		}
		s.cachedAt = s.now()
		return s.cached
	}
	s.cached, s.cachedAt, s.have = f, s.now(), true
	return f
}

// Invalidate makes the next Features call read the database.
func (s *Settings) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.have = false
	s.mu.Unlock()
}

func (s *Settings) readFeatures(ctx context.Context) (Features, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var f Features
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT enabled, send_invitations, send_email_verification, allow_password_reset FROM email_settings_features()`).
			Scan(&f.Enabled, &f.Invitations, &f.Verification, &f.PasswordReset)
	})
	if err != nil {
		return Features{}, err
	}
	// The table cannot hold a switch without a tested server; the guard is for a future change.
	if !f.Enabled {
		f = Features{}
	}
	return f, nil
}

// SealPassword encrypts an SMTP password with the installation keyring and returns the sealed
// value with the id of the key used.
func (s *Settings) SealPassword(password string) (sealed []byte, keyID string, err error) {
	sealed, err = s.keys.Encrypt([]byte(password), passwordAAD)
	if err != nil {
		return nil, "", fmt.Errorf("mail: seal password: %w", err)
	}
	return sealed, s.keys.ActiveKeyID(), nil
}

// ConnectionVersion identifies the connection settings a message was sent with; a test result is
// valid only for the version that was tested.
type ConnectionVersion = uuid.UUID

// Connection loads the settings for sending, with the decrypted password. found is false when no
// server is configured.
func (s *Settings) Connection(ctx context.Context) (c Connection, version ConnectionVersion, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var sealed []byte
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `SELECT host, port, tls, from_header, username, password_sealed, connection_version FROM email_settings_for_jobs()`).
			Scan(&c.Host, &c.Port, &c.TLS, &c.From, &c.Username, &sealed, &version)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		found = e == nil
		return e
	})
	if err != nil || !found {
		return Connection{}, uuid.Nil, false, err
	}
	if err := c.Validate(""); err != nil {
		return Connection{}, uuid.Nil, false, errors.New("mail: stored settings are not valid")
	}
	if len(sealed) > 0 {
		pw, err := s.keys.Decrypt(sealed, passwordAAD)
		if err != nil {
			return Connection{}, uuid.Nil, false, errors.New("mail: stored password cannot be decrypted")
		}
		c.Password = config.Secret(pw)
	}
	return c, version, true, nil
}

// RecordTest stores the result of a test message sent with the connection version: nil sendErr is
// a success. It reports false when the settings changed meanwhile and the result was dropped.
func (s *Settings) RecordTest(ctx context.Context, version ConnectionVersion, sendErr error) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	kind, code := "", 0
	if sendErr != nil {
		kind = KindOther
		var se *SendError
		if errors.As(sendErr, &se) {
			kind, code = se.Kind, se.Code
		}
	}
	var recorded bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT email_settings_record_test($1, $2, $3, $4)`, version, sendErr == nil, kind, code).Scan(&recorded)
	})
	return recorded, err
}

// FixedFeatures is a FeatureSource that always answers the same; for tests and tools.
type FixedFeatures Features

// Features implements FeatureSource.
func (f FixedFeatures) Features(context.Context) Features { return Features(f) }

// AllFeatures has every feature on, for tests.
var AllFeatures = FixedFeatures{Enabled: true, Invitations: true, Verification: true, PasswordReset: true}
