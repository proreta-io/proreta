package api_test

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

var resetLink = regexp.MustCompile(`https://proreta\.example\.com/reset-password#([A-Za-z0-9_-]{43})`)

const resetNewPassword = "a brand new passphrase"

// TestIntegrationPasswordResetEndToEnd asks for a reset over HTTP, delivers the mail to an
// in-process SMTP server, sets a new password with the emailed token and checks that the old
// session is gone, the old password stops working and the new one logs in.
func TestIntegrationPasswordResetEndToEnd(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apireset"))
	logs := &lockedBuffer{}
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	s := newRegistrationServer(t, env, auth.Options{Mail: mail.NewQueue(keys, nil)}, true, logs)
	srv, deliverer := startMail(t, env, keys, logs)
	cookie, _, _ := s.register("ada@example.com")
	xff := map[string]string{"X-Forwarded-For": "198.51.100.40"}

	// The answer is the same for a known and an unknown address.
	known := s.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"ada@example.com"}`, xff)
	unknown := s.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"nobody@example.com"}`, xff)
	if known.code != http.StatusAccepted || unknown.code != known.code || known.raw != unknown.raw {
		t.Fatalf("known %d %q, unknown %d %q", known.code, known.raw, unknown.code, unknown.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"not an address"}`, xff); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed: %d %s", r.code, r.raw)
	}

	// The registration verification mail and the reset mail.
	if n := deliverer.RunOnce(context.Background()); n != 2 {
		t.Fatalf("delivered %d", n)
	}
	var token string
	for _, m := range srv.Messages() {
		text, html, err := m.Bodies()
		if err != nil {
			t.Fatal(err)
		}
		if g := resetLink.FindStringSubmatch(text); g != nil {
			token = g[1]
			if !strings.Contains(html, g[0]) {
				t.Fatal("the HTML part lacks the link")
			}
		}
	}
	if token == "" || len(srv.Messages()) != 2 {
		t.Fatalf("no reset link among %d messages", len(srv.Messages()))
	}

	if r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+token+`","password":"short"}`, xff); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("short password: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+strings.Repeat("A", 43)+`","password":"`+resetNewPassword+`"}`, xff); r.code != http.StatusNotFound {
		t.Fatalf("unknown token: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+token+`","password":"`+resetNewPassword+`"}`, xff); r.code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+token+`","password":"`+resetNewPassword+`x"}`, xff); r.code != http.StatusNotFound {
		t.Fatalf("reused token: %d %s", r.code, r.raw)
	}
	if me := s.do(http.MethodGet, "/api/v1/auth/me", "", cookie); me.code != http.StatusUnauthorized {
		t.Fatalf("the old session still works: %d", me.code)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/login", `{"login":"ada@example.com","password":"`+ownerPassword+`"}`, xff); r.code != http.StatusUnauthorized {
		t.Fatalf("old password: %d", r.code)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/login", `{"login":"ada@example.com","password":"`+resetNewPassword+`"}`, xff); r.code != http.StatusOK {
		t.Fatalf("new password: %d %s", r.code, r.raw)
	}

	for _, secret := range []string{token, "ada@example.com", resetNewPassword, ownerPassword} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

func TestIntegrationPasswordResetWithoutEmail(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiresetoff"))
	s := newRegistrationServer(t, env, auth.Options{}, true, &lockedBuffer{})
	s.register("ada@example.com")
	if r := s.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"ada@example.com"}`, nil); r.code != http.StatusNotFound {
		t.Fatalf("forgot-password: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+strings.Repeat("A", 43)+`","password":"`+resetNewPassword+`"}`, nil); r.code != http.StatusNotFound {
		t.Fatalf("reset-password: %d %s", r.code, r.raw)
	}
	admin := env.AdminConn(t)
	var n int
	if err := admin.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM mail_outbox) + (SELECT count(*) FROM password_resets)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows without email: %d %v", n, err)
	}
}
