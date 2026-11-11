// Package platformsettings reads the installation-wide settings that the platform admin changes
// in the panel. The value in the database wins; the environment value is the initial one.
package platformsettings

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// TTL is how long a process keeps the value it read. A change made in the panel reaches the
// other replicas within this time; the replica that served the change drops its copy at once.
const TTL = 5 * time.Second

const readTimeout = 3 * time.Second

// Registration answers whether self-service registration is open. A nil Registration is closed.
type Registration struct {
	pool   *db.Pool
	env    bool
	log    *slog.Logger
	now    func() time.Time
	mu     sync.Mutex
	cached *bool
	at     time.Time
	have   bool
}

// NewRegistration returns a Registration. env is PRORETA_REGISTRATION_ENABLED: the value in use
// until the platform admin chooses one, and again when the choice is cleared.
func NewRegistration(pool *db.Pool, env bool, log *slog.Logger) *Registration {
	if log == nil {
		log = slog.Default()
	}
	return &Registration{pool: pool, env: env, log: log, now: time.Now}
}

// Environment is the value of the environment setting.
func (r *Registration) Environment() bool { return r != nil && r.env }

// Enabled reports whether registration is open, cached for TTL. On a database error it keeps the
// last answer for another TTL and, with none, falls back to the environment value.
func (r *Registration) Enabled(ctx context.Context) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.have || r.now().Sub(r.at) >= TTL {
		v, err := r.read(ctx)
		switch {
		case err == nil:
			r.cached, r.at, r.have = v, r.now(), true
		case !r.have:
			if ctx.Err() == nil {
				r.log.WarnContext(ctx, "reading the registration setting failed", "error", err)
			}
			return r.env
		default:
			r.at = r.now()
		}
	}
	if r.cached != nil {
		return *r.cached
	}
	return r.env
}

// Invalidate makes the next Enabled call read the database.
func (r *Registration) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.have = false
	r.mu.Unlock()
}

func (r *Registration) read(ctx context.Context) (*bool, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var v *bool
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT platform_registration_override()`).Scan(&v)
	})
	return v, err
}
