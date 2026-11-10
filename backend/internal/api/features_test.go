package api

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeEntitlements struct {
	features []string
	err      error
}

func (f fakeEntitlements) Features(context.Context, uuid.UUID) ([]string, error) {
	return f.features, f.err
}

func TestRequireFeature(t *testing.T) {
	ctx, tenant := context.Background(), uuid.New()
	var fe *featureError
	if err := requireFeature(ctx, fakeEntitlements{features: []string{"audit-export", "sso"}}, tenant, "sso"); err != nil {
		t.Fatalf("entitled feature: %v", err)
	}
	if err := requireFeature(ctx, fakeEntitlements{features: []string{"sso"}}, tenant, "scim"); !errors.As(err, &fe) || fe.feature != "scim" {
		t.Fatalf("missing feature: %v", err)
	}
	if err := requireFeature(ctx, nil, tenant, "sso"); !errors.As(err, &fe) {
		t.Fatalf("no provider: %v", err)
	}
	boom := errors.New("db down")
	if err := requireFeature(ctx, fakeEntitlements{err: boom}, tenant, "sso"); !errors.Is(err, boom) {
		t.Fatalf("lookup error: %v", err)
	}
}
