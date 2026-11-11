package mail

import (
	"fmt"
	netmail "net/mail"
	"net/netip"
	"regexp"
	"strings"

	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// TLS modes of a connection.
const (
	// TLSStartTLS upgrades a plain connection with STARTTLS (usually port 587). Never downgraded.
	TLSStartTLS = "starttls"
	// TLSImplicit speaks TLS from the first byte (usually port 465).
	TLSImplicit = "tls"
	// TLSNone sends in the clear. Only for a loopback host, and never with a login.
	TLSNone = "none"
)

// Connection is how to reach the SMTP server: the settings the platform admin enters in the
// admin panel. It is stored in email_settings, with the password sealed.
type Connection struct {
	Host string
	Port int
	TLS  string
	// From is the sender, "addr@example.com" or "Name <addr@example.com>".
	From string
	// FromAddress is the address part of From, used in the SMTP envelope.
	FromAddress string
	// Username and Password authenticate with PLAIN, only over TLS. Both are empty for a relay
	// that trusts the network.
	Username string
	Password config.Secret
}

// DefaultPort is the usual port of a TLS mode.
func DefaultPort(tls string) int {
	switch tls {
	case TLSImplicit:
		return 465
	case TLSNone:
		return 25
	}
	return 587
}

// String describes the server without credentials, for logs.
func (c Connection) String() string { return fmt.Sprintf("%s:%d (%s)", c.Host, c.Port, c.TLS) }

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?$`)

// Validate checks every field and parses From into FromAddress. password is the password given
// with this change, or "" when the stored one is kept; the password rules that need the stored
// state (a username needs a password) are enforced by the database function.
func (c *Connection) Validate(password string) error {
	var v validation.Error
	c.Host = strings.TrimSpace(c.Host)
	c.Username = strings.TrimSpace(c.Username)
	switch {
	case c.Host == "":
		v.Add("host", "is required")
	case len(c.Host) > 253:
		v.Add("host", "must be at most 253 characters")
	default:
		if ip, err := netip.ParseAddr(strings.Trim(c.Host, "[]")); err == nil {
			c.Host = ip.Unmap().String()
			if BlockedAddress(ip) {
				v.Add("host", "this address is never used for mail: link-local, cloud metadata and unspecified addresses are refused")
			}
		} else if !validHostname(c.Host) {
			v.Add("host", "must be a host name or an IP address, without a scheme or a port")
		}
	}
	switch c.TLS {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		v.Add("tls", "must be starttls, tls or none")
	}
	if c.Port < 1 || c.Port > 65535 {
		v.Add("port", "must be between 1 and 65535")
	}
	if c.TLS == TLSNone && c.Host != "" && !config.IsLoopbackHost(strings.ToLower(strings.Trim(c.Host, "[]"))) {
		v.Add("tls", "none is allowed only for localhost; use starttls or tls for a remote server")
	}
	if c.TLS == TLSNone && c.Username != "" {
		v.Add("tls", "a login is never sent without TLS; use starttls or tls")
	}
	from := strings.TrimSpace(c.From)
	if from == "" {
		v.Add("from", "is required")
	} else if addr, err := netmail.ParseAddress(from); err != nil || strings.ContainsAny(from, "\r\n\x00") || !strings.Contains(addr.Address, "@") || len(from) > 320 {
		v.Add("from", "must be an address such as proreta@example.com or Proreta <proreta@example.com>")
	} else {
		c.From, c.FromAddress = from, addr.Address
	}
	if strings.ContainsAny(c.Username, "\r\n\x00") || len(c.Username) > 256 {
		v.Add("username", "must be at most 256 characters without control characters")
	}
	if strings.ContainsAny(password, "\r\n\x00") || len(password) > 512 {
		v.Add("password", "must be at most 512 characters without control characters")
	}
	if c.Username == "" && password != "" {
		v.Add("username", "is required with a password")
	}
	return v.Err()
}

func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}
