package mail_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/mail"
)

func TestBlockedAddress(t *testing.T) {
	for ip, want := range map[string]bool{
		"169.254.169.254": true, "169.254.0.1": true, "fe80::1": true, "fd00:ec2::254": true, "168.63.129.16": true, "100.100.100.200": true,
		"0.0.0.0": true, "::": true, "0.1.2.3": true, "224.0.0.1": true, "255.255.255.255": true, "::ffff:169.254.169.254": true,
		// Relays on private networks and loopback are fine.
		"10.0.0.5": false, "192.168.1.10": false, "172.16.0.1": false, "fd12:3456::1": false, "127.0.0.1": false, "::1": false,
		"203.0.113.7": false, "100.64.0.1": false,
	} {
		if got := mail.BlockedAddress(netip.MustParseAddr(ip)); got != want {
			t.Errorf("BlockedAddress(%s) = %t, want %t", ip, got, want)
		}
	}
}

func TestConnectionValidate(t *testing.T) {
	ok := mail.Connection{Host: "smtp.example.com", Port: 587, TLS: mail.TLSStartTLS, From: "Proreta <proreta@example.com>", Username: "u"}
	c := ok
	if err := c.Validate("pw"); err != nil || c.FromAddress != "proreta@example.com" {
		t.Fatalf("valid: %v %+v", err, c)
	}
	for name, mod := range map[string]func(*mail.Connection){
		"empty host":      func(c *mail.Connection) { c.Host = "" },
		"host with port":  func(c *mail.Connection) { c.Host = "smtp.example.com:587" },
		"host with url":   func(c *mail.Connection) { c.Host = "https://smtp.example.com" },
		"host with space": func(c *mail.Connection) { c.Host = "smtp example.com" },
		"metadata":        func(c *mail.Connection) { c.Host = "169.254.169.254" },
		"unspecified":     func(c *mail.Connection) { c.Host = "0.0.0.0" },
		"ipv6 link-local": func(c *mail.Connection) { c.Host = "fe80::1" },
		"port 0":          func(c *mail.Connection) { c.Port = 0 },
		"tls unknown":     func(c *mail.Connection) { c.TLS = "ssl" },
		"none remote":     func(c *mail.Connection) { c.TLS = mail.TLSNone; c.Username = "" },
		"none with login": func(c *mail.Connection) { c.TLS = mail.TLSNone; c.Host = "localhost" },
		"from not mail":   func(c *mail.Connection) { c.From = "nobody" },
		"from newline":    func(c *mail.Connection) { c.From = "a@example.com\r\nBcc: x@example.com" },
	} {
		c := ok
		mod(&c)
		if err := c.Validate("pw"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Private relays and loopback are fine.
	for _, host := range []string{"10.1.2.3", "relay.internal", "mailhog", "192.168.0.2"} {
		c := ok
		c.Host = host
		if err := c.Validate("pw"); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	c = ok
	c.Host, c.TLS, c.Username = "127.0.0.1", mail.TLSNone, ""
	if err := c.Validate(""); err != nil {
		t.Errorf("plain loopback: %v", err)
	}
	c = ok
	c.Username = ""
	if err := c.Validate("pw"); err == nil {
		t.Error("a password without a username")
	}
}

// The dial refuses a blocked address even when it comes from name resolution; here a literal.
func TestSendRefusesBlockedAddress(t *testing.T) {
	s := mail.NewSMTPSender(mail.Connection{Host: "169.254.169.254", Port: 25, TLS: mail.TLSStartTLS, From: "a@example.com", FromAddress: "a@example.com"})
	s.Timeout = 3 * time.Second
	err := s.Send(context.Background(), testMessage())
	if got := mail.ErrorKind(err); got != "blocked_address" {
		t.Fatalf("err kind = %q (%v)", got, err)
	}
}

func TestSendClassifiesRefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	s := mail.NewSMTPSender(mail.Connection{Host: "127.0.0.1", Port: port, TLS: mail.TLSNone, From: "a@example.com", FromAddress: "a@example.com"})
	s.Timeout = 3 * time.Second
	if got := mail.ErrorKind(s.Send(context.Background(), testMessage())); got != "refused" {
		t.Fatalf("kind = %q", got)
	}
}
