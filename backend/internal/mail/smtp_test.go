package mail_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/config"
	"github.com/proreta-io/proreta/backend/internal/testsupport/smtptest"
)

func sender(srv *smtptest.Server, mode smtptest.Mode, user, pass string) *mail.SMTPSender {
	s := mail.NewSMTPSender(mail.Connection{
		Host: srv.Host(), Port: srv.Port(), TLS: string(mode), From: "Proreta <proreta@example.com>", FromAddress: "proreta@example.com",
		Username: user, Password: config.Secret(pass),
	})
	s.TLSConfig = &tls.Config{RootCAs: srv.Roots, MinVersion: tls.VersionTLS12}
	s.Timeout = 5 * time.Second
	s.Now = func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }
	return s
}

func testMessage() mail.Message {
	m, err := mail.Render(mail.KindTest, "https://proreta.example.com", "alice@example.org", nil)
	if err != nil {
		panic(err)
	}
	return m
}

func TestSendModes(t *testing.T) {
	for _, mode := range []smtptest.Mode{smtptest.StartTLS, smtptest.Implicit, smtptest.Plain} {
		t.Run(string(mode), func(t *testing.T) {
			srv := smtptest.New(t, smtptest.Options{Mode: mode, User: "u", Pass: "p4ss"})
			if err := sender(srv, mode, "u", "p4ss").Send(context.Background(), testMessage()); err != nil {
				t.Fatalf("send: %v", err)
			}
			got := srv.Messages()
			if len(got) != 1 {
				t.Fatalf("%d messages", len(got))
			}
			m := got[0]
			if m.From != "proreta@example.com" || len(m.To) != 1 || m.To[0] != "alice@example.org" || !m.Authed || m.TLS != (mode != smtptest.Plain) {
				t.Fatalf("envelope = %+v", m)
			}
		})
	}
}

func TestMessageFormat(t *testing.T) {
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.StartTLS})
	msg := testMessage()
	msg.Subject = "Héllo – Proreta"
	msg.Text = "Line one\n\n.dot line\nünïcode"
	if err := sender(srv, smtptest.StartTLS, "", "").Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(srv.Messages()[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	h := parsed.Header
	dec := new(mime.WordDecoder)
	subj, _ := dec.DecodeHeader(h.Get("Subject"))
	if subj != "Héllo – Proreta" || h.Get("From") != "Proreta <proreta@example.com>" || h.Get("To") != "alice@example.org" ||
		h.Get("Auto-Submitted") != "auto-generated" || !strings.HasSuffix(h.Get("Message-Id"), "@example.com>") || h.Get("Date") == "" {
		t.Fatalf("headers: %v", h)
	}
	mt, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content type %q %v", mt, err)
	}
	mr := multipart.NewReader(parsed.Body, params["boundary"])
	var types []string
	var text, html string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(quotedprintable.NewReader(p))
		types = append(types, p.Header.Get("Content-Type"))
		if strings.HasPrefix(p.Header.Get("Content-Type"), "text/plain") {
			text = string(body)
		} else {
			html = string(body)
		}
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Fatalf("parts: %v", types)
	}
	if !strings.Contains(text, "\r\n.dot line\r\nünïcode") {
		t.Fatalf("text part mangled: %q", text)
	}
	for _, bad := range []string{"<img", "http://", "https://", "<script"} {
		if strings.Contains(html, bad) {
			t.Errorf("html part contains %q", bad)
		}
	}
}

func TestSendRefusesInjection(t *testing.T) {
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.Plain})
	s := sender(srv, smtptest.Plain, "", "")
	for name, mut := range map[string]func(*mail.Message){
		"newline in recipient": func(m *mail.Message) { m.To = "a@example.org\r\nBcc: evil@example.net" },
		"newline in subject":   func(m *mail.Message) { m.Subject = "hi\r\nBcc: evil@example.net" },
		"display name":         func(m *mail.Message) { m.To = "Alice <a@example.org>" },
		"two recipients":       func(m *mail.Message) { m.To = "a@example.org,b@example.org" },
		"empty":                func(m *mail.Message) { m.To = "" },
	} {
		m := testMessage()
		mut(&m)
		err := s.Send(context.Background(), m)
		var se *mail.SendError
		if !errors.As(err, &se) || !se.Permanent || se.Kind != mail.KindBadMessage {
			t.Errorf("%s: err = %v, want a permanent bad_message", name, err)
		}
	}
	if srv.Connections() != 0 {
		t.Fatal("a refused message opened a connection")
	}
}

func TestSendFailures(t *testing.T) {
	type want struct {
		kind      string
		code      int
		permanent bool
	}
	cases := map[string]struct {
		opts   smtptest.Options
		user   string
		pass   string
		script [][2]string
		want   want
	}{
		"recipient rejected": {smtptest.Options{Mode: smtptest.StartTLS}, "", "", [][2]string{{"RCPT", "550 5.1.1 no such user alice@example.org"}}, want{mail.KindRecipient, 550, true}},
		"recipient deferred": {smtptest.Options{Mode: smtptest.StartTLS}, "", "", [][2]string{{"RCPT", "451 4.7.1 greylisted"}}, want{mail.KindRecipient, 451, false}},
		"data rejected":      {smtptest.Options{Mode: smtptest.StartTLS}, "", "", [][2]string{{"DATAEND", "554 5.7.1 spam"}}, want{mail.KindRejected, 554, true}},
		"sender rejected":    {smtptest.Options{Mode: smtptest.StartTLS}, "", "", [][2]string{{"MAIL", "550 5.7.1 relay denied"}}, want{mail.KindRejected, 550, true}},
		"wrong password":     {smtptest.Options{Mode: smtptest.StartTLS, User: "u", Pass: "right"}, "u", "wrong", nil, want{mail.KindAuth, 535, false}},
		"no login offered":   {smtptest.Options{Mode: smtptest.StartTLS}, "u", "p", nil, want{mail.KindAuth, 0, false}},
		"downgrade refused":  {smtptest.Options{Mode: smtptest.StartTLS, NoSTARTTLS: true}, "", "", nil, want{mail.KindTLS, 0, false}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := smtptest.New(t, c.opts)
			for _, s := range c.script {
				if s[0] == "DATAEND" {
					srv.Reply("DATAEND", s[1])
				} else {
					srv.Reply(s[0], s[1])
				}
			}
			err := sender(srv, smtptest.StartTLS, c.user, c.pass).Send(context.Background(), testMessage())
			var se *mail.SendError
			if !errors.As(err, &se) || se.Kind != c.want.kind || se.Code != c.want.code || se.Permanent != c.want.permanent {
				t.Fatalf("err = %#v, want %+v", err, c.want)
			}
			if strings.Contains(err.Error(), "alice@") || strings.Contains(err.Error(), "greylisted") {
				t.Fatalf("error text carries server text: %v", err)
			}
			if len(srv.Messages()) != 0 {
				t.Fatal("a failed send delivered a message")
			}
		})
	}
}

func TestSendNeverSendsLoginWithoutTLS(t *testing.T) {
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.StartTLS, User: "u", Pass: "p", NoSTARTTLS: true})
	err := sender(srv, smtptest.StartTLS, "u", "p").Send(context.Background(), testMessage())
	if err == nil || mail.ErrorKind(err) != mail.KindTLS {
		t.Fatalf("err = %v", err)
	}
	for _, c := range srv.Commands() {
		if c == "AUTH" {
			t.Fatal("login sent without TLS")
		}
	}
}

func TestSendUntrustedCertificate(t *testing.T) {
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.StartTLS})
	s := sender(srv, smtptest.StartTLS, "", "")
	s.TLSConfig = nil // system roots do not trust the throwaway certificate
	if err := s.Send(context.Background(), testMessage()); mail.ErrorKind(err) != mail.KindTLS {
		t.Fatalf("err = %v", err)
	}
}

func TestSendTimeoutAndCancel(t *testing.T) {
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.Plain})
	s := sender(srv, smtptest.Plain, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, testMessage()); mail.ErrorKind(err) != mail.KindTimeout {
		t.Fatalf("cancelled: %v", err)
	}
	if mail.IsPermanent(errors.New("x")) || mail.ErrorKind(errors.New("x")) != mail.KindOther {
		t.Fatal("non-SendError classification")
	}
}
