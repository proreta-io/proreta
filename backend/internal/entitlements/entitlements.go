// Package entitlements answers which paid features an organization may use. They come from the
// tenant's plan (plans.features); other sources can be added behind the same methods. Consumers
// declare the interface they need (for example the api's EntitlementsProvider) and get a
// *Service.
package entitlements

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
)

// Feature is the key of a paid feature. Feature keys are stable strings: they are stored in
// plans.features and listed to clients in the session's features, so a key is never renamed or
// reused. Every feature gets a constant here; none is defined yet.
type Feature string

// Service reads entitlements from the database.
type Service struct {
	pool *db.Pool
}

// NewService returns a Service.
func NewService(pool *db.Pool) *Service {
	return &Service{pool: pool}
}

// Features returns the features of a tenant, sorted; empty, never nil, when there are none.
func (s *Service) Features(ctx context.Context, tenantID uuid.UUID) ([]string, error) {
	var out []string
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = plans.Features(ctx, tx, tenantID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Has reports whether a tenant is entitled to feature. It is the seam for gated features: a
// feature-specific handler or module checks its Feature here before it runs. Only tests call it
// until the first gated feature exists.
func (s *Service) Has(ctx context.Context, tenantID uuid.UUID, feature Feature) (bool, error) {
	features, err := s.Features(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return Includes(features, feature), nil
}

// Includes reports whether feature is in a sorted feature list.
func Includes(features []string, feature Feature) bool {
	if feature == "" {
		return false
	}
	_, ok := slices.BinarySearch(features, string(feature))
	return ok
}
