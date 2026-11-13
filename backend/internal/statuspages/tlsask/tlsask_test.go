package tlsask

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeAllower struct {
	mu    sync.Mutex
	hosts map[string]bool
	calls []string
	err   error
}

func (f *fakeAllower) Allowed(_ context.Context, host string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host)
	return f.hosts[host], f.err
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newHandler(t *testing.T, f *fakeAllower, c *clock, mod func(*Options)) *Handler {
	t.Helper()
	o := Options{Token: testToken, Now: c.now, Rules: domains.NewRules([]string{"proreta.example.com"}, nil), Registerer: prometheus.NewRegistry()}
	if mod != nil {
		mod(&o)
	}
	h, err := New(f, o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, path string, header http.Header) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func ask(host string) string {
	return Path + "?token=" + testToken + "&domain=" + url.QueryEscape(host)
}

func TestNewRefusesAnEmptyToken(t *testing.T) {
	if _, err := New(&fakeAllower{}, Options{}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestAnswerMatrix(t *testing.T) {
	f := &fakeAllower{hosts: map[string]bool{"status.acme.com": true, "xn--80ak6aa92e.acme.com": true}}
	h := newHandler(t, f, &clock{time.Unix(1e9, 0)}, nil)
	bearer := http.Header{"Authorization": {"Bearer " + testToken}}
	for _, tc := range []struct {
		name, path string
		header     http.Header
		want       int
	}{
		{"allowed (query token)", ask("status.acme.com"), nil, 200},
		{"allowed (bearer)", Path + "?domain=status.acme.com", bearer, 200},
		{"normalised: case, trailing dot, space", ask(" Status.ACME.com. "), nil, 200},
		{"unknown host", ask("other.acme.com"), nil, 404},
		{"missing domain", Path + "?token=" + testToken, nil, 404},
		{"ip literal", ask("203.0.113.7"), nil, 404},
		{"installation host never asked", ask("proreta.example.com"), nil, 404},
		{"wildcard", ask("*.acme.com"), nil, 404},
		{"unicode form refused", ask("аcme.com"), nil, 404},
		{"no token", Path + "?domain=status.acme.com", nil, 401},
		{"wrong token", Path + "?domain=status.acme.com&token=" + strings.Repeat("x", 32), nil, 401},
		{"wrong bearer wins over query", ask("status.acme.com"), http.Header{"Authorization": {"Bearer nope"}}, 401},
		{"overlong token", Path + "?domain=status.acme.com&token=" + strings.Repeat("a", 1000), nil, 401},
		{"other path", "/internal/tls/other?token=" + testToken, nil, 404},
	} {
		code, body := get(h, tc.path, tc.header)
		if code != tc.want || body != "" {
			t.Errorf("%s: %d %q, want %d and an empty body", tc.name, code, body, tc.want)
		}
	}
	for _, c := range f.calls {
		if c != "status.acme.com" && c != "other.acme.com" {
			t.Errorf("lookup for %q: invalid or unauthenticated hosts must not reach the database", c)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ask("status.acme.com"), nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestAnswersAreCachedBriefly(t *testing.T) {
	f := &fakeAllower{hosts: map[string]bool{"status.acme.com": true}}
	c := &clock{time.Unix(1e9, 0)}
	h := newHandler(t, f, c, func(o *Options) { o.CacheTTL = 10 * time.Second })
	for range 5 {
		get(h, ask("status.acme.com"), nil)
		get(h, ask("gone.acme.com"), nil)
	}
	if len(f.calls) != 2 {
		t.Fatalf("lookups = %v, want one per host (positive and negative answers are cached)", f.calls)
	}
	// A takedown shows after the TTL.
	f.mu.Lock()
	f.hosts["status.acme.com"] = false
	f.mu.Unlock()
	c.t = c.t.Add(11 * time.Second)
	if code, _ := get(h, ask("status.acme.com"), nil); code != 404 {
		t.Fatalf("after the TTL = %d, want 404", code)
	}
}

func TestRateLimitOnlyCountsLookups(t *testing.T) {
	f := &fakeAllower{hosts: map[string]bool{"status.acme.com": true}}
	c := &clock{time.Unix(1e9, 0)}
	h := newHandler(t, f, c, func(o *Options) { o.Rate, o.Burst = 1, 2 })
	// Cache hits are free.
	for range 10 {
		if code, _ := get(h, ask("status.acme.com"), nil); code != 200 {
			t.Fatalf("cached = %d", code)
		}
	}
	if code, _ := get(h, ask("a.acme.com"), nil); code != 404 {
		t.Fatalf("second lookup = %d, want 404", code)
	}
	if code, _ := get(h, ask("b.acme.com"), nil); code != 429 {
		t.Fatalf("third lookup = %d, want 429 (burst of 2 used)", code)
	}
	c.t = c.t.Add(time.Second)
	if code, _ := get(h, ask("b.acme.com"), nil); code != 404 {
		t.Fatalf("after a second = %d, want a lookup again", code)
	}
}

func TestLookupFailureIsNotCachedOrAllowed(t *testing.T) {
	f := &fakeAllower{hosts: map[string]bool{"status.acme.com": true}, err: errors.New("db down")}
	h := newHandler(t, f, &clock{time.Unix(1e9, 0)}, nil)
	if code, _ := get(h, ask("status.acme.com"), nil); code != http.StatusServiceUnavailable {
		t.Fatalf("on error = %d, want 503", code)
	}
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	if code, _ := get(h, ask("status.acme.com"), nil); code != 200 {
		t.Fatalf("after recovery = %d, want 200 (errors are not cached)", code)
	}
}

func TestResultMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	f := &fakeAllower{hosts: map[string]bool{"status.acme.com": true}}
	h := newHandler(t, f, &clock{time.Unix(1e9, 0)}, func(o *Options) { o.Registerer = reg })
	get(h, ask("status.acme.com"), nil)
	get(h, ask("x.acme.com"), nil)
	get(h, Path+"?domain=x.acme.com", nil)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			got[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}
	for label, want := range map[string]float64{"allowed": 1, "denied": 1, "unauthorized": 1} {
		if got[label] != want {
			t.Errorf("result %s = %v, want %v", label, got[label], want)
		}
	}
}
