package specula

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// HelloInput is what a Specula reports about itself. Everything after Hostname is optional and
// replaces what the previous hello said, so the latest hello decides the capabilities.
type HelloInput struct {
	Version, Hostname string
	Capabilities      []string
	InstanceID        string
	ScheduleIssuedAt  *time.Time
	BufferDepth       *int
	OldestUnsentAt    *time.Time
	BufferDegraded    bool
	// ClockOffsetMs is the server's time minus the executor's corrected clock; nil when unmeasured.
	ClockOffsetMs *int64
	// SlotCounters are the cumulative counters of a schedule/2 hello by reason name; nil when the hello had none.
	SlotCounters map[string]int64
}

// Loss is how much a counter grew since the previous hello of the same process.
type Loss struct {
	Kind, Reason string
	Count        int64
}

// Hello records what a Specula reports about itself. duplicate is true when this hello carried a
// different instance id than the one stored, and the stored one said hello less than three
// minutes ago: two executors share the token.
func Hello(ctx context.Context, pool *db.Pool, id uuid.UUID, in HelloInput) (duplicate bool, losses []Loss, err error) {
	var instance *string
	if in.InstanceID != "" {
		instance = &in.InstanceID
	}
	caps := in.Capabilities
	if caps == nil {
		caps = []string{}
	}
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT found, duplicate FROM specula_hello($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, in.Version, in.Hostname, caps, instance, in.ScheduleIssuedAt, in.BufferDepth, in.OldestUnsentAt, in.BufferDegraded, in.ClockOffsetMs).Scan(&ok, &duplicate); err != nil {
			return err
		}
		if !ok {
			return ErrUnauthorized
		}
		// Two executors on one token flip the instance id on every hello and each would add its whole
		// cumulative value again; a hello of a duplicate is not counted, the stored baseline stays empty
		// and the next hello of the process that keeps the id counts what it has.
		if in.SlotCounters == nil || duplicate {
			return nil
		}
		// Same transaction, so the deltas and the hello's row update commit together.
		raw, err := json.Marshal(in.SlotCounters)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT kind, reason, delta FROM specula_record_slot_losses($1, $2)`, id, raw)
		if err != nil {
			return err
		}
		losses, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Loss, error) {
			var l Loss
			return l, r.Scan(&l.Kind, &l.Reason, &l.Count)
		})
		return err
	})
	if errors.Is(err, ErrUnauthorized) {
		return false, nil, err
	}
	if err != nil {
		return false, nil, fmt.Errorf("specula: hello: %w", err)
	}
	return duplicate, losses, nil
}
