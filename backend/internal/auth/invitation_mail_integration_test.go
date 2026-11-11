package auth_test

import (
	"context"
	"encoding/json"
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

// invitationMails returns the queued invitation messages, oldest first.
func (e *verifyEnv) invitationMails() (out []map[string]string, to []string) {
	e.t.Helper()
	rows, err := e.admin.Query(e.ctx, `SELECT id, payload FROM mail_outbox WHERE kind = 'invitation' ORDER BY created_at, id`)
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
		out, to = append(out, params), append(to, rcpt)
	}
	return out, to
}

func (e *verifyEnv) actor(res auth.LoginResult) auth.Actor {
	p := e.principal(res)
	return auth.Actor{TenantID: p.TenantID, UserID: p.UserID, IP: testIP}
}

func TestIntegrationInvitationEmail(t *testing.T) {
	e := newVerifyEnv(t, "auth_invite_mail", true)
	owner := e.register("owner@example.com")
	if _, err := e.admin.Exec(e.ctx, `UPDATE users SET display_name = 'Grace Hopper' WHERE email = 'owner@example.com'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Exec(e.ctx, `UPDATE tenants SET name = 'Acme Corp' WHERE id = $1`, owner.Tenant.ID); err != nil {
		t.Fatal(err)
	}
	a := e.actor(owner)

	inv, token, emailed, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: "  Guest@Example.ORG "})
	if err != nil || !emailed {
		t.Fatalf("create: emailed=%v err=%v", emailed, err)
	}
	mails, to := e.invitationMails()
	if len(mails) != 1 || to[0] != "guest@example.org" {
		t.Fatalf("queued %v to %v", mails, to)
	}
	m := mails[0]
	if m["org"] != "Acme Corp" || m["role"] != "member" || m["inviter"] != "Grace Hopper" || m["token"] != token || m["expires"] != "7 days" {
		t.Fatalf("params = %v", m)
	}
	if strings.Contains(strings.Join(mapValues(m), "|"), "owner@example.com") {
		t.Fatal("the inviter's login is in the message")
	}
	// The mailed token is the invitation's token; the link opens the invitation.
	pv, err := e.svc.LookupInvitation(e.ctx, m["token"], testIP)
	if err != nil || pv.OrganizationName != "Acme Corp" || pv.Role != "member" {
		t.Fatalf("lookup: %+v %v", pv, err)
	}
	if inv.Label != "guest@example.org" {
		t.Fatalf("label = %q, want the address", inv.Label)
	}
	// Nothing about the address is stored besides the label the admin sees, and the audit row has none.
	var meta string
	if err := e.admin.QueryRow(e.ctx, `SELECT metadata::text FROM audit_log WHERE action = 'member.invited'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(meta), &parsed); err != nil || parsed["emailed"] != true || strings.Contains(meta, "guest") || strings.Contains(meta, token) {
		t.Fatalf("audit metadata = %s (%v)", meta, err)
	}
	var cols string
	if err := e.admin.QueryRow(e.ctx, `SELECT coalesce(string_agg(column_name, ','), '') FROM information_schema.columns WHERE table_name = 'invitations' AND column_name LIKE '%mail%'`).Scan(&cols); err != nil || cols != "" {
		t.Fatalf("invitations has email columns: %q %v", cols, err)
	}

	// An explicit label wins over the address.
	inv2, _, _, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleViewer, Label: "Ann from ops", Email: "ann@example.org"})
	if err != nil || inv2.Label != "Ann from ops" {
		t.Fatalf("label: %+v %v", inv2, err)
	}
	// Without an address nothing is queued, as before email existed (a place is freed first: the
	// free plan has three).
	if err := e.svc.RevokeInvitation(e.ctx, a, inv2.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, emailed, err = e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleViewer}); err != nil || emailed {
		t.Fatalf("no address: emailed=%v err=%v", emailed, err)
	}
	if mails, _ = e.invitationMails(); len(mails) != 2 {
		t.Fatalf("%d invitation mails, want 2", len(mails))
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestIntegrationInvitationEmailValidation(t *testing.T) {
	e := newVerifyEnv(t, "auth_invite_mail_bad", true)
	a := e.actor(e.register("owner@example.com"))
	for _, bad := range []string{"not-an-address", "a@b", "x@example.org\r\nBcc: y@example.org", "two@example.org,three@example.org", "Name <x@example.org>"} {
		_, _, _, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: bad})
		var verr *validation.Error
		if !errors.As(err, &verr) {
			t.Errorf("%q: err = %v, want a validation error", bad, err)
		}
	}
	var n int
	if err := e.admin.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM invitations) + (SELECT count(*) FROM mail_outbox WHERE kind = 'invitation')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after refused requests: %d %v", n, err)
	}
}

func TestIntegrationInvitationEmailNeedsEmail(t *testing.T) {
	e := newVerifyEnv(t, "auth_invite_mail_off", false)
	a := e.actor(e.register("owner@example.com"))
	_, _, _, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: "guest@example.org"})
	var verr *validation.Error
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error on email", err)
	}
	// The plain flow is unchanged.
	if _, token, emailed, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember}); err != nil || emailed || token == "" {
		t.Fatalf("plain: %v %v %q", err, emailed, token)
	}
}

func TestIntegrationInvitationEmailRollsBackWithTheInvitation(t *testing.T) {
	e := newVerifyEnv(t, "auth_invite_mail_limit", true)
	a := e.actor(e.register("owner@example.com"))
	// The free plan allows 3 members: the owner plus two open invitations.
	for _, addr := range []string{"a@example.org", "b@example.org"} {
		if _, _, _, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: addr}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, err := e.svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: "c@example.org"})
	if err == nil || errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("third invitation: %v, want the plan limit", err)
	}
	if mails, to := e.invitationMails(); len(mails) != 2 || to[1] != "b@example.org" {
		t.Fatalf("mails after a refused invitation: %v", to)
	}
}

func TestIntegrationInvitationEmailRateLimits(t *testing.T) {
	e := newVerifyEnv(t, "auth_invite_mail_rate", true)
	svc, err := auth.NewService(e.env.Pool(t, e.env.APIURL, 8), auth.Options{
		SessionTTL: time.Hour, Mail: mail.NewQueue(e.keys, mail.AllFeatures, nil), Clock: e.clk,
		InviteMailLimiter:          auth.NewLimiter(3, time.Hour, 100, clock.System{}),
		InviteMailRecipientLimiter: auth.NewLimiter(1, 24*time.Hour, 100, clock.System{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := crypto.HashPassword("pw", cheap)
	if err := svc.BootstrapAdmin(e.ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Register(context.Background(), auth.RegisterInput{Email: "owner@example.com", Password: "a perfectly fine password", IP: testIP})
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(context.Background(), res.Token)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.Actor{TenantID: p.TenantID, UserID: p.UserID, IP: testIP}
	if _, _, _, err := svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: "same@example.org"}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: "SAME@example.org"})
	var rl *auth.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("second mail to one recipient: %v", err)
	}
	if _, _, _, err := svc.CreateInvitationFor(e.ctx, a, auth.InvitationInput{Role: auth.RoleMember, Email: "other@example.org"}); err != nil {
		t.Fatal(err)
	}
}
