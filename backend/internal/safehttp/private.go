package safehttp

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"time"
)

// NewPrivateClient returns a client for checks that run inside a customer's own network: it
// reaches any address, port and scheme (http or https), because the customer chose the targets
// and the executor runs on the customer's side. It still ignores proxy settings from the
// environment, keeps no connection between requests, follows at most MaxRedirects redirects over
// http or https, and applies the same-host rule to requests that carry custom headers or a body.
//
// It removes the SSRF guard, so it must never be used by a process that runs on our
// infrastructure: only cmd/specula builds one, and TestPrivateClientOnlyInSpecula enforces it.
func NewPrivateClient(opts Options) *http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxRedirects := min(max(opts.MaxRedirects, 0), maxRedirectsCeiling)
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.rootCAs},
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxResponseHeaderLen,
		ForceAttemptHTTP2:      false,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return ErrTooManyRedirects
			}
			if s := strings.ToLower(req.URL.Scheme); s != "https" && s != "http" {
				return ErrRedirectNotHTTPS
			}
			if req.Context().Value(sameHostKey{}) != nil && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return ErrRedirectCrossHost
			}
			return nil
		},
	}
}

// NewPrivateDialer is the TCP counterpart of NewPrivateClient: no address guard, any port. The
// same rule applies, only cmd/specula may build one (TestPrivateClientOnlyInSpecula).
func NewPrivateDialer(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return (&net.Dialer{Timeout: timeout}).DialContext
}
