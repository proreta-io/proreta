// Package safehttp builds HTTP clients that only reach public HTTPS endpoints on port 443. It
// is the SSRF guard and is shared by the HTTPS prober and the
// Slack notifier.
//
// The destination is validated where the connection is made: hostnames are resolved by the
// guard, every returned address must be allowed, and the socket is dialed to one of those
// validated IPs (a second resolution cannot change it). The net.Dialer Control hook checks the
// address again on the actual socket. Redirects are re-validated by the same dialer, and
// proxies from the environment are ignored.
//
// Tests reach loopback servers through unexported options. Inside this package they are set
// from _test.go files; other packages reach them only through NewLoopbackTestClient, which
// exists only in builds with the `proretatest` tag (testsupport_proretatest.go, used by
// `make test-e2e`). TestOverrideNotSetOutsideTests enforces both.
package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Typed errors. Their messages are generic and never include resolved addresses.
var (
	ErrBlockedAddress    = errors.New("safehttp: destination address is not allowed")
	ErrBadPort           = errors.New("safehttp: only port 443 is allowed")
	ErrNotHTTPS          = errors.New("safehttp: only https URLs are allowed")
	ErrTooManyRedirects  = errors.New("safehttp: too many redirects")
	ErrRedirectNotHTTPS  = errors.New("safehttp: redirect to a non-https URL")
	ErrRedirectCrossHost = errors.New("safehttp: redirect to another host")
)

type sameHostKey struct{}

// WithSameHostRedirects marks requests made with ctx as carrying data meant for one host only
// (custom headers or a body): a redirect to another host then fails with ErrRedirectCrossHost
// instead of forwarding them.
func WithSameHostRedirects(ctx context.Context) context.Context {
	return context.WithValue(ctx, sameHostKey{}, true)
}

const (
	allowedPort          = "443"
	defaultTimeout       = 10 * time.Second
	maxRedirectsCeiling  = 3
	maxResponseHeaderLen = 64 << 10
)

// Resolver resolves a hostname to IP addresses. *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options configure NewClient. The zero value is valid: 10 s deadline, no redirects, system
// resolver, built-in blocked ranges only.
type Options struct {
	// Timeout is the overall deadline of one request including redirects. Default 10 s.
	Timeout time.Duration
	// MaxRedirects is how many redirects are followed, clamped to [0, 3]. Zero means none
	// (the Slack notifier); the prober uses 3.
	MaxRedirects int
	// BlockedPrefixes are blocked in addition to the built-in ranges (PRORETA_BLOCKED_CIDRS).
	BlockedPrefixes []netip.Prefix
	// Resolver defaults to net.DefaultResolver. Whatever it returns is still validated.
	Resolver Resolver

	// Test-only: replaces the final socket dial (after validation) and the TLS roots. Set only
	// from _test.go files; never assign these in production code.
	dialOverride func(ctx context.Context, network, validatedAddr string) (net.Conn, error)
	rootCAs      *x509.CertPool
	// Test-only (proretatest builds): when non-zero, loopback addresses pass validation and every
	// validated address is dialed on this port instead of 443. URLs must still use port 443.
	loopbackPort uint16
}

// NewClient returns an *http.Client guarded as described in the package documentation.
func NewClient(opts Options) *http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxRedirects := min(max(opts.MaxRedirects, 0), maxRedirectsCeiling)
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	g := &guard{
		extra:    append([]netip.Prefix(nil), opts.BlockedPrefixes...),
		resolver: resolver,
		override: opts.dialOverride,
		testPort: opts.loopbackPort,
	}
	g.dialer = &net.Dialer{Timeout: timeout, Control: g.control}

	transport := &http.Transport{
		Proxy:       nil,
		DialContext: g.dialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    opts.rootCAs, // nil means the system roots
		},
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		DisableKeepAlives:      true, // every request dials (and validates) afresh
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxResponseHeaderLen,
		ForceAttemptHTTP2:      false,
	}
	return &http.Client{
		Transport: schemeCheck{transport},
		Timeout:   timeout,
		Jar:       nil,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return ErrTooManyRedirects
			}
			if req.URL.Scheme != "https" {
				return ErrRedirectNotHTTPS
			}
			if req.Context().Value(sameHostKey{}) != nil && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return ErrRedirectCrossHost
			}
			if p := req.URL.Port(); p != "" && p != allowedPort {
				return ErrBadPort
			}
			return nil
		},
	}
}

// schemeCheck rejects non-https requests before anything is dialed, so http://host:443 never
// sends plaintext.
type schemeCheck struct{ next http.RoundTripper }

func (s schemeCheck) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, ErrNotHTTPS
	}
	if p := req.URL.Port(); p != "" && p != allowedPort {
		return nil, ErrBadPort
	}
	return s.next.RoundTrip(req)
}

type guard struct {
	// anyPort lifts the port-443 rule for NewDialer (TCP checks); the address rules stay.
	anyPort  bool
	extra    []netip.Prefix
	resolver Resolver
	dialer   *net.Dialer
	override func(ctx context.Context, network, validatedAddr string) (net.Conn, error)
	// testPort is the test-only Options.loopbackPort; zero in production.
	testPort uint16
}

// dialPort is the port validated addresses are dialed on: 443, or the test-only loopback port.
func (g *guard) dialPort() uint16 {
	if g.testPort != 0 {
		return g.testPort
	}
	return 443
}

// NewDialer returns a dial function for TCP checks from our own infrastructure: the same
// connect-time rules as NewClient (every resolved address must be allowed, the socket is dialed
// to a validated IP, the control hook checks the actual address again), but to any port, because
// a TCP monitor's purpose is the port. Redirects do not exist here. Only opts.Timeout,
// BlockedPrefixes and Resolver apply.
func NewDialer(opts Options) func(ctx context.Context, network, addr string) (net.Conn, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	g := &guard{
		anyPort: true, extra: append([]netip.Prefix(nil), opts.BlockedPrefixes...), resolver: resolver,
		override: opts.dialOverride, testPort: opts.loopbackPort,
	}
	g.dialer = &net.Dialer{Timeout: timeout, Control: g.control}
	return g.dialContext
}

// allowedAddr is allowed() plus the test-only loopback exception.
func (g *guard) allowedAddr(ip netip.Addr) bool {
	if g.testPort != 0 && ip.Unmap().IsLoopback() {
		return true
	}
	return allowed(ip, g.extra)
}

// dialContext resolves the host once, requires every address to be allowed, and dials the
// validated IPs directly.
func (g *guard) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, ErrBlockedAddress
	}
	dialPort := g.dialPort()
	if g.anyPort {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, ErrBadPort
		}
		dialPort = uint16(n)
	} else if port != allowedPort {
		return nil, ErrBadPort
	}
	ips, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !g.allowedAddr(ip) {
			return nil, ErrBlockedAddress
		}
	}
	var lastErr error
	for i, ip := range ips {
		target := netip.AddrPortFrom(ip.Unmap(), dialPort).String()
		var conn net.Conn
		actx, cancel := attemptContext(ctx, len(ips)-i)
		if g.override != nil {
			conn, err = g.override(actx, network, target)
		} else {
			conn, err = g.dialer.DialContext(actx, network, target)
		}
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, ErrBlockedAddress) || errors.Is(err, ErrBadPort) {
			break
		}
	}
	return nil, lastErr
}

// minAttempt is the least a dial attempt gets while budget remains.
const minAttempt = 250 * time.Millisecond

// attemptContext gives one of the left remaining addresses a share of the time that is left, so an
// address that never answers (a blackholed AAAA) cannot use up the budget of the ones after it.
func attemptContext(ctx context.Context, left int) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok || left <= 1 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, max(time.Until(dl)/time.Duration(left), minAttempt))
}

func (g *guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	ips, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("safehttp: resolve: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("safehttp: resolve: %w", &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true})
	}
	return ips, nil
}

// control runs on the real socket just before connect(2): the last line of defence if anything
// above ever dials an address that was not validated.
func (g *guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	if !g.anyPort && ap.Port() != g.dialPort() {
		return ErrBadPort
	}
	if !g.allowedAddr(ap.Addr()) {
		return ErrBlockedAddress
	}
	return nil
}
