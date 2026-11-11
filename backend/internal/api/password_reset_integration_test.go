package api_test

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
	s := newRegistrationServer(t, env, auth.Options{Mail: mail.NewQueue(keys, mail.AllFeatures, nil)}, true, logs)
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

type switchableFeatures struct {
	mu sync.Mutex
	f  mail.Features
}

func (s *switchableFeatures) Features(context.Context) mail.Features {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f
}

func (s *switchableFeatures) set(f mail.Features) { s.mu.Lock(); s.f = f; s.mu.Unlock() }

// Turning the switch off ends the flow completely: both endpoints answer 404, nothing is queued,
// and a link issued earlier does not work while the switch is off. Nothing is deleted, so the link
// works again when the switch is turned back on.
func TestIntegrationPasswordResetSwitchOff(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiresetswitch"))
	logs := &lockedBuffer{}
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	sw := &switchableFeatures{}
	sw.set(mail.Features(mail.AllFeatures))
	s := newRegistrationServer(t, env, auth.Options{Mail: mail.NewQueue(keys, sw, nil)}, true, logs)
	srv, deliverer := startMail(t, env, keys, logs)
	s.register("ada@example.com")
	xff := map[string]string{"X-Forwarded-For": "198.51.100.41"}
	admin := env.AdminConn(t)
	count := func(q string) int {
		var n int
		if err := admin.QueryRow(context.Background(), q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if r := s.do(http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"ada@example.com"}`, xff); r.code != http.StatusAccepted {
		t.Fatalf("forgot-password while on: %d %s", r.code, r.raw)
	}
	deliverer.RunOnce(context.Background())
	var token string
	for _, m := range srv.Messages() {
		text, _, _ := m.Bodies()
		if g := resetLink.FindStringSubmatch(text); g != nil {
			token = g[1]
		}
	}
	if token == "" {
		t.Fatal("no reset link was sent")
	}
	providers := func() map[string]any { return s.do(http.MethodGet, "/api/v1/auth/providers", "", nil).body }
	if providers()["passwordResetEnabled"] != true {
		t.Fatal("passwordResetEnabled is false while the switch is on")
	}

	// The switch goes off after the link was issued.
	sw.set(mail.Features{Enabled: true, Verification: true})
	if providers()["passwordResetEnabled"] != false {
		t.Fatal("passwordResetEnabled is true while the switch is off")
	}
	outbox, resets := count(`SELECT count(*) FROM mail_outbox`), count(`SELECT count(*) FROM password_resets`)
	for _, body := range []string{`{"email":"ada@example.com"}`, `{"email":"nobody@example.com"}`} {
		r := s.do(http.MethodPost, "/api/v1/auth/forgot-password", body, xff)
		if r.code != http.StatusNotFound || r.body["type"] != "urn:proreta:problem:not-found" {
			t.Fatalf("forgot-password while off: %d %s", r.code, r.raw)
		}
	}
	r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+token+`","password":"`+resetNewPassword+`"}`, xff)
	if r.code != http.StatusNotFound || r.body["type"] != "urn:proreta:problem:not-found" {
		t.Fatalf("reset-password with a valid token while off: %d %s", r.code, r.raw)
	}
	if count(`SELECT count(*) FROM mail_outbox`) != outbox || count(`SELECT count(*) FROM password_resets`) != resets ||
		count(`SELECT count(*) FROM password_resets WHERE used_at IS NOT NULL`) != 0 {
		t.Fatal("turning the switch off, or a request while off, changed the stored links or queued mail")
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/login", `{"login":"ada@example.com","password":"`+ownerPassword+`"}`, xff); r.code != http.StatusOK {
		t.Fatalf("the password changed while off: %d", r.code)
	}

	// Back on: the same link works until its own expiry.
	sw.set(mail.Features(mail.AllFeatures))
	if r := s.do(http.MethodPost, "/api/v1/auth/reset-password", `{"token":"`+token+`","password":"`+resetNewPassword+`"}`, xff); r.code != http.StatusNoContent {
		t.Fatalf("reset-password after turning the switch back on: %d %s", r.code, r.raw)
	}
}
