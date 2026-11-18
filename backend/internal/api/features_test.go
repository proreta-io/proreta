package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestAPIHeadersHSTS(t *testing.T) {
	h := apiHeaders(1)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for proto, want := range map[string]bool{"https": true, "http": false, "": false} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Strict-Transport-Security") != ""; got != want {
			t.Errorf("proto %q: HSTS %v, want %v", proto, got, want)
		}
	}
}

func TestPageOfRejectsNonPositiveLimit(t *testing.T) {
	for _, n := range []int{-1, 0} {
		if _, err := pageOf(&n, nil); err == nil {
			t.Errorf("limit %d accepted", n)
		}
	}
}
