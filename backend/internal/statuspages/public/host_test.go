package public

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
)

func TestClassifyHost(t *testing.T) {
	c := NewHostClassifier([]string{"Proreta.Example.com", "app.internal.example.com"})
	for _, tc := range []struct {
		raw  string
		kind HostKind
		host string
	}{
		{"proreta.example.com", HostInstallation, "proreta.example.com"},
		{"PRORETA.example.com:8443", HostInstallation, "proreta.example.com"},
		{"proreta.example.com.", HostInstallation, "proreta.example.com"},
		{"app.internal.example.com", HostInstallation, "app.internal.example.com"},
		{"status.acme.com", HostCustom, "status.acme.com"},
		{"Status.ACME.com:443", HostCustom, "status.acme.com"},
		{"status.acme.com.", HostCustom, "status.acme.com"},
		// Below an installation host is not the installation host, and not a customer's either.
		{"x.proreta.example.com", HostInvalid, ""},
		{"", HostInvalid, ""},
		{"127.0.0.1", HostInvalid, ""},
		{"127.0.0.1:8080", HostInvalid, ""},
		{"[::1]:8080", HostInvalid, ""},
		{"[::1]", HostInvalid, ""},
		{"2130706433", HostInvalid, ""},
		{"localhost", HostInvalid, ""},
		{"acme.com", HostInvalid, ""},
		{"com", HostInvalid, ""},
		{"status.acme.com:", HostCustom, "status.acme.com"},
		{"status.acme.com:80:80", HostInvalid, ""},
		{"stat us.acme.com", HostInvalid, ""},
		{"*.acme.com", HostInvalid, ""},
		{"status_.acme.com", HostInvalid, ""},
		{"stätus.acme.com", HostInvalid, ""},
		{strings.Repeat("a", 64) + ".acme.com", HostInvalid, ""},
		{"status.acme.com/path", HostInvalid, ""},
	} {
		kind, host := c.Classify(tc.raw)
		if kind != tc.kind || host != tc.host {
			t.Errorf("Classify(%q) = %v %q, want %v %q", tc.raw, kind, host, tc.kind, tc.host)
		}
	}
}

type countingHosts struct {
	mu    sync.Mutex
	hosts map[string]string
	calls map[string]int
	total atomic.Int64
	err   error
	delay time.Duration
}

func (f *countingHosts) LookupHost(_ context.Context, host string) (string, bool, error) {
	f.total.Add(1)
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[host]++
	if f.err != nil {
		return "", false, f.err
	}
	slug, ok := f.hosts[host]
	return slug, ok, nil
}

func TestHostCacheTTLNegativeAndErrors(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC))
	src := &countingHosts{hosts: map[string]string{"status.acme.com": "acme"}}
	c := NewHostCache(src, clk)
	ctx := context.Background()
	if _, _, ok := c.Cached("status.acme.com"); ok {
		t.Fatal("cached before any lookup")
	}
	for _, host := range []string{"status.acme.com", "nope.acme.com"} {
		if _, _, err := c.Fetch(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	if slug, found, ok := c.Cached("status.acme.com"); !ok || !found || slug != "acme" {
		t.Fatalf("positive entry: %q %v %v", slug, found, ok)
	}
	if _, found, ok := c.Cached("nope.acme.com"); !ok || found {
		t.Fatalf("negative entry: %v %v", found, ok)
	}
	clk.Advance(HostCacheTTL - time.Second)
	if _, _, ok := c.Cached("nope.acme.com"); !ok {
		t.Fatal("expired early")
	}
	clk.Advance(2 * time.Second)
	if _, _, ok := c.Cached("nope.acme.com"); ok {
		t.Fatal("entry outlived its TTL")
	}
	src.err = errors.New("db down")
	if _, _, err := c.Fetch(ctx, "other.acme.com"); err == nil {
		t.Fatal("error swallowed")
	}
	if _, _, ok := c.Cached("other.acme.com"); ok {
		t.Fatal("an error was cached")
	}
}

func TestHostCacheIsBoundedAndSharesConcurrentMisses(t *testing.T) {
	src := &countingHosts{delay: 50 * time.Millisecond, hosts: map[string]string{"status.acme.com": "acme"}}
	c := NewHostCache(src, clock.System{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = c.Fetch(context.Background(), "status.acme.com")
		}()
	}
	wg.Wait()
	if n := src.calls["status.acme.com"]; n != 1 {
		t.Errorf("concurrent misses made %d lookups", n)
	}

	src = &countingHosts{}
	c = NewHostCache(src, clock.System{})
	for i := range maxHostCacheEntries + 500 {
		_, _, _ = c.Fetch(context.Background(), fmt.Sprintf("h%d.acme.com", i))
	}
	c.mu.Lock()
	n := len(c.items)
	c.mu.Unlock()
	if n > maxHostCacheEntries {
		t.Errorf("cache holds %d entries, cap %d", n, maxHostCacheEntries)
	}
}

func TestForwardedHTTPS(t *testing.T) {
	req := func(hdr ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, v := range hdr {
			r.Header.Add("X-Forwarded-Proto", v)
		}
		return r
	}
	tlsReq := req()
	tlsReq.TLS = &tls.ConnectionState{}
	for _, tc := range []struct {
		name string
		r    *http.Request
		hops int
		want bool
	}{
		{"direct TLS", tlsReq, 0, true},
		{"https from one trusted hop", req("https"), 1, true},
		{"http from one trusted hop", req("http"), 1, false},
		{"no trusted proxy ignores the header", req("https"), 0, false},
		{"no header", req(), 1, false},
		// The client's own header comes first; the trusted proxy's value is the last.
		{"client lies, one hop", req("https, http"), 1, false},
		{"client says http, proxy https", req("http, https"), 1, true},
		{"two hops use the outermost proxy", req("https, http"), 2, true},
		{"more hops than values", req("https"), 3, true},
		{"case", req("HTTPS"), 1, true},
	} {
		if got := forwardedHTTPS(tc.r, tc.hops); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ---- renderer in host mode ----

type hostHarness struct {
	*harness
	hosts *countingHosts
	reg   *prometheus.Registry
}

const abuse = "https://abuse.example.net/report"

// newHostHarness serves status.acme.com and status.other.org with the page "acme", and
// secret.acme.com with a protected page "locked". Installation host: proreta.example.com.
func newHostHarness(t *testing.T, mutate func(*Config)) *hostHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	src := &fakeSource{pages: map[string]stored{
		"acme":   {payload: payload(t, nil)},
		"locked": {payload: payload(t, nil), hash: "$argon2id$x"},
	}}
	hosts := &countingHosts{hosts: map[string]string{
		"status.acme.com": "acme", "status.other.org": "acme", "secret.acme.com": "locked", "secret.other.org": "locked",
		"gone.acme.com": "deleted-page",
	}}
	reg := prometheus.NewRegistry()
	cfg := Config{PublicURL: publicURL, TrustedProxyHops: 1, CookieSecure: true, CustomDomains: true,
		InstallationHosts: []string{"proreta.example.com"}, AbuseURL: abuse}
	if mutate != nil {
		mutate(&cfg)
	}
	srv := httptest.NewServer(NewHandler(cfg, Deps{Source: src, Hosts: hosts, Registerer: reg, Clock: clk,
		Cookies: NewCookieSigner([]byte("0123456789abcdef0123456789abcdef"))}))
	t.Cleanup(srv.Close)
	return &hostHarness{harness: &harness{t: t, src: src, srv: srv, clk: clk}, hosts: hosts, reg: reg}
}

// at sends a request for host (the Host header), as the edge would forward it.
func (h *hostHarness) at(host, method, path, body string, hdr map[string]string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{code: r.StatusCode, header: r.Header, body: string(b)}
}

// counter reads a labelled counter of the harness registry (the label is the only one it has).
func (h *hostHarness) counter(name, label string) float64 {
	h.t.Helper()
	fams, err := h.reg.Gather()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == label {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func hashFor(t *testing.T, password string) string {
	t.Helper()
	hash, err := crypto.HashPassword(password, crypto.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func (h *hostHarness) getAt(host, path string) resp {
	return h.at(host, http.MethodGet, path, "", nil)
}

func TestHostModeServesThePageAtTheRoot(t *testing.T) {
	h := newHostHarness(t, nil)
	r := h.getAt("status.acme.com", "/")
	if r.code != http.StatusOK || r.header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("page: %d %v", r.code, r.header)
	}
	for _, want := range []string{
		`<link rel="canonical" href="https://status.acme.com/">`,
		`href="/feed.rss"`, `href="/status.json"`, `href="/_assets/status.css?v=`,
		"Status page by Proreta", `<a href="` + abuse + `" rel="noopener noreferrer">Report abuse</a>`, "Partial outage",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"/status/", "Powered by Proreta", "proreta.example.com"} {
		if strings.Contains(r.body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if r.header.Get("Cross-Origin-Resource-Policy") != "same-origin" || !strings.Contains(r.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		!strings.Contains(r.header.Get("Content-Security-Policy"), "form-action 'self'") {
		t.Errorf("headers: %v", r.header)
	}
	// Host case, port and a trailing dot do not matter.
	if r2 := h.getAt("STATUS.Acme.com.:443", "/"); r2.code != http.StatusOK || !strings.Contains(r2.body, `href="https://status.acme.com/"`) {
		t.Errorf("normalised host: %d", r2.code)
	}
	if css := h.getAt("status.acme.com", "/_assets/status.css"); css.code != http.StatusOK || !strings.Contains(css.header.Get("Content-Type"), "text/css") {
		t.Errorf("css: %d", css.code)
	}
}

func TestHostModeURLsUseTheRequestHost(t *testing.T) {
	h := newHostHarness(t, nil)
	for _, host := range []string{"status.acme.com", "status.other.org"} {
		origin := "https://" + host
		rss := h.getAt(host, "/feed.rss")
		if rss.code != http.StatusOK || !strings.Contains(rss.body, "<link>"+origin+"/</link>") ||
			!strings.Contains(rss.body, "<link>"+origin+"/#incident-aaaaaaaaaaaaaaaaaaaaaaaa</link>") {
			t.Errorf("%s rss: %d\n%s", host, rss.code, rss.body)
		}
		if strings.Contains(rss.body, "proreta.example.com") || strings.Contains(rss.body, "/status/") {
			t.Errorf("%s rss carries the installation URL", host)
		}
		badge := h.getAt(host, "/badge")
		if badge.code != http.StatusOK || !strings.Contains(badge.body, `href="`+origin+`/"`) || strings.Contains(badge.body, "proreta.example.com") {
			t.Errorf("%s badge: %d %s", host, badge.code, badge.body)
		}
		embed := h.getAt(host, "/embed.js")
		if embed.code != http.StatusOK || !strings.Contains(embed.body, origin+"/badge") || strings.Contains(embed.body, "proreta.example.com") {
			t.Errorf("%s embed: %d %s", host, embed.code, embed.body)
		}
		if js := h.getAt(host, "/status.json"); js.code != http.StatusOK || js.header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s json: %d", host, js.code)
		}
	}
	// X-Forwarded-Host is never read.
	r := h.at("status.acme.com", http.MethodGet, "/feed.rss", "", map[string]string{"X-Forwarded-Host": "evil.example", "Forwarded": "host=evil.example"})
	if strings.Contains(r.body, "evil.example") {
		t.Error("X-Forwarded-Host reached the feed")
	}
}

func TestHostModeRobots(t *testing.T) {
	h := newHostHarness(t, nil)
	if r := h.getAt("status.acme.com", "/robots.txt"); r.code != http.StatusOK || r.body != "User-agent: *\nAllow: /\n" || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
		t.Errorf("public robots: %d %q", r.code, r.body)
	}
	if r := h.getAt("secret.acme.com", "/robots.txt"); r.code != http.StatusOK || r.body != "User-agent: *\nDisallow: /\n" {
		t.Errorf("protected robots: %d %q", r.code, r.body)
	}
	if r := h.getAt("unknown.acme.com", "/robots.txt"); r.code != http.StatusNotFound {
		t.Errorf("unknown host robots: %d", r.code)
	}
}

// Every refusal is one answer: same status, headers and body, so nothing can be probed.
func TestHostModeNotFoundIsIdentical(t *testing.T) {
	h := newHostHarness(t, nil)
	base := h.getAt("nobody.acme.com", "/")
	if base.code != http.StatusNotFound || !strings.Contains(base.body, "Status page not found") || base.header.Get("Cache-Control") != "public, max-age=30" ||
		base.header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("404: %d %v", base.code, base.header)
	}
	type c struct{ host, method, path string }
	for _, tc := range []c{
		// not found, unverified, failed, disabled, suspended, unpublished all look the same to the renderer
		{"gone.acme.com", "GET", "/"},                // host maps to a slug whose page is not published or deleted
		{"nobody.other.org", "GET", "/status.json"},  //
		{"nobody.acme.com", "GET", "/feed.rss"},      //
		{"nobody.acme.com", "GET", "/badge"},         //
		{"nobody.acme.com", "GET", "/embed.js"},      //
		{"gone.acme.com", "GET", "/robots.txt"},      //
		{"nobody.acme.com", "POST", "/unlock"},       //
		{"127.0.0.1", "GET", "/"},                    // IP literal
		{"127.0.0.1:8080", "GET", "/"},               //
		{"[::1]", "GET", "/"},                        //
		{"", "GET", "/"},                             //
		{"bad host.acme.com", "GET", "/"},            //
		{"acme.com", "GET", "/"},                     // registrable domain itself
		{"x.proreta.example.com", "GET", "/"},        // below an installation host
		{"status.acme.com", "GET", "/status/acme"},   // a customer host never exposes a slug path
		{"status.acme.com", "GET", "/status/locked"}, //
		{"status.acme.com", "GET", "/status/acme.json"},
		{"status.acme.com", "GET", "/nothing"},     //
		{"status.acme.com", "GET", "/unlock"},      // GET on a POST route
		{"status.acme.com", "DELETE", "/"},         //
		{"secret.acme.com", "GET", "/status.json"}, // protected pages have no JSON, RSS, badge or embed
		{"secret.acme.com", "GET", "/feed.rss"},    //
		{"secret.acme.com", "GET", "/badge"},       //
		{"secret.acme.com", "GET", "/embed.js"},    //
		{"status.acme.com", "POST", "/unlock"},     // a public page has no password
	} {
		r := h.at(tc.host, tc.method, tc.path, "password=x", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if comparable(r) != comparable(base) {
			t.Errorf("%s %s%s differs from an unknown host:\n%s\n---\n%s", tc.method, tc.host, tc.path, comparable(r), comparable(base))
		}
	}
	// Even over TLS: HSTS is only on success, so a 404 stays the same bytes.
	if r := h.at("status.acme.com", "GET", "/nothing", "", map[string]string{"X-Forwarded-Proto": "https"}); comparable(r) != comparable(base) {
		t.Errorf("404 over TLS differs")
	}
}

func TestHostModeKeepsTheInstallationHost(t *testing.T) {
	h := newHostHarness(t, nil)
	r := h.getAt("proreta.example.com", "/status/acme")
	if r.code != http.StatusOK || !strings.Contains(r.body, "Powered by Proreta") || strings.Contains(r.body, "Status page by Proreta") ||
		!strings.Contains(r.body, `href="/status/acme/feed.rss"`) || strings.Contains(r.body, "canonical") || r.header.Get("Cross-Origin-Resource-Policy") != "" {
		t.Errorf("installation page: %d", r.code)
	}
	if rss := h.getAt("proreta.example.com:443", "/status/acme/feed.rss"); !strings.Contains(rss.body, "<link>"+publicURL+"/status/acme</link>") {
		t.Errorf("installation rss: %s", rss.body)
	}
	// On the installation host "/" is not a page.
	if r := h.getAt("proreta.example.com", "/"); r.code != http.StatusNotFound {
		t.Errorf("installation root: %d", r.code)
	}
	// And a customer host with a slug path stays closed.
	if r := h.getAt("status.acme.com", "/status/acme"); r.code != http.StatusNotFound {
		t.Errorf("slug path on a customer host: %d", r.code)
	}
}

// With the feature off the Host header is not read at all: every host gets the path routes.
func TestHostModeOffIgnoresTheHost(t *testing.T) {
	h := newHostHarness(t, func(c *Config) { c.CustomDomains = false })
	for _, host := range []string{"proreta.example.com", "status.acme.com", "127.0.0.1", ""} {
		r := h.getAt(host, "/status/acme")
		if r.code != http.StatusOK || !strings.Contains(r.body, "Powered by Proreta") || strings.Contains(r.body, "canonical") {
			t.Errorf("host %q: %d", host, r.code)
		}
	}
	if r := h.getAt("status.acme.com", "/"); r.code != http.StatusNotFound {
		t.Errorf("root with the feature off: %d", r.code)
	}
	if h.hosts.total.Load() != 0 {
		t.Error("hosts were looked up with the feature off")
	}
}

func TestHostModeHSTSOnlyOverTLS(t *testing.T) {
	h := newHostHarness(t, nil)
	tls := map[string]string{"X-Forwarded-Proto": "https"}
	for _, path := range []string{"/", "/status.json", "/feed.rss", "/badge", "/embed.js", "/robots.txt"} {
		r := h.at("status.acme.com", http.MethodGet, path, "", tls)
		if r.code != http.StatusOK || r.header.Get("Strict-Transport-Security") != "max-age=31536000" {
			t.Errorf("%s over TLS: %d %q", path, r.code, r.header.Get("Strict-Transport-Security"))
		}
		if p := h.at("status.acme.com", http.MethodGet, path, "", map[string]string{"X-Forwarded-Proto": "http"}); p.header.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s over plain HTTP: HSTS sent", path)
		}
	}
	if r := h.getAt("status.acme.com", "/"); r.header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS without a forwarded scheme")
	}
	// No includeSubDomains, no preload.
	if v := h.at("status.acme.com", http.MethodGet, "/", "", tls).header.Get("Strict-Transport-Security"); strings.Contains(v, "includeSubDomains") || strings.Contains(v, "preload") {
		t.Errorf("HSTS %q", v)
	}
	// A client cannot ask for HSTS when no proxy is trusted.
	h = newHostHarness(t, func(c *Config) { c.TrustedProxyHops = 0 })
	if r := h.at("status.acme.com", http.MethodGet, "/", "", tls); r.header.Get("Strict-Transport-Security") != "" {
		t.Error("forwarded scheme trusted with no trusted hop")
	}
	// The installation host's HSTS stays with the gateway.
	if r := h.at("proreta.example.com", http.MethodGet, "/status/acme", "", tls); r.header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on the installation host")
	}
}

func TestHostModePasswordFlowAndHostScopedCookie(t *testing.T) {
	h := newHostHarness(t, nil)
	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	lock := h.getAt("secret.acme.com", "/")
	if lock.code != http.StatusOK || !strings.Contains(lock.body, "This status page is protected") || !strings.Contains(lock.body, `action="/unlock"`) ||
		!strings.Contains(lock.body, "Never enter your account password here.") || !strings.Contains(lock.body, "Status page by Proreta") ||
		!strings.Contains(lock.body, abuse) || lock.header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("lock form: %d\n%s", lock.code, lock.body)
	}
	// A cross-site post, and a post claiming another host's origin, are refused.
	for _, hdr := range []map[string]string{
		{"Origin": "https://evil.example"}, {"Origin": "https://status.acme.com"}, {"Origin": publicURL}, {"Origin": "http://secret.acme.com"},
		{"Sec-Fetch-Site": "cross-site"}, {"Origin": "null", "Sec-Fetch-Site": "cross-site"},
	} {
		hdr["Content-Type"] = form["Content-Type"]
		if r := h.at("secret.acme.com", http.MethodPost, "/unlock", "password=x", hdr); r.code != http.StatusNotFound {
			t.Errorf("%v: unlock answered %d", hdr, r.code)
		}
	}
	// A wrong password through the right origin: the lock form again. (The argon2 hash of the fake
	// is not a valid hash, so a right password cannot be tested here; the cookie is signed directly.)
	wrong := h.at("secret.acme.com", http.MethodPost, "/unlock", "password=x", map[string]string{"Content-Type": form["Content-Type"], "Origin": "https://secret.acme.com"})
	if wrong.code != http.StatusUnauthorized || !strings.Contains(wrong.body, "That password is not correct.") {
		t.Errorf("wrong password: %d", wrong.code)
	}

	signer := NewCookieSigner([]byte("0123456789abcdef0123456789abcdef"))
	good := signer.HostValue("secret.acme.com", "locked", "$argon2id$x", h.clk.Now())
	cookie := func(v string) map[string]string { return map[string]string{"Cookie": HostCookieName + "=" + v} }
	if r := h.at("secret.acme.com", http.MethodGet, "/", "", cookie(good)); r.code != http.StatusOK || strings.Contains(r.body, "This status page is protected") ||
		r.header.Get("Vary") != "Cookie" || !strings.Contains(r.header.Get("X-Robots-Tag")+r.body, "noindex") {
		t.Errorf("unlocked page: %d", r.code)
	}
	// The same page on another host (same slug) refuses the cookie of the first host.
	if r := h.at("secret.other.org", http.MethodGet, "/", "", cookie(good)); !strings.Contains(r.body, "This status page is protected") {
		t.Error("a cookie of one host unlocked the page on another host")
	}
	// An installation-host cookie, and the plain cookie name, do not unlock a customer host.
	plain := signer.Value("locked", "$argon2id$x", h.clk.Now())
	if r := h.at("secret.acme.com", http.MethodGet, "/", "", cookie(plain)); !strings.Contains(r.body, "This status page is protected") {
		t.Error("an installation cookie unlocked a customer host")
	}
	if r := h.at("secret.acme.com", http.MethodGet, "/", "", map[string]string{"Cookie": CookieName + "=" + good}); !strings.Contains(r.body, "This status page is protected") {
		t.Error("the path cookie name unlocked a customer host")
	}
	// And the other way round: a host cookie does not unlock the installation path.
	if r := h.at("proreta.example.com", http.MethodGet, "/status/locked", "", map[string]string{"Cookie": CookieName + "=" + good}); !strings.Contains(r.body, "This status page is protected") {
		t.Error("a host cookie unlocked the installation host")
	}
}

func TestHostCookieAttributes(t *testing.T) {
	// The fake password hash cannot verify, so the Set-Cookie is checked with a real hash.
	h := newHostHarness(t, nil)
	hash := hashFor(t, "a long enough password")
	h.src.pages["locked"] = stored{payload: payload(t, nil), hash: hash}
	body := "password=" + url.QueryEscape("a long enough password")
	r := h.at("secret.acme.com", http.MethodPost, "/unlock", body, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://secret.acme.com"})
	if r.code != http.StatusSeeOther || r.header.Get("Location") != "/" {
		t.Fatalf("unlock: %d %v", r.code, r.header)
	}
	cs := (&http.Response{Header: r.header}).Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies: %v", cs)
	}
	c := cs[0]
	if c.Name != "__Host-proreta_status_unlock" || c.Path != "/" || c.Domain != "" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 12*3600 {
		t.Errorf("cookie %+v", c)
	}
	if got := h.at("secret.acme.com", http.MethodGet, "/", "", map[string]string{"Cookie": c.Name + "=" + c.Value}); strings.Contains(got.body, "This status page is protected") {
		t.Error("the issued cookie does not unlock its own host")
	}
	if got := h.at("secret.other.org", http.MethodGet, "/", "", map[string]string{"Cookie": c.Name + "=" + c.Value}); !strings.Contains(got.body, "This status page is protected") {
		t.Error("the issued cookie unlocked another host")
	}
	// Secure is on even when the installation runs with PRORETA_COOKIE_SECURE=false.
	h2 := newHostHarness(t, func(c *Config) { c.CookieSecure = false })
	h2.src.pages["locked"] = stored{payload: payload(t, nil), hash: hash}
	r = h2.at("secret.acme.com", http.MethodPost, "/unlock", body, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if cs := (&http.Response{Header: r.header}).Cookies(); len(cs) != 1 || !cs[0].Secure {
		t.Errorf("cookie without Secure: %v", cs)
	}
}

func TestHostModeLimits(t *testing.T) {
	// Unknown hosts: a flood of random Host values reaches the database at most a burst, then 429.
	h := newHostHarness(t, nil)
	limited, ok := 0, 0
	for i := range 400 {
		r := h.at(fmt.Sprintf("r%d.acme.com", i), http.MethodGet, "/", "", map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i%250+1)})
		switch r.code {
		case http.StatusNotFound:
			ok++
		case http.StatusTooManyRequests:
			limited++
			if r.header.Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
		default:
			t.Fatalf("status %d", r.code)
		}
	}
	if n := h.hosts.total.Load(); n > unknownHostBurst+5 || limited == 0 || ok < unknownHostBurst {
		t.Errorf("lookups %d, 404s %d, 429s %d", n, ok, limited)
	}
	if got := h.counter("proreta_statuspage_host_lookups_total", "limited"); got == 0 {
		t.Error("limited lookups not counted")
	}
	// A cached host (known or not) is not charged to that limiter: it still answers.
	// A host that is not cached yet is refused during the flood (Retry-After says when), ...
	if r := h.at("status.acme.com", http.MethodGet, "/", "", map[string]string{"X-Forwarded-For": "192.0.2.77"}); r.code != http.StatusTooManyRequests {
		t.Errorf("uncached host during the flood: %d", r.code)
	}
	// ... and served once the limiter refilled.
	h.clk.Advance(5 * time.Second)
	for range 3 {
		if r := h.at("status.acme.com", http.MethodGet, "/", "", map[string]string{"X-Forwarded-For": "192.0.2.78"}); r.code != http.StatusOK {
			t.Fatalf("known host after the flood: %d", r.code)
		}
	}
	before := h.hosts.total.Load()
	for i := range 20 {
		h.at("status.acme.com", http.MethodGet, "/", "", map[string]string{"X-Forwarded-For": fmt.Sprintf("192.0.2.%d", 100+i)})
	}
	if h.hosts.total.Load() != before {
		t.Error("cached host looked up again")
	}
}

func TestHostModePerHostLimitIsSharedByAllClients(t *testing.T) {
	h := newHostHarness(t, nil)
	codes := map[int]int{}
	for i := range hostViewBurst + 40 {
		// Each request from its own client, so only the per-host limiter can refuse.
		r := h.at("status.acme.com", http.MethodGet, "/status.json", "", map[string]string{"X-Forwarded-For": fmt.Sprintf("10.%d.%d.1", i/250, i%250)})
		codes[r.code]++
	}
	if codes[http.StatusOK] < hostViewBurst || codes[http.StatusTooManyRequests] == 0 {
		t.Errorf("codes %v", codes)
	}
	// Another host has its own budget.
	if r := h.at("status.other.org", http.MethodGet, "/status.json", "", map[string]string{"X-Forwarded-For": "10.250.1.1"}); r.code != http.StatusOK {
		t.Errorf("other host: %d", r.code)
	}
}

func TestHostModeLookupErrorIsTheSame404(t *testing.T) {
	h := newHostHarness(t, nil)
	base := h.getAt("nobody.acme.com", "/")
	h.hosts.err = errors.New("db down")
	if r := h.getAt("fresh.acme.com", "/"); comparable(r) != comparable(base) {
		t.Errorf("lookup error: %d", r.code)
	}
}

func TestHostModeMetrics(t *testing.T) {
	h := newHostHarness(t, nil)
	h.getAt("proreta.example.com", "/status/acme")
	h.getAt("status.acme.com", "/")
	h.getAt("status.acme.com", "/")
	h.getAt("127.0.0.1", "/")
	for _, tc := range []struct {
		name, label string
		want        float64
	}{
		{"proreta_statuspage_requests_total", "path", 1}, {"proreta_statuspage_requests_total", "host", 3},
		{"proreta_statuspage_host_lookups_total", "miss", 1}, {"proreta_statuspage_host_lookups_total", "hit", 1},
		{"proreta_statuspage_host_lookups_total", "unknown", 1},
	} {
		if got := h.counter(tc.name, tc.label); got != tc.want {
			t.Errorf("%s{%s} = %v, want %v", tc.name, tc.label, got, tc.want)
		}
	}
}
