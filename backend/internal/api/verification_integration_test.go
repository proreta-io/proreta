package api_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/jobs"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
	"github.com/proreta-io/proreta/backend/internal/testsupport/smtptest"
)

var verifyLink = regexp.MustCompile(`https://proreta\.example\.com/verify-email#([A-Za-z0-9_-]{43})`)

// startMail runs an in-process SMTP server (STARTTLS and a login) and the jobs delivery code that
// sends to it.
func startMail(t *testing.T, env *pgtest.Env, keys *crypto.Keyring, logs *lockedBuffer) (*smtptest.Server, *jobs.MailDeliverer) {
	t.Helper()
	srv := smtptest.New(t, smtptest.Options{Mode: smtptest.StartTLS, User: "mailer", Pass: "mailer-secret"})
	pool := env.Pool(t, env.WorkerURL, 4)
	settings := mail.NewSettings(pool, keys, nil)
	sealed, keyID, err := settings.SealPassword("mailer-secret")
	if err != nil {
		t.Fatal(err)
	}
	admin := env.AdminConn(t)
	if _, err := admin.Exec(context.Background(), `UPDATE email_settings SET host = $1, port = $2, tls = 'starttls', from_header = 'Proreta <proreta@example.com>',
		username = 'mailer', password_sealed = $3, password_key_id = $4`, srv.Host(), srv.Port(), sealed, keyID); err != nil {
		t.Fatal(err)
	}
	newSender := func(c mail.Connection) jobs.MailSender {
		s := mail.NewSMTPSender(c)
		s.TLSConfig = &tls.Config{RootCAs: srv.Roots, MinVersion: tls.VersionTLS12}
		return s
	}
	deliverer := jobs.NewMailDeliverer(jobs.MailConfig{PublicURL: "https://proreta.example.com"}, jobs.MailDeps{
		Pool: pool, Keys: keys, Settings: settings, NewSender: newSender, Metrics: jobs.NewMetrics(prometheus.NewRegistry()),
		Log: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	return srv, deliverer
}

// TestIntegrationEmailVerificationEndToEnd registers over HTTP, delivers the queued mail through
// the real jobs delivery code to an in-process SMTP server over STARTTLS, opens the link's token
// the way the web page does and watches the session change.
func TestIntegrationEmailVerificationEndToEnd(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiverify"))
	logs := &lockedBuffer{}
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	s := newRegistrationServer(t, env, auth.Options{Mail: mail.NewQueue(keys, mail.AllFeatures, nil)}, true, logs)

	srv, deliverer := startMail(t, env, keys, logs)

	cookie, csrf, reg := s.register("ada@example.com")
	if user, _ := reg.body["user"].(map[string]any); user["emailVerified"] != false {
		t.Fatalf("registration answer: %s", reg.raw)
	}
	if me := s.do(http.MethodGet, "/api/v1/auth/me", "", cookie); me.code != 200 || me.body["user"].(map[string]any)["emailVerified"] != false {
		t.Fatalf("me before: %d %s", me.code, me.raw)
	}

	if n := deliverer.RunOnce(context.Background()); n != 1 {
		t.Fatalf("delivered %d", n)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 || msgs[0].To[0] != "ada@example.com" {
		t.Fatalf("messages: %+v", msgs)
	}
	text, html, err := msgs[0].Bodies()
	if err != nil {
		t.Fatal(err)
	}
	m := verifyLink.FindStringSubmatch(text)
	if m == nil || !strings.Contains(html, m[0]) {
		t.Fatalf("no verification link in the text and HTML parts:\n%s\n%s", text, html)
	}
	token := m[1]
	if strings.Contains(text+html, "?") && strings.Contains(text+html, "?token") {
		t.Fatal("the token travels in a query string")
	}

	// The link's page posts the token; no session or CSRF token is needed.
	if r := s.do(http.MethodPost, "/api/v1/auth/verify-email", `{"token":"`+strings.Repeat("A", 43)+`"}`, nil); r.code != http.StatusNotFound {
		t.Fatalf("unknown token: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/verify-email", `{"token":"`+token+`"}`, nil); r.code != http.StatusNoContent {
		t.Fatalf("verify: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/verify-email", `{"token":"`+token+`"}`, nil); r.code != http.StatusNotFound || r.body["type"] != "urn:proreta:problem:not-found" {
		t.Fatalf("reused token: %d %s", r.code, r.raw)
	}
	if me := s.do(http.MethodGet, "/api/v1/auth/me", "", cookie); me.body["user"].(map[string]any)["emailVerified"] != true {
		t.Fatalf("me after: %s", me.raw)
	}

	// Resending to a verified account does nothing; the endpoint needs the session and CSRF token.
	hdr := map[string]string{"Cookie": cookie["Cookie"], "X-CSRF-Token": csrf, "X-Forwarded-For": "198.51.100.20"}
	if r := s.do(http.MethodPost, "/api/v1/auth/resend-verification", "", cookie); r.code != http.StatusForbidden {
		t.Fatalf("resend without CSRF: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/resend-verification", "", nil); r.code != http.StatusUnauthorized {
		t.Fatalf("resend without session: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/resend-verification", "", hdr); r.code != http.StatusNoContent {
		t.Fatalf("resend: %d %s", r.code, r.raw)
	}
	if n := deliverer.RunOnce(context.Background()); n != 0 {
		t.Fatalf("a verified account was sent another mail (%d)", n)
	}

	// Neither the token nor the address is in the logs.
	all := logs.String()
	for _, secret := range []string{token, "ada@example.com", "mailer-secret"} {
		if strings.Contains(all, secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
	// The wait for the next link is a 429 with Retry-After.
	other, ocsrf, _ := s.register("bob@example.com")
	ohdr := map[string]string{"Cookie": other["Cookie"], "X-CSRF-Token": ocsrf, "X-Forwarded-For": "198.51.100.21"}
	if r := s.do(http.MethodPost, "/api/v1/auth/resend-verification", "", ohdr); r.code != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("resend right after registration: %d %s", r.code, r.raw)
	}
}

// With no mail queue the endpoints behave as if email did not exist.
func TestIntegrationEmailVerificationWithoutEmail(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiverifyoff"))
	s := newRegistrationServer(t, env, auth.Options{}, true, &lockedBuffer{})
	cookie, csrf, _ := s.register("ada@example.com")
	hdr := map[string]string{"Cookie": cookie["Cookie"], "X-CSRF-Token": csrf}
	if r := s.do(http.MethodPost, "/api/v1/auth/resend-verification", "", hdr); r.code != http.StatusNotFound {
		t.Fatalf("resend: %d %s", r.code, r.raw)
	}
	if r := s.do(http.MethodPost, "/api/v1/auth/verify-email", `{"token":"`+strings.Repeat("A", 43)+`"}`, nil); r.code != http.StatusNotFound {
		t.Fatalf("verify: %d %s", r.code, r.raw)
	}
	admin := env.AdminConn(t)
	var n int
	if err := admin.QueryRow(context.Background(), `SELECT count(*) FROM mail_outbox`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("queued %d messages without email (%v)", n, err)
	}
}
