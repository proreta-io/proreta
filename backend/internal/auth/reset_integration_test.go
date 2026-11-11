package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

const (
	oldPassword = "a perfectly fine password"
	newPassword = "a brand new passphrase"
)

func (e *verifyEnv) resetTokens() (tokens, to []string) {
	e.t.Helper()
	rows, err := e.admin.Query(e.ctx, `SELECT id, payload FROM mail_outbox WHERE kind = 'password_reset' ORDER BY created_at, id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			e.t.Fatal(err)
		}
		rcpt, params, err := mail.Open(e.keys, id, payload)
		if err != nil {
			e.t.Fatal(err)
		}
		tokens, to = append(tokens, params["token"]), append(to, rcpt)
	}
	return tokens, to
}

func TestIntegrationPasswordResetFlow(t *testing.T) {
	e := newVerifyEnv(t, "auth_reset_flow", true)
	res := e.register("ada@example.com")
	other := e.register("bob@example.com")
	if _, err := e.admin.Exec(e.ctx, `UPDATE users SET password_must_change = true WHERE email = 'ada@example.com'`); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.RequestPasswordReset(e.ctx, " Ada@Example.com ", testIP); err != nil {
		t.Fatal(err)
	}
	tokens, to := e.resetTokens()
	if len(tokens) != 1 || to[0] != "ada@example.com" || len(tokens[0]) != 43 {
		t.Fatalf("queued %v %v", tokens, to)
	}
	var plain int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*) FROM password_resets WHERE token_hash = sha256(convert_to($1, 'UTF8'))`, tokens[0]).Scan(&plain); err != nil || plain != 1 {
		t.Fatalf("only the SHA-256 of the token may be stored: %d %v", plain, err)
	}

	// A bad password leaves the link usable.
	if err := e.svc.ResetPassword(e.ctx, tokens[0], "short", testIP); err == nil {
		t.Fatal("short password accepted")
	} else if verr := new(validation.Error); !errors.As(err, &verr) {
		t.Fatalf("short password: %v", err)
	}
	if err := e.svc.ResetPassword(e.ctx, tokens[0], "ADA@example.com", testIP); err == nil {
		t.Fatal("the email as password accepted")
	}

	// The reset changes the password, ends every session of the account and only that account's.
	if err := e.svc.ResetPassword(e.ctx, tokens[0], newPassword, testIP); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, res.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("the old session survived: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, other.Token); err != nil {
		t.Fatalf("another account's session was ended: %v", err)
	}
	if _, err := e.svc.Login(e.ctx, auth.LoginInput{Login: "ada@example.com", Password: oldPassword, IP: testIP}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	if in, err := e.svc.Login(e.ctx, auth.LoginInput{Login: "ada@example.com", Password: newPassword, IP: testIP}); err != nil {
		t.Fatalf("new password: %v", err)
	} else if in.User.MustChangePassword {
		t.Fatal("a password chosen through the reset link still has to be changed")
	}
	if e.verifiedAt("ada@example.com") == nil {
		t.Fatal("a reset through the mailbox did not verify the address")
	}
	if err := e.svc.ResetPassword(e.ctx, tokens[0], "yet another passphrase", testIP); !errors.Is(err, auth.ErrResetInvalid) {
		t.Fatalf("reused link: %v", err)
	}

	var requested, completed string
	if err := e.admin.QueryRow(e.ctx, `SELECT metadata::text FROM audit_log WHERE action = 'auth.password_reset_requested'`).Scan(&requested); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.QueryRow(e.ctx, `SELECT metadata::text FROM audit_log WHERE action = 'auth.password_reset_completed'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(requested+completed, "ada") || strings.Contains(requested+completed, tokens[0]) || !strings.Contains(completed, `"sessionsRevoked": 1`) {
		t.Fatalf("audit metadata: %s / %s", requested, completed)
	}
}

func TestIntegrationPasswordResetAnswersTheSameForEveryone(t *testing.T) {
	e := newVerifyEnv(t, "auth_reset_same", true)
	e.register("ada@example.com")
	// An account without a password (an OAuth-only user), a disabled one and the system admin.
	for _, q := range []string{
		`INSERT INTO users (id, email) VALUES (gen_random_uuid(), 'oauth@example.com')`,
		`INSERT INTO users (id, email, password_hash, status) SELECT gen_random_uuid(), 'off@example.com', password_hash, 'disabled' FROM users WHERE email = 'ada@example.com'`,
	} {
		if _, err := e.admin.Exec(e.ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, addr := range []string{"nobody@example.com", "oauth@example.com", "off@example.com", "admin"} {
		err := e.svc.RequestPasswordReset(e.ctx, addr, testIP)
		if addr == "admin" {
			// Not an address: the same validation error whoever it names.
			var verr *validation.Error
			if !errors.As(err, &verr) {
				t.Errorf("%q: %v", addr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", addr, err)
		}
	}
	if tokens, _ := e.resetTokens(); len(tokens) != 0 {
		t.Fatalf("%d reset mails for accounts that cannot be reset", len(tokens))
	}
	var n int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*) FROM password_resets`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tokens stored: %d %v", n, err)
	}
	if err := e.svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); err != nil {
		t.Fatal(err)
	}
	if tokens, _ := e.resetTokens(); len(tokens) != 1 {
		t.Fatalf("known account: %d mails", len(tokens))
	}
}

func TestIntegrationPasswordResetExpiryAndReplacement(t *testing.T) {
	e := newVerifyEnv(t, "auth_reset_expiry", true)
	e.register("ada@example.com")
	if err := e.svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); err != nil {
		t.Fatal(err)
	}
	// A second request, after the per-address window is not needed: use a fresh limiter by moving time.
	e.clk.now = e.clk.now.Add(61 * time.Minute)
	if err := e.svc.ResetPassword(e.ctx, mustFirstReset(t, e), newPassword, testIP); !errors.Is(err, auth.ErrResetInvalid) {
		t.Fatalf("expired link: %v", err)
	}
	if err := e.svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); err != nil {
		t.Fatal(err)
	}
	tokens, _ := e.resetTokens()
	if len(tokens) != 2 {
		t.Fatalf("%d mails", len(tokens))
	}
	if err := e.svc.ResetPassword(e.ctx, tokens[0], newPassword, testIP); !errors.Is(err, auth.ErrResetInvalid) {
		t.Fatalf("replaced link: %v", err)
	}
	if err := e.svc.ResetPassword(e.ctx, tokens[1], newPassword, testIP); err != nil {
		t.Fatalf("newest link: %v", err)
	}
	for _, bad := range []string{"", "nope", strings.Repeat("A", 43)} {
		if err := e.svc.ResetPassword(e.ctx, bad, newPassword, testIP); !errors.Is(err, auth.ErrResetInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func mustFirstReset(t *testing.T, e *verifyEnv) string {
	t.Helper()
	tokens, _ := e.resetTokens()
	if len(tokens) == 0 {
		t.Fatal("no reset mail")
	}
	return tokens[0]
}

func TestIntegrationPasswordResetAddressChangeKillsTheLink(t *testing.T) {
	e := newVerifyEnv(t, "auth_reset_addr", true)
	e.register("ada@example.com")
	if err := e.svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); err != nil {
		t.Fatal(err)
	}
	tok := mustFirstReset(t, e)
	if _, err := e.admin.Exec(e.ctx, `UPDATE users SET email = 'ada2@example.com' WHERE email = 'ada@example.com'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ResetPassword(e.ctx, tok, newPassword, testIP); !errors.Is(err, auth.ErrResetInvalid) {
		t.Fatalf("link for an old address: %v", err)
	}
}

func TestIntegrationPasswordResetLimits(t *testing.T) {
	e := newVerifyEnv(t, "auth_reset_limits", true)
	svc, err := auth.NewService(e.env.Pool(t, e.env.APIURL, 8), auth.Options{
		SessionTTL: time.Hour, Mail: mail.NewQueue(e.keys, nil), Clock: e.clk,
		ResetLimiter:        auth.NewLimiter(5, time.Hour, 100, clock.System{}),
		ResetAddressLimiter: auth.NewLimiter(1, time.Hour, 100, clock.System{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := crypto.HashPassword("pw", cheap)
	if err := svc.BootstrapAdmin(e.ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(e.ctx, auth.RegisterInput{Email: "ada@example.com", Password: oldPassword, IP: testIP}); err != nil {
		t.Fatal(err)
	}
	// One request per address and hour sends mail; the second is answered alike and sends nothing.
	for i := 0; i < 2; i++ {
		if err := svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); err != nil {
			t.Fatal(err)
		}
	}
	if tokens, _ := e.resetTokens(); len(tokens) != 1 {
		t.Fatalf("%d mails for two requests", len(tokens))
	}
	// The per-IP limit is an explicit 429.
	var rl *auth.RateLimitedError
	var last error
	for i := 0; i < 6; i++ {
		last = svc.RequestPasswordReset(e.ctx, "x"+string(rune('a'+i))+"@example.com", testIP)
	}
	if !errors.As(last, &rl) {
		t.Fatalf("sixth request from one IP: %v", last)
	}
}

func TestIntegrationPasswordResetPerAccountCap(t *testing.T) {
	e := newVerifyEnv(t, "auth_reset_cap", true)
	svc, err := auth.NewService(e.env.Pool(t, e.env.APIURL, 8), auth.Options{
		SessionTTL: time.Hour, Mail: mail.NewQueue(e.keys, nil), Clock: e.clk,
		ResetLimiter:        auth.NewLimiter(100, time.Hour, 100, clock.System{}),
		ResetAddressLimiter: auth.NewLimiter(100, time.Hour, 100, clock.System{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := crypto.HashPassword("pw", cheap)
	if err := svc.BootstrapAdmin(e.ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(e.ctx, auth.RegisterInput{Email: "ada@example.com", Password: oldPassword, IP: testIP}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); err != nil {
			t.Fatal(err)
		}
	}
	if tokens, _ := e.resetTokens(); len(tokens) != 3 {
		t.Fatalf("%d mails in an hour, want 3", len(tokens))
	}
}

func TestIntegrationPasswordResetDisabled(t *testing.T) {
	off := newVerifyEnv(t, "auth_reset_off", false)
	off.register("ada@example.com")
	if err := off.svc.RequestPasswordReset(off.ctx, "ada@example.com", testIP); !errors.Is(err, auth.ErrEmailDisabled) {
		t.Fatalf("without email: %v", err)
	}
	if err := off.svc.ResetPassword(off.ctx, strings.Repeat("A", 43), newPassword, testIP); !errors.Is(err, auth.ErrEmailDisabled) {
		t.Fatalf("reset without email: %v", err)
	}
	var n int
	if err := off.admin.QueryRow(off.ctx, `SELECT (SELECT count(*) FROM mail_outbox) + (SELECT count(*) FROM password_resets)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows: %d %v", n, err)
	}

	e := newVerifyEnv(t, "auth_reset_nopw", true)
	svc, err := auth.NewService(e.env.Pool(t, e.env.APIURL, 4), auth.Options{SessionTTL: time.Hour, Mail: mail.NewQueue(e.keys, nil), PasswordLoginDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset(e.ctx, "ada@example.com", testIP); !errors.Is(err, auth.ErrEmailDisabled) {
		t.Fatalf("password form off: %v", err)
	}
}
