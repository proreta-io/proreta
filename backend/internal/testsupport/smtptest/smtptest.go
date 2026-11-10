// Package smtptest is an in-process SMTP server for tests: it speaks just enough of the protocol
// (EHLO, STARTTLS or implicit TLS, AUTH PLAIN, MAIL, RCPT, DATA, QUIT) to receive messages from
// net/smtp, records them, and can be scripted to answer a command with an error.
package smtptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Mode is how the server secures the connection.
type Mode string

// Modes.
const (
	Plain    Mode = "none"
	StartTLS Mode = "starttls"
	Implicit Mode = "tls"
)

// Options configure a Server.
type Options struct {
	Mode Mode
	// User and Pass, when set, make AUTH PLAIN mandatory before MAIL.
	User, Pass string
	// NoSTARTTLS stops a starttls server from advertising STARTTLS (a downgrade attempt).
	NoSTARTTLS bool
}

// Message is one received message.
type Message struct {
	From   string
	To     []string
	Data   string
	TLS    bool
	Authed bool
}

// Server is a running fake SMTP server.
type Server struct {
	opts Options
	ln   net.Listener
	cert tls.Certificate
	// Roots trusts the server's throwaway certificate.
	Roots *x509.CertPool

	mu       sync.Mutex
	messages []Message
	script   map[string][]string
	commands []string
	conns    int
}

// New starts a server on 127.0.0.1 and stops it when the test ends.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	if opts.Mode == "" {
		opts.Mode = Plain
	}
	cert, roots := selfSigned(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode == Implicit {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	s := &Server{opts: opts, ln: ln, cert: cert, Roots: roots, script: map[string][]string{}}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// Host is "localhost": the certificate and the loopback-only settings accept it.
func (s *Server) Host() string { return "localhost" }

// Port is the listening port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Messages returns the messages received so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Commands returns the command verbs received so far, in order (AUTH arguments are not kept).
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Connections is how many connections were accepted.
func (s *Server) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// Reply scripts the answers (full reply lines such as "550 no such user") to the next uses of
// a command ("MAIL", "RCPT", "DATA", "AUTH", "EHLO", "STARTTLS"), one per use.
func (s *Server) Reply(command string, lines ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script[command] = append(s.script[command], lines...)
}

func (s *Server) scripted(cmd string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.script[cmd]
	if len(q) == 0 {
		return "", false
	}
	s.script[cmd] = q[1:]
	return q[0], true
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	_, isTLS := c.(*tls.Conn)
	r := textproto.NewReader(bufio.NewReader(c))
	w := func(line string) { _, _ = fmt.Fprintf(c, "%s\r\n", line) }
	w("220 localhost fake smtp")
	var from string
	var to []string
	authed := false
	for {
		line, err := r.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		s.mu.Lock()
		s.commands = append(s.commands, verb)
		s.mu.Unlock()
		if reply, ok := s.scripted(verb); ok {
			w(reply)
			if strings.HasPrefix(reply, "4") || strings.HasPrefix(reply, "5") {
				continue
			}
		}
		switch verb {
		case "EHLO", "HELO":
			w("250-localhost")
			if s.opts.Mode == StartTLS && !isTLS && !s.opts.NoSTARTTLS {
				w("250-STARTTLS")
			}
			if s.opts.User != "" {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case "STARTTLS":
			w("220 ready")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, isTLS = tc, true
			r = textproto.NewReader(bufio.NewReader(c))
			w = func(line string) { _, _ = fmt.Fprintf(c, "%s\r\n", line) }
		case "AUTH":
			mech, rest, _ := strings.Cut(arg, " ")
			raw, _ := base64.StdEncoding.DecodeString(rest)
			parts := strings.Split(string(raw), "\x00")
			if !strings.EqualFold(mech, "PLAIN") || len(parts) != 3 || parts[1] != s.opts.User || parts[2] != s.opts.Pass {
				w("535 5.7.8 authentication failed")
				continue
			}
			authed = true
			w("235 2.7.0 ok")
		case "MAIL":
			if s.opts.User != "" && !authed {
				w("530 5.7.0 authentication required")
				continue
			}
			from = angle(arg)
			to = nil
			w("250 ok")
		case "RCPT":
			to = append(to, angle(arg))
			w("250 ok")
		case "DATA":
			w("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadLine()
				if err != nil {
					return
				}
				if l == "." {
					break
				}
				l = strings.TrimPrefix(l, ".")
				b.WriteString(l + "\r\n")
			}
			if reply, ok := s.scripted("DATAEND"); ok {
				w(reply)
				continue
			}
			s.mu.Lock()
			s.messages = append(s.messages, Message{From: from, To: to, Data: b.String(), TLS: isTLS, Authed: authed})
			s.mu.Unlock()
			w("250 2.0.0 queued")
		case "RSET", "NOOP":
			w("250 ok")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("502 command not implemented " + strconv.Quote(verb))
		}
	}
}

func selfSigned(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// angle returns the text between < and > of "FROM:<a@example.com> SIZE=1".
func angle(arg string) string {
	_, rest, _ := strings.Cut(arg, "<")
	addr, _, _ := strings.Cut(rest, ">")
	return addr
}
