package auth_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/mail"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// fakeClock moves only when told.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

var _ clock.Clock = (*fakeClock)(nil)

type verifyEnv struct {
	t     *testing.T
	ctx   context.Context
	env   *pgtest.Env
	admin *pgx.Conn
	svc   *auth.Service
	keys  *crypto.Keyring
	clk   *fakeClock
}

func newVerifyEnv(t *testing.T, name string, withMail bool) *verifyEnv {
	t.Helper()
	env := pgtest.New(t, pgtest.DBName(name))
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{5}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	opts := auth.Options{SessionTTL: time.Hour, Clock: clk}
	if withMail {
		opts.Mail = mail.NewQueue(keys, mail.AllFeatures, nil)
	}
	svc, err := auth.NewService(env.Pool(t, env.APIURL, 8), opts)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := crypto.HashPassword("pw", cheap)
	if err := svc.BootstrapAdmin(context.Background(), "admin", hash); err != nil {
		t.Fatal(err)
	}
	return &verifyEnv{t: t, ctx: context.Background(), env: env, admin: env.AdminConn(t), svc: svc, keys: keys, clk: clk}
}

// queued returns the tokens of the queued verification mails, oldest first, with the recipients.
func (e *verifyEnv) queued() (tokens, to []string) {
	e.t.Helper()
	rows, err := e.admin.Query(e.ctx, `SELECT id, kind, payload FROM mail_outbox ORDER BY created_at, id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var kind string
		var payload []byte
		if err := rows.Scan(&id, &kind, &payload); err != nil {
			e.t.Fatal(err)
		}
		if kind != string(mail.KindVerifyEmail) || len(payload) == 0 {
			continue
		}
		rcpt, params, err := mail.Open(e.keys, id, payload)
		if err != nil {
			e.t.Fatal(err)
		}
		tokens, to = append(tokens, params["token"]), append(to, rcpt)
	}
	return tokens, to
}

func (e *verifyEnv) register(email string) auth.LoginResult {
	e.t.Helper()
	res, err := e.svc.Register(e.ctx, auth.RegisterInput{Email: email, Password: "a perfectly fine password", IP: testIP})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *verifyEnv) verifiedAt(email string) *time.Time {
	e.t.Helper()
	var at *time.Time
	if err := e.admin.QueryRow(e.ctx, `SELECT email_verified_at FROM users WHERE email = $1`, email).Scan(&at); err != nil {
		e.t.Fatal(err)
	}
	return at
}

func (e *verifyEnv) principal(res auth.LoginResult) auth.Principal {
	e.t.Helper()
	p, err := e.svc.Authenticate(e.ctx, res.Token)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestIntegrationRegistrationQueuesVerification(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_register", true)
	res := e.register("Ada@Example.com")
	if res.User.EmailVerified {
		t.Fatal("a new account is verified")
	}
	tokens, to := e.queued()
	if len(tokens) != 1 || to[0] != "ada@example.com" || len(tokens[0]) != 43 {
		t.Fatalf("queued %v %v; want one 256-bit token for the lowercase address", tokens, to)
	}
	var stored, plain int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*), count(*) FILTER (WHERE token_hash = sha256(convert_to($1, 'UTF8'))) FROM email_verifications`, tokens[0]).Scan(&stored, &plain); err != nil || stored != 1 || plain != 1 {
		t.Fatalf("stored hashes %d matching %d (%v); only the SHA-256 of the token may be stored", stored, plain, err)
	}
	var leaked int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*) FROM email_verifications WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%'`, tokens[0]).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("raw token in email_verifications (%d, %v)", leaked, err)
	}

	// A verified link: the session reports it, the audit row has no address, the token is spent.
	if err := e.svc.VerifyEmail(e.ctx, tokens[0], testIP); err != nil {
		t.Fatal(err)
	}
	if e.verifiedAt("ada@example.com") == nil {
		t.Fatal("not verified")
	}
	info, err := e.svc.Me(e.ctx, e.principal(res))
	if err != nil || !info.User.EmailVerified {
		t.Fatalf("me: %+v %v", info.User, err)
	}
	if err := e.svc.VerifyEmail(e.ctx, tokens[0], testIP); !errors.Is(err, auth.ErrVerificationInvalid) {
		t.Fatalf("second use: %v", err)
	}
	var action, meta string
	if err := e.admin.QueryRow(e.ctx, `SELECT action, metadata::text FROM audit_log WHERE action = 'auth.email_verified'`).Scan(&action, &meta); err != nil || strings.Contains(meta, "ada") {
		t.Fatalf("audit: %q %q %v", action, meta, err)
	}
}

func TestIntegrationNoMailWithoutEmail(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_off", false)
	res := e.register("ada@example.com")
	var n int
	if err := e.admin.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM mail_outbox) + (SELECT count(*) FROM email_verifications)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows with email off: %d %v", n, err)
	}
	if e.svc.EmailVerificationEnabled(e.ctx) {
		t.Fatal("verification enabled without a queue")
	}
	if err := e.svc.ResendVerification(e.ctx, e.principal(res), testIP); !errors.Is(err, auth.ErrEmailDisabled) {
		t.Fatalf("resend: %v", err)
	}
}

func TestIntegrationVerificationTokenRules(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_rules", true)
	e.register("ada@example.com")
	tokens, _ := e.queued()

	for name, tok := range map[string]string{"malformed": "nope", "unknown": strings.Repeat("A", 43), "empty": ""} {
		if err := e.svc.VerifyEmail(e.ctx, tok, testIP); !errors.Is(err, auth.ErrVerificationInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Expiry: a day and a second later the link is dead, and so is it for an address that changed.
	e.clk.now = e.clk.now.Add(24*time.Hour + time.Second)
	if err := e.svc.VerifyEmail(e.ctx, tokens[0], testIP); !errors.Is(err, auth.ErrVerificationInvalid) {
		t.Fatalf("expired: %v", err)
	}
	if e.verifiedAt("ada@example.com") != nil {
		t.Fatal("an expired link verified the address")
	}
	e.clk.now = e.clk.now.Add(-24 * time.Hour)

	// The address changed after the mail was sent: the link must not verify the new one.
	if _, err := e.admin.Exec(e.ctx, `UPDATE users SET email = 'ada2@example.com' WHERE email = 'ada@example.com'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyEmail(e.ctx, tokens[0], testIP); !errors.Is(err, auth.ErrVerificationInvalid) {
		t.Fatalf("changed address: %v", err)
	}
	if e.verifiedAt("ada2@example.com") != nil {
		t.Fatal("a link for the old address verified the new one")
	}
	// A rolled-back attempt does not spend the token.
	if _, err := e.admin.Exec(e.ctx, `UPDATE users SET email = 'ada@example.com' WHERE email = 'ada2@example.com'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyEmail(e.ctx, tokens[0], testIP); err != nil {
		t.Fatalf("valid again: %v", err)
	}

	// A disabled account cannot be verified.
	res := e.register("bob@example.com")
	_ = res
	tokens, _ = e.queued()
	if _, err := e.admin.Exec(e.ctx, `UPDATE users SET status = 'disabled' WHERE email = 'bob@example.com'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyEmail(e.ctx, tokens[1], testIP); !errors.Is(err, auth.ErrVerificationInvalid) {
		t.Fatalf("disabled user: %v", err)
	}
}

func TestIntegrationResendReplacesTheOldLink(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_resend", true)
	res := e.register("ada@example.com")
	p := e.principal(res)

	if err := e.svc.ResendVerification(e.ctx, p, testIP); err == nil {
		t.Fatal("resend within the first minute was accepted")
	} else {
		var rl *auth.RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter <= 0 || rl.RetryAfter > time.Minute {
			t.Fatalf("err = %v", err)
		}
	}
	e.clk.now = e.clk.now.Add(61 * time.Second)
	if err := e.svc.ResendVerification(e.ctx, p, testIP); err != nil {
		t.Fatal(err)
	}
	tokens, to := e.queued()
	if len(tokens) != 2 || to[1] != "ada@example.com" || tokens[0] == tokens[1] {
		t.Fatalf("queued %v %v", tokens, to)
	}
	if err := e.svc.VerifyEmail(e.ctx, tokens[0], testIP); !errors.Is(err, auth.ErrVerificationInvalid) {
		t.Fatalf("the replaced link still works: %v", err)
	}
	if err := e.svc.VerifyEmail(e.ctx, tokens[1], testIP); err != nil {
		t.Fatalf("the newest link: %v", err)
	}
	// Verified: another resend queues nothing.
	e.clk.now = e.clk.now.Add(time.Hour)
	if err := e.svc.ResendVerification(e.ctx, p, testIP); err != nil {
		t.Fatal(err)
	}
	if tokens, _ = e.queued(); len(tokens) != 2 {
		t.Fatalf("a verified account got another mail (%d)", len(tokens))
	}
	var actions int
	if err := e.admin.QueryRow(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'auth.email_verification_requested'`).Scan(&actions); err != nil || actions != 1 {
		t.Fatalf("audit rows = %d (%v)", actions, err)
	}
}

func TestIntegrationResendDailyCap(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_cap", true)
	p := e.principal(e.register("ada@example.com"))
	sent := 1
	for i := 0; i < 10; i++ {
		e.clk.now = e.clk.now.Add(61 * time.Second)
		err := e.svc.ResendVerification(e.ctx, p, testIP)
		if err == nil {
			sent++
			continue
		}
		if !errors.Is(err, auth.ErrRateLimited) {
			t.Fatal(err)
		}
		break
	}
	if sent != 5 {
		t.Fatalf("%d links in a day, want 5", sent)
	}
}

func TestIntegrationSystemAdminHasNothingToVerify(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_admin", true)
	res, err := e.svc.Login(e.ctx, auth.LoginInput{Login: "admin", Password: "pw", IP: testIP})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.EmailVerified {
		t.Fatal("the admin login reports a verified email")
	}
	if err := e.svc.ResendVerification(e.ctx, e.principal(res), testIP); !errors.Is(err, auth.ErrEmailDisabled) {
		t.Fatalf("resend for the admin: %v", err)
	}
}

func TestIntegrationVerificationAttemptsAreLimitedPerIP(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("auth_verify_limit"))
	svc, err := auth.NewService(env.Pool(t, env.APIURL, 4), auth.Options{
		SessionTTL: time.Hour, VerifyLimiter: auth.NewLimiter(3, time.Minute, 100, clock.System{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("198.51.100.7")
	for i := 0; i < 3; i++ {
		if err := svc.VerifyEmail(context.Background(), strings.Repeat("A", 43), ip); !errors.Is(err, auth.ErrVerificationInvalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := svc.VerifyEmail(context.Background(), strings.Repeat("A", 43), ip); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("after the limit: %v", err)
	}
	if err := svc.VerifyEmail(context.Background(), strings.Repeat("A", 43), netip.MustParseAddr("198.51.100.8")); !errors.Is(err, auth.ErrVerificationInvalid) {
		t.Fatalf("another IP: %v", err)
	}
}

func TestIntegrationInvitedRegistrationQueuesVerification(t *testing.T) {
	e := newVerifyEnv(t, "auth_verify_invited", true)
	owner := e.register("owner@example.com")
	p := e.principal(owner)
	_, token, err := e.svc.CreateInvitation(e.ctx, auth.Actor{TenantID: p.TenantID, UserID: p.UserID, IP: testIP}, auth.RoleMember, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Register(e.ctx, auth.RegisterInput{Email: "guest@example.com", Password: "another fine password", InviteToken: token, IP: testIP})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.EmailVerified {
		t.Fatal("invited account verified")
	}
	if _, to := e.queued(); len(to) != 2 || to[1] != "guest@example.com" {
		t.Fatalf("queued to %v", to)
	}
}
