package domains

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// ErrAnswerTooLarge: a lookup returned more records than the cap. It is an error that is not
// ErrNotFound, so the check counts it as unknown: a truncated answer must never make a record
// look missing.
var ErrAnswerTooLarge = errors.New("domains: too many records in the answer")

// Defaults of NetResolverConfig.
const (
	DefaultDNSTimeout = 3 * time.Second
	// DefaultMaxAnswers is generous for one TXT name; a real zone holds a handful.
	DefaultMaxAnswers = 16
	// DefaultMaxTXTLength is far above the 47 characters of a proof value.
	DefaultMaxTXTLength = 512
)

// NetResolverConfig configures NetResolver. The zero value is the system resolver with the
// defaults above.
type NetResolverConfig struct {
	// Server pins a recursive resolver, "ip" or "ip:port" (port 53 by default). Empty uses the
	// system's.
	Server string
	// Dial replaces the dialer for DNS traffic (tests point it at a local server); nil is a net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Timeout bounds one lookup.
	Timeout time.Duration
	// MaxAnswers caps the TXT strings of one answer; MaxTXTLength drops longer strings (they cannot
	// be a proof value).
	MaxAnswers, MaxTXTLength int
}

// NetResolver is the production DomainResolver, on net.Resolver. It only asks DNS and never
// connects to a customer's host. Only "the record does not exist" is ErrNotFound; timeouts,
// SERVFAIL and everything else are other errors, which the check treats as unknown.
//
// shortcut: the CNAME chain is followed by the recursive resolver, so its depth cap is that
// server's, not ours; net.Resolver does not expose the hops. Move to a raw DNS client if a hop
// cap of our own is needed.
type NetResolver struct {
	r                        *net.Resolver
	timeout                  time.Duration
	maxAnswers, maxTXTLength int
}

// NewNetResolver returns a NetResolver for c.
func NewNetResolver(c NetResolverConfig) (*NetResolver, error) {
	if c.Timeout <= 0 {
		c.Timeout = DefaultDNSTimeout
	}
	if c.MaxAnswers <= 0 {
		c.MaxAnswers = DefaultMaxAnswers
	}
	if c.MaxTXTLength <= 0 {
		c.MaxTXTLength = DefaultMaxTXTLength
	}
	r := &net.Resolver{}
	if c.Server != "" || c.Dial != nil {
		server := c.Server
		if server != "" {
			if _, _, err := net.SplitHostPort(server); err != nil {
				server = net.JoinHostPort(strings.Trim(server, "[]"), "53")
			}
		}
		dial := c.Dial
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		r.PreferGo = true
		r.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			if server != "" {
				address = server
			}
			return dial(ctx, network, address)
		}
	}
	return &NetResolver{r: r, timeout: c.Timeout, maxAnswers: c.MaxAnswers, maxTXTLength: c.MaxTXTLength}, nil
}

// absolute makes name fully qualified. Without the trailing dot Go tries the system's search
// domains after an error, and an NXDOMAIN for "status.acme.com.cluster.local" would hide a SERVFAIL
// for the real name as "no such record".
func absolute(name string) string { return strings.TrimSuffix(name, ".") + "." }

func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// LookupTXT implements DomainResolver.
func (n *NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	txt, err := n.r.LookupTXT(ctx, absolute(name))
	switch {
	case notFound(err):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("domains: txt lookup: %w", err)
	case len(txt) > n.maxAnswers:
		return nil, ErrAnswerTooLarge
	}
	out := txt[:0]
	for _, s := range txt {
		if len(s) <= n.maxTXTLength {
			out = append(out, s)
		}
	}
	return out, nil
}

// LookupCNAME implements DomainResolver. net.Resolver answers with the name itself when it has no
// alias, which is ErrNotFound here.
func (n *NetResolver) LookupCNAME(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	target, err := n.r.LookupCNAME(ctx, absolute(name))
	switch {
	case notFound(err):
		return "", ErrNotFound
	case err != nil:
		return "", fmt.Errorf("domains: cname lookup: %w", err)
	}
	target = Normalize(target)
	if target == "" || target == Normalize(name) {
		return "", ErrNotFound
	}
	return target, nil
}
