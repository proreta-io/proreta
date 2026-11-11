package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sendTimeout bounds one whole delivery (connect, TLS, login and transfer).
const sendTimeout = 30 * time.Second

// dialTimeout bounds the TCP connect (and the TLS handshake of implicit TLS is bounded by the
// whole-send timeout).
const dialTimeout = 10 * time.Second

// SMTPSender delivers a message to one SMTP server, one connection per message. It never
// downgrades: with starttls a server that does not offer STARTTLS is an error, and the login is
// only sent over TLS (or to a loopback server).
type SMTPSender struct {
	cfg Connection
	// Timeout overrides sendTimeout.
	Timeout time.Duration
	// TLSConfig, when set, replaces the default TLS settings (server name is always the host).
	// Tests use it to trust a throwaway certificate.
	TLSConfig *tls.Config
	// Now supplies the Date header; nil means time.Now.
	Now func() time.Time
}

// NewSMTPSender returns a sender for cfg, which must be valid (Connection.Validate).
func NewSMTPSender(cfg Connection) *SMTPSender { return &SMTPSender{cfg: cfg} }

// Send delivers m. Failures are *SendError.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	body, err := s.build(m)
	if err != nil {
		return &SendError{Kind: KindBadMessage, Permanent: true}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = sendTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := s.dial(ctx)
	if err != nil {
		return classify(ctx, KindConnect, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Closing the connection is the only way to interrupt net/smtp when the context ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return classify(ctx, KindConnect, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello(domainOf(s.cfg.FromAddress)); err != nil {
		return classify(ctx, KindConnect, err)
	}
	if s.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return &SendError{Kind: KindTLS}
		}
		if err := c.StartTLS(s.tlsConfig()); err != nil {
			return classify(ctx, KindTLS, err)
		}
	}
	if s.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return &SendError{Kind: KindAuth}
		}
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password.Value(), s.cfg.Host)); err != nil {
			return classify(ctx, KindAuth, err)
		}
	}
	if err := c.Mail(s.cfg.FromAddress); err != nil {
		return classify(ctx, KindRejected, err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return classify(ctx, KindRecipient, err)
	}
	w, err := c.Data()
	if err != nil {
		return classify(ctx, KindRejected, err)
	}
	if _, err := w.Write(body); err != nil {
		return classify(ctx, KindConnect, err)
	}
	if err := w.Close(); err != nil {
		return classify(ctx, KindRejected, err)
	}
	_ = c.Quit()
	return nil
}

func (s *SMTPSender) tlsConfig() *tls.Config {
	var cfg *tls.Config
	if s.TLSConfig != nil {
		cfg = s.TLSConfig.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.ServerName = s.cfg.Host
	return cfg
}

func (s *SMTPSender) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	d := net.Dialer{Timeout: dialTimeout, Control: guardDial}
	if s.cfg.TLS == TLSImplicit {
		td := tls.Dialer{NetDialer: &d, Config: s.tlsConfig()}
		return td.DialContext(ctx, "tcp", addr)
	}
	return d.DialContext(ctx, "tcp", addr)
}

// classify maps an error to a SendError without keeping its text. A 5xx reply to the
// recipient, sender or data is permanent; everything else may be fixed by waiting.
func classify(ctx context.Context, kind string, err error) *SendError {
	if ctx.Err() != nil || isTimeout(err) {
		return &SendError{Kind: KindTimeout}
	}
	if kind == KindConnect {
		var dns *net.DNSError
		var cve *tls.CertificateVerificationError
		var rhe tls.RecordHeaderError
		var alert tls.AlertError
		switch {
		case errors.Is(err, errBlockedAddress):
			return &SendError{Kind: KindBlocked}
		case errors.Is(err, syscall.ECONNREFUSED):
			return &SendError{Kind: KindRefused}
		case errors.As(err, &dns):
			return &SendError{Kind: KindDNS}
		case errors.As(err, &cve), errors.As(err, &rhe), errors.As(err, &alert):
			return &SendError{Kind: KindTLS}
		}
	}
	var tp *textproto.Error
	if errors.As(err, &tp) {
		se := &SendError{Kind: kind, Code: tp.Code}
		se.Permanent = tp.Code >= 500 && (kind == KindRecipient || kind == KindRejected)
		return se
	}
	return &SendError{Kind: kind}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// build assembles the RFC 5322 message: multipart/alternative with a text and an HTML part,
// quoted-printable, UTF-8, a random Message-ID on the sender's domain. No tracking pixels, no
// remote content. Header values are checked for line breaks first.
func (s *SMTPSender) build(m Message) ([]byte, error) {
	to, err := parseBare(m.To)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errors.New("subject has a line break")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("message id: %w", err)
	}
	boundary := "proreta-" + hex.EncodeToString(id[:8])
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	from := s.cfg.From
	if strings.ContainsAny(from, "\r\n") {
		return nil, errors.New("from has a line break")
	}

	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from)
	hdr("To", to)
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	hdr("Date", now().UTC().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(id)+"@"+domainOf(s.cfg.FromAddress)+">")
	hdr("MIME-Version", "1.0")
	hdr("Auto-Submitted", "auto-generated")
	hdr("X-Auto-Response-Suppress", "All")
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		hdr("Content-Type", part.typ+"; charset=utf-8")
		hdr("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		w := quotedprintable.NewWriter(&b)
		if _, err := w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(part.body, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}
