// Package checks runs monitor checks. There is one kind: an HTTPS request through the SSRF
// guard (internal/safehttp) that records status, latency to response headers and the leaf
// certificate expiry, and never reads the response body.
package checks

import (
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

// Request describes one check.
type Request struct {
	URL string
	// Timeout defaults to DefaultTimeout.
	Timeout time.Duration
	// ExpectedStatus is "2xx" (also when empty) or an exact code such as "204".
	ExpectedStatus string
	// Method defaults to GET. Body is sent with ContentType and must never be logged or returned.
	Method      string
	Body        string
	ContentType string
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
// safehttp.NewClient (with MaxRedirects 3). The client is copied, not modified.
func NewHTTPSProber(client *http.Client) *HTTPSProber {
	c := *client
	inner := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := checkReplay(req, via); err != nil {
			return err
		}
		if inner != nil {
			return inner(req, via)
		}
		return nil
	}
	return &HTTPSProber{client: &c, now: func() time.Time { return time.Now().UTC() }}
}

// checkReplay refuses a redirect that would send a request other than GET or HEAD to another
// host. The client turns a 301, 302 or 303 into a GET without a body, which is harmless, but it
// replays the method and body on a 307 or 308, and a body (or a state-changing method) must not
// follow a redirect to a host the user did not enter.
func checkReplay(req *http.Request, via []*http.Request) error {
	if req.Method == http.MethodGet || req.Method == http.MethodHead || len(via) == 0 {
		return nil
	}
	if !strings.EqualFold(req.URL.Hostname(), via[len(via)-1].URL.Hostname()) {
		return safehttp.ErrRedirectReplay
	}
	return nil
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
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		res.ErrorKind = KindOther
		return res
	}
	if req.Body != "" && req.ContentType != "" {
		httpReq.Header.Set("Content-Type", req.ContentType)
	}
	httpReq.Header.Set("User-Agent", "Proreta/1 (+uptime check)")

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
		errors.Is(err, safehttp.ErrRedirectReplay):
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
