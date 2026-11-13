// Package domains holds the rules for status pages on a customer's own domain: which hostnames
// are acceptable, the ownership token, the state machine of a domain and the DNS seam the
// verification uses. It has no database and no network access; the service and the job build on it.
package domains

import (
	"errors"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Reason says why a hostname was refused. It is safe to show to the caller: it never says who
// holds a host.
type Reason string

// Reasons a hostname is refused.
const (
	// ReasonInvalid: not a plain host name (scheme, port, path, spaces, underscores, empty or
	// over-long labels, a single label).
	ReasonInvalid Reason = "invalid"
	// ReasonNonASCII: Unicode characters; the xn-- form must be entered.
	ReasonNonASCII Reason = "non-ascii"
	// ReasonIP: an IP address in any notation.
	ReasonIP Reason = "ip-address"
	// ReasonWildcard: a wildcard host.
	ReasonWildcard Reason = "wildcard"
	// ReasonReserved: localhost, internal and other reserved names.
	ReasonReserved Reason = "reserved"
	// ReasonPublicSuffix: a public suffix such as com or co.uk.
	ReasonPublicSuffix Reason = "public-suffix"
	// ReasonRegistrable: the registrable domain itself (acme.com); a label above it is required.
	ReasonRegistrable Reason = "registrable-domain"
	// ReasonInstallation: a name of this installation or under an operator-forbidden suffix.
	ReasonInstallation Reason = "installation"
)

var reasonMessages = map[Reason]string{
	ReasonInvalid:      "enter only the host name, for example status.example.com",
	ReasonNonASCII:     "use the ASCII form of the name (xn--...) instead of Unicode characters",
	ReasonIP:           "an IP address cannot be used",
	ReasonWildcard:     "wildcards cannot be used",
	ReasonReserved:     "this name is reserved",
	ReasonPublicSuffix: "this is a public suffix; use a name below your own domain",
	ReasonRegistrable:  "use a subdomain such as status.example.com, not the domain itself",
	ReasonInstallation: "this name cannot be used on this installation",
}

// HostError is the error of a refused hostname.
type HostError struct{ Reason Reason }

func (e *HostError) Error() string { return "domains: " + reasonMessages[e.Reason] }

// Message is the short text for the user.
func (e *HostError) Message() string { return reasonMessages[e.Reason] }

// ErrUnavailable means a host cannot be taken now (another organization holds it, or it is not
// verified). It is one answer for every such reason, so it never says who holds a host.
var ErrUnavailable = errors.New("domains: host not available")

// ErrHost matches every *HostError with errors.Is.
var ErrHost = errors.New("domains: invalid host")

// Is makes errors.Is(err, ErrHost) true for a *HostError.
func (e *HostError) Is(target error) bool { return target == ErrHost }

const (
	maxHostLen  = 253
	maxLabelLen = 63
	minHostLen  = 4 // matches the CHECK on status_page_domains.host
)

// reservedSuffixes are names that are never a customer's public host. Each also matches itself.
var reservedSuffixes = []string{"localhost", "local", "localdomain", "internal", "lan", "home.arpa", "test", "invalid", "onion", "example", "arpa"}

// Rules are the installation-specific refusals on top of the fixed ones.
type Rules struct {
	installationHosts []string
	forbiddenSuffixes []string
}

// NewRules returns Rules that also refuse each of installationHosts and every name below one, and
// each of forbiddenSuffixes (PRORETA_STATUSPAGE_FORBIDDEN_SUFFIXES) and every name below one. Entries are
// lowercased and stripped of a trailing dot; empty entries are ignored.
func NewRules(installationHosts, forbiddenSuffixes []string) Rules {
	clean := func(in []string) []string {
		var out []string
		for _, s := range in {
			if s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "."); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return Rules{installationHosts: clean(installationHosts), forbiddenSuffixes: clean(forbiddenSuffixes)}
}

// Normalize lowercases raw and strips one trailing dot. It does not judge the name; use Validate.
func Normalize(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

// Validate normalizes raw and returns the host, or a *HostError. The rules: ASCII letters,
// digits and hyphens in labels of 1 to 63 characters, at most 253 characters, at least two labels;
// xn-- labels must be valid Punycode; no IP address, wildcard, reserved name, public suffix or
// registrable domain itself, and nothing the installation owns.
func (r Rules) Validate(raw string) (string, error) {
	host := Normalize(raw)
	if strings.Contains(host, "*") {
		return "", &HostError{ReasonWildcard}
	}
	for _, c := range []byte(host) {
		if c >= 0x80 {
			return "", &HostError{ReasonNonASCII}
		}
	}
	if len(host) < minHostLen || len(host) > maxHostLen || !validLabels(host) {
		return "", &HostError{ReasonInvalid}
	}
	if isIP(host) {
		return "", &HostError{ReasonIP}
	}
	for _, s := range reservedSuffixes {
		if below(host, s) {
			return "", &HostError{ReasonReserved}
		}
	}
	for _, s := range r.installationHosts {
		if below(host, s) {
			return "", &HostError{ReasonInstallation}
		}
	}
	for _, s := range r.forbiddenSuffixes {
		if below(host, s) {
			return "", &HostError{ReasonInstallation}
		}
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", &HostError{ReasonPublicSuffix}
	}
	if host == etld1 {
		return "", &HostError{ReasonRegistrable}
	}
	return host, nil
}

// Display returns the Unicode form of host for showing next to an xn-- name; host itself when it
// has no xn-- label.
func Display(host string) string {
	if !strings.Contains(host, "xn--") {
		return host
	}
	u, err := idna.Lookup.ToUnicode(host)
	if err != nil {
		return host
	}
	return u
}

// below reports whether host is name or a subdomain of it.
func below(host, name string) bool {
	return host == name || strings.HasSuffix(host, "."+name)
}

func validLabels(host string) bool {
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLen || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
		if strings.HasPrefix(l, "xn--") {
			if _, err := idna.Lookup.ToUnicode(l); err != nil || l == "xn--" {
				return false
			}
		}
	}
	return true
}

// isIP reports whether host is an IP address in any notation a resolver or browser would accept:
// dotted quad, IPv6, or a name whose last label is a decimal or 0x-hex number ("127.1",
// "2130706433", "0x7f.0.0.1"). A real top-level domain is never numeric.
func isIP(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	last := host[strings.LastIndexByte(host, '.')+1:]
	if strings.HasPrefix(last, "0x") {
		return true
	}
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			return false
		}
	}
	return last != ""
}
