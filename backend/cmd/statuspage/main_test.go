package main

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/metrics"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/internal/statuspages/public"
)

type fakeSource struct{}

func (fakeSource) Lookup(_ context.Context, slug string) (public.Entry, error) {
	if slug != "acme" {
		return public.Entry{}, nil
	}
	return public.Entry{Found: true, ETag: `"e"`, JSON: []byte(`{}`),
		Snapshot: statuspages.Snapshot{Page: statuspages.PublicPage{Slug: "acme", Title: "Acme Status"}}}, nil
}

func (fakeSource) LookupHost(_ context.Context, host string) (string, bool, error) {
	return "acme", host == "status.acme.com", nil
}

// The handler main serves must honour the custom-domain settings of the environment.
func TestNewHandlerServesCustomDomain(t *testing.T) {
	env := map[string]string{
		"PRORETA_DATABASE_URL":               "postgres://proreta_statuspage:pw@db:5432/proreta?sslmode=require",
		"PRORETA_PUBLIC_URL":                 "https://proreta.example.com",
		"PRORETA_STATUSPAGE_COOKIE_KEY":      base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		"PRORETA_STATUS_PAGE_CUSTOM_DOMAINS": "true",
		"PRORETA_ABUSE_URL":                  "https://abuse.example.net/report",
	}
	cfg, err := config.LoadStatuspage(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(cfg, fakeSource{}, slog.New(slog.DiscardHandler), metrics.NewRegistry("statuspage", "test", "test"))

	get := func(host, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, string(b)
	}
	code, body := get("status.acme.com", "/")
	if code != http.StatusOK || !strings.Contains(body, "Acme Status") {
		t.Fatalf("custom domain root: %d %.200s", code, body)
	}
	if !strings.Contains(body, "https://abuse.example.net/report") {
		t.Errorf("abuse link missing on a customer domain")
	}
	if code, _ := get("proreta.example.com", "/status/acme"); code != http.StatusOK {
		t.Errorf("installation host path route: %d", code)
	}
	if code, _ := get("other.example", "/"); code != http.StatusNotFound {
		t.Errorf("unknown host: %d", code)
	}
}
