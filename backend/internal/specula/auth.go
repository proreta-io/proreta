package specula

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// Identity is what a valid token stands for. The gateway scopes everything it does to it.
type Identity struct {
	SpeculaID    uuid.UUID
	TenantID     uuid.UUID
	LocationID   uuid.UUID
	LocationCode string
}

// Authenticate resolves a token through the specula_authenticate definer function, which also
// marks the Specula as seen. It runs with the worker's pool: there is no tenant yet.
func Authenticate(ctx context.Context, pool *db.Pool, token string) (Identity, error) {
	if len(token) < len(TokenPrefix) || token[:len(TokenPrefix)] != TokenPrefix {
		return Identity{}, ErrUnauthorized
	}
	var id Identity
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT specula_id, tenant_id, location_id, location_code FROM specula_authenticate($1)`, HashToken(token)).
			Scan(&id.SpeculaID, &id.TenantID, &id.LocationID, &id.LocationCode)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, ErrUnauthorized
	}
	if err != nil {
		return Identity{}, fmt.Errorf("specula: authenticate: %w", err)
	}
	return id, nil
}

// Hello records the version and host name a Specula reports about itself.
func Hello(ctx context.Context, pool *db.Pool, id uuid.UUID, version, hostname string) error {
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT specula_hello($1, $2, $3)`, id, version, hostname).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrUnauthorized
		}
		return nil
	})
	if errors.Is(err, ErrUnauthorized) {
		return err
	}
	if err != nil {
		return fmt.Errorf("specula: hello: %w", err)
	}
	return nil
}
