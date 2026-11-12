package results

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/results/resultsdb"
)

// Row is one raw check result. StatusCode and LatencyMs are zero when no response arrived and
// are then stored as NULL (a TCP check has no status code, its latency is the connect time); ErrorKind is empty for a successful check.
type Row struct {
	TenantID    uuid.UUID
	MonitorID   uuid.UUID
	LocationID  uuid.UUID
	CheckedAt   time.Time
	OK          bool
	StatusCode  int
	LatencyMs   int
	ErrorKind   string
	TLSNotAfter *time.Time
}

// Insert stores r in tx, a tenant transaction of r.TenantID.
func Insert(ctx context.Context, tx pgx.Tx, r Row) error {
	p := resultsdb.InsertResultParams{
		TenantID: r.TenantID, MonitorID: r.MonitorID, LocationID: r.LocationID, CheckedAt: r.CheckedAt,
		Ok: r.OK, TlsNotAfter: r.TLSNotAfter,
	}
	if r.StatusCode > 0 {
		code := int16(r.StatusCode) //nolint:gosec // an HTTP status
		p.StatusCode = &code
	}
	// A TCP check that connected has a latency and no status code.
	if r.StatusCode > 0 || r.OK {
		latency := int32(max(r.LatencyMs, 0)) //nolint:gosec // bounded by the 10 s timeout
		p.LatencyMs = &latency
	}
	if r.ErrorKind != "" {
		p.ErrorKind = &r.ErrorKind
	}
	if err := resultsdb.New(tx).InsertResult(ctx, p); err != nil {
		return fmt.Errorf("results: insert: %w", err)
	}
	return nil
}
