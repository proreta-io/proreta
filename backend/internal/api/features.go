package api

import (
	"context"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/entitlements"
)

// featureError is an operation the organization is not entitled to; it becomes 403
// urn:proreta:problem:feature-unavailable.
type featureError struct{ feature entitlements.Feature }

func (e *featureError) Error() string { return "feature not available: " + string(e.feature) }

// requireFeature returns a *featureError unless the organization is entitled to feature. A nil
// provider entitles nobody to anything. A paid operation calls it before doing any work.
func requireFeature(ctx context.Context, p EntitlementsProvider, tenantID uuid.UUID, feature entitlements.Feature) error {
	if p == nil {
		return &featureError{feature: feature}
	}
	features, err := p.Features(ctx, tenantID)
	if err != nil {
		return err
	}
	if !entitlements.Includes(features, feature) {
		return &featureError{feature: feature}
	}
	return nil
}
