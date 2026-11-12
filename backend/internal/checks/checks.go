// Package checks runs monitor checks. There are two kinds. HTTP: an HTTPS request through the
// SSRF guard (internal/safehttp) that records status, latency to response headers and the leaf
// certificate expiry, and never reads the response body. TCP: a connection to a host and a port,
// up when the handshake completes, with the latency of the connect and nothing sent or read.
package checks

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/proreta-io/proreta/backend/internal/safehttp"
)

// Error kinds stored in check results. They are the only failure detail ever exposed: raw
// error strings can contain resolved addresses and are dropped.
const (
	KindDNS              = "dns"
	KindConnect          = "connect"
	KindTLS              = "tls"
	KindTimeout          = "timeout"
	KindBlockedAddress   = "blocked_address"
	KindBadPort          = "bad_port"
	KindRedirect         = "redirect"
	KindUnexpectedStatus = "unexpected_status"
	KindOther            = "other"
)

// DefaultTimeout is the check timeout of every http monitor; it covers redirects.
const DefaultTimeout = 10 * time.Second

// Request describes one check. A TCP check has Host and Port and none of the HTTP fields.
type Request struct {
	// TCP selects a TCP check.
	TCP  bool
	Host string
	Port int

	URL string
	// Method defaults to GET.
	Method string
	// Header holds the monitor's custom headers; User-Agent may override ours. Headers refused
	// by DeniedHeader are dropped.
	Header http.Header
	// Body is sent as is; Content-Type defaults to application/json when Header has none.
	Body []byte
	// Timeout defaults to DefaultTimeout.
	Timeout time.Duration
	// ExpectedStatus is "2xx" (also when empty) or an exact code such as "204".
	ExpectedStatus string
}

// DeniedHeader reports whether a monitor may not set the header name: those that frame the
// message or the connection, which the HTTP client owns.
func DeniedHeader(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "trailer", "expect":
		return true
	}
	return strings.HasPrefix(n, "proxy-")
}

// Result is the outcome of one check.
type Result struct {
	OK         bool
	StatusCode int
	LatencyMs  int
	ErrorKind  string // empty when OK
	// TLSNotAfter is the leaf certificate expiry of the final hop, when a TLS handshake completed.
	TLSNotAfter *time.Time
	CheckedAt   time.Time
	// Cancelled means the caller's context was cancelled before the check finished (shutdown).
	// It is not a check outcome: OK is false and ErrorKind empty, the caller must discard the
	// result. The check's own deadline is a timeout, not a cancellation.
	Cancelled bool
}

// Prober runs one check. It is the seam the worker consumes; tests substitute it.
type Prober interface {
	Probe(ctx context.Context, req Request) Result
}

// HTTPSProber implements Prober with a guarded HTTP client.
type HTTPSProber struct {
	client *http.Client
	now    func() time.Time
}

// NewHTTPSProber returns a prober using client, which in production must come from
// safehttp.NewClient (with MaxRedirects 3).
func NewHTTPSProber(client *http.Client) *HTTPSProber {
	return &HTTPSProber{client: client, now: func() time.Time { return time.Now().UTC() }}
}

// Probe issues the request and reports the result. It never returns an error: failures are kinds.
func (p *HTTPSProber) Probe(parent context.Context, req Request) Result {
	res := Result{CheckedAt: p.now()}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	// Cancelling right after the headers abandons the body without reading it.
	defer cancel()

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(req.Body) > 0 {
		// A bytes.Reader gives the request a GetBody, so 307 and 308 can resend it.
		body = bytes.NewReader(req.Body)
	}
	custom := len(req.Header) > 0 || len(req.Body) > 0
	if custom {
		ctx = safehttp.WithSameHostRedirects(ctx)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		res.ErrorKind = KindOther
		return res
	}
	httpReq.Header.Set("User-Agent", "Proreta/1 (+uptime check)")
	for name, values := range req.Header {
		if DeniedHeader(name) {
			continue
		}
		httpReq.Header.Del(name)
		for _, v := range values {
			httpReq.Header.Add(name, v)
		}
	}
	if len(req.Body) > 0 && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	latency := time.Since(start)
	if err != nil {
		if errors.Is(parent.Err(), context.Canceled) {
			return Result{CheckedAt: res.CheckedAt, Cancelled: true}
		}
		res.ErrorKind = classify(ctx, err)
		return res
	}
	// The body is never read (no response content reaches users), so a close error changes nothing.
	_ = resp.Body.Close()
	cancel()

	res.LatencyMs = int(latency.Milliseconds())
	res.StatusCode = resp.StatusCode
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		na := resp.TLS.PeerCertificates[0].NotAfter.UTC()
		res.TLSNotAfter = &na
	}
	if statusMatches(req.ExpectedStatus, resp.StatusCode) {
		res.OK = true
	} else {
		res.ErrorKind = KindUnexpectedStatus
	}
	return res
}

func statusMatches(expected string, code int) bool {
	switch expected {
	case "", "2xx":
		return code >= 200 && code <= 299
	}
	want, err := strconv.Atoi(expected)
	return err == nil && want == code
}

func classify(ctx context.Context, err error) string {
	var (
		dnsErr      *net.DNSError
		netErr      net.Error
		opErr       *net.OpError
		recordErr   tls.RecordHeaderError
		alertErr    tls.AlertError
		verifyErr   *tls.CertificateVerificationError
		unknownAuth x509.UnknownAuthorityError
		invalidErr  x509.CertificateInvalidError
		hostErr     x509.HostnameError
	)
	switch {
	case errors.Is(err, safehttp.ErrBlockedAddress):
		return KindBlockedAddress
	case errors.Is(err, safehttp.ErrBadPort):
		return KindBadPort
	case errors.Is(err, safehttp.ErrTooManyRedirects), errors.Is(err, safehttp.ErrRedirectNotHTTPS),
		errors.Is(err, safehttp.ErrRedirectCrossHost):
		return KindRedirect
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return KindTimeout
	case errors.As(err, &dnsErr):
		return KindDNS
	case errors.As(err, &verifyErr), errors.As(err, &unknownAuth), errors.As(err, &invalidErr),
		errors.As(err, &hostErr), errors.As(err, &recordErr), errors.As(err, &alertErr):
		return KindTLS
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return KindConnect
	}
	return KindOther
}

// DialFunc opens a TCP connection: the guarded dialer of internal/safehttp in our own processes,
// the unguarded one only in the Specula executor.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// TCPProber implements Prober for TCP checks.
type TCPProber struct {
	dial DialFunc
	now  func() time.Time
}

// NewTCPProber returns a prober that connects with dial.
func NewTCPProber(dial DialFunc) *TCPProber {
	return &TCPProber{dial: dial, now: func() time.Time { return time.Now().UTC() }}
}

// Probe connects to req.Host:req.Port and closes the connection at once. The latency is the time
// to connect; failures are kinds, as for HTTP.
func (p *TCPProber) Probe(parent context.Context, req Request) Result {
	res := Result{CheckedAt: p.now()}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	start := time.Now()
	conn, err := p.dial(ctx, "tcp", net.JoinHostPort(req.Host, strconv.Itoa(req.Port)))
	latency := time.Since(start)
	if err != nil {
		if errors.Is(parent.Err(), context.Canceled) {
			return Result{CheckedAt: res.CheckedAt, Cancelled: true}
		}
		res.ErrorKind = classify(ctx, err)
		return res
	}
	_ = conn.Close()
	res.OK, res.LatencyMs = true, int(latency.Milliseconds())
	return res
}

// Mux sends TCP requests to TCP and everything else to HTTP.
type Mux struct{ HTTP, TCP Prober }

// Probe implements Prober.
func (m Mux) Probe(ctx context.Context, req Request) Result {
	if req.TCP {
		return m.TCP.Probe(ctx, req)
	}
	return m.HTTP.Probe(ctx, req)
}
