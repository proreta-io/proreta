package config

import (
	"fmt"
	"net"
	netmail "net/mail"
	"strings"
)

// SMTP TLS modes (PRORETA_SMTP_TLS).
const (
	SMTPStartTLS = "starttls"
	SMTPImplicit = "tls"
	SMTPPlain    = "none"
)

// SMTP is the outgoing mail configuration. Email is optional: with no host the feature is off
// and Enabled reports false, so every caller behaves as it did before email existed.
type SMTP struct {
	Host string
	Port int
	// Username and Password authenticate to the server (PLAIN, only over TLS or to localhost).
	// Both are empty for a relay that trusts the network. Only jobs loads them: the api only
	// queues mail and never connects.
	Username string
	Password Secret
	// From is the sender address (PRORETA_SMTP_FROM), "addr@example.com" or "Name <addr@example.com>".
	From string
	// FromAddress is the address part of From, used in the SMTP envelope.
	FromAddress string
	// TLS is starttls (default), tls (implicit TLS) or none (loopback hosts only).
	TLS string
}

// Enabled reports whether outgoing email is configured.
func (s SMTP) Enabled() bool { return s.Host != "" }

var smtpNames = []string{"PRORETA_SMTP_HOST", "PRORETA_SMTP_PORT", "PRORETA_SMTP_USERNAME", "PRORETA_SMTP_PASSWORD", "PRORETA_SMTP_FROM", "PRORETA_SMTP_TLS"}

// smtp reads the PRORETA_SMTP_* variables. credentials is true for the process that connects
// to the server (jobs); the api passes false and neither reads nor requires the login.
func (l *loader) smtp(credentials bool) SMTP {
	host := l.optional("PRORETA_SMTP_HOST")
	if host == "" {
		for _, n := range smtpNames[1:] {
			if l.optional(n) != "" {
				l.fail(n, "is set but PRORETA_SMTP_HOST is not; set the host to turn email on, or unset every PRORETA_SMTP_* variable")
			}
		}
		return SMTP{}
	}
	s := SMTP{Host: host, TLS: l.oneOf("PRORETA_SMTP_TLS", SMTPStartTLS, SMTPStartTLS, SMTPImplicit, SMTPPlain)}
	if len(host) > 253 || (strings.ContainsAny(host, " \t\r\n/@:[]") && net.ParseIP(strings.Trim(host, "[]")) == nil) {
		l.fail("PRORETA_SMTP_HOST", "must be a host name or IP address without a port")
	}
	defPort := map[string]int{SMTPStartTLS: 587, SMTPImplicit: 465, SMTPPlain: 25}[s.TLS]
	s.Port = l.integer("PRORETA_SMTP_PORT", defPort, 1, 65535)
	if s.TLS == SMTPPlain && !IsLoopbackHost(host) {
		l.fail("PRORETA_SMTP_TLS", "none is allowed only for localhost; use starttls or tls for a remote server")
	}
	raw := l.required("PRORETA_SMTP_FROM")
	if raw != "" {
		addr, err := netmail.ParseAddress(raw)
		if err != nil || strings.ContainsAny(raw, "\r\n") || !strings.Contains(addr.Address, "@") {
			l.fail("PRORETA_SMTP_FROM", "must be an address such as proreta@example.com or Proreta <proreta@example.com>")
		} else {
			s.From, s.FromAddress = raw, addr.Address
		}
	}
	if credentials {
		s.Username = l.optional("PRORETA_SMTP_USERNAME")
		pw, _ := l.lookup("PRORETA_SMTP_PASSWORD")
		s.Password = Secret(pw)
		switch {
		case s.Username == "" && s.Password != "":
			l.fail("PRORETA_SMTP_USERNAME", "and PRORETA_SMTP_PASSWORD must be set together")
		case s.Username != "" && s.Password == "":
			l.fail("PRORETA_SMTP_PASSWORD", "and PRORETA_SMTP_USERNAME must be set together")
		case strings.ContainsAny(s.Username, "\r\n\x00") || strings.ContainsAny(string(s.Password), "\r\n\x00"):
			l.fail("PRORETA_SMTP_USERNAME", "and the password must not contain control characters")
		case s.Username != "" && s.TLS == SMTPPlain:
			l.fail("PRORETA_SMTP_TLS", "a login is never sent without TLS; use starttls or tls")
		}
	}
	return s
}

// String describes the server without credentials, for logs.
func (s SMTP) String() string { return fmt.Sprintf("%s:%d (%s)", s.Host, s.Port, s.TLS) }

// Mailer is the configuration of `proretactl mail test`: only the SMTP settings, with the login,
// and the public URL when set.
type Mailer struct {
	SMTP      SMTP
	PublicURL string
}

// LoadMailer loads the SMTP settings for a direct send. PRORETA_SMTP_HOST is required here:
// the command exists to check them.
func LoadMailer(lookup LookupFunc) (Mailer, error) {
	l := &loader{lookup: lookup}
	m := Mailer{SMTP: l.smtp(true)}
	if !m.SMTP.Enabled() {
		l.fail("PRORETA_SMTP_HOST", "is required")
	}
	if l.optional("PRORETA_PUBLIC_URL") != "" {
		m.PublicURL = l.publicURL("PRORETA_PUBLIC_URL")
	}
	return m, l.err()
}
