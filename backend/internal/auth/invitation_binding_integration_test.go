package auth_test

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func TestIntegrationInvitationExpiryChoices(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_expiry")
	cases := []struct {
		choice string
		ttl    time.Duration
		text   string
	}{
		{"", 7 * 24 * time.Hour, "7 days"},
		{"1h", time.Hour, "1 hour"},
		{"1d", 24 * time.Hour, "1 day"},
		{"7d", 7 * 24 * time.Hour, "7 days"},
		{"30d", 30 * 24 * time.Hour, "30 days"},
	}
	for _, c := range cases {
		t.Run("expiry "+c.choice, func(t *testing.T) {
			inv, token, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{
				Role: auth.RoleViewer, Email: "who" + c.choice + "@example.org", Expiry: c.choice})
			if err != nil {
				t.Fatal(err)
			}
			if got := inv.ExpiresAt.Sub(inv.CreatedAt); got != c.ttl {
				t.Fatalf("lifetime %v, want %v", got, c.ttl)
			}
			var stored time.Time
			if err := w.admin.QueryRow(w.ctx, `SELECT expires_at FROM invitations WHERE id = $1`, inv.ID).Scan(&stored); err != nil || !stored.Equal(inv.ExpiresAt) {
				t.Fatalf("stored %v %v", stored, err)
			}
			// The mail names the real lifetime, not a fixed "7 days".
			mails, _ := w.invitationMails()
			var found bool
			for _, m := range mails {
				if m["token"] == token {
					found = true
					if m["expires"] != c.text {
						t.Fatalf("mail says %q, want %q", m["expires"], c.text)
					}
				}
			}
			if !found {
				t.Fatal("no mail for the invitation")
			}
			if err := w.svc.RevokeInvitation(w.ctx, w.owner, inv.ID); err != nil {
				t.Fatal(err)
			}
		})
	}

	// Anything else is a validation error on expiresIn and writes nothing.
	for _, bad := range []string{"2h", "7", "1w", "90d", "unlimited", "7D"} {
		before := w.count("invitations")
		_, _, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Expiry: bad})
		if f := fieldNames(err); len(f) != 1 || f[0] != "expiresIn" || w.count("invitations") != before {
			t.Fatalf("%q: fields %v err %v", bad, f, err)
		}
	}

	// The database refuses a lifetime beyond 30 days whatever the application does.
	_, err := w.admin.Exec(w.ctx, `INSERT INTO invitations (id, tenant_id, role, token_hash, created_by, created_at, expires_at)
		VALUES ($1, $2, 'viewer', $3, $4, now(), now() + interval '31 days')`,
		uuid.New(), w.tenant, make([]byte, 32), w.ownerID)
	if err == nil || !strings.Contains(err.Error(), "invitations_expiry_max") {
		t.Fatalf("31 days: %v", err)
	}
}

func TestIntegrationInvitationBindToEmailCreate(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_bind_create")
	mails := func() int { return w.count("mail_outbox WHERE kind = 'invitation'") }
	boundRow := func(id uuid.UUID) []byte {
		var h []byte
		if err := w.admin.QueryRow(w.ctx, `SELECT email_hash FROM invitations WHERE id = $1`, id).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	cases := []struct {
		name    string
		in      auth.InvitationInput
		bound   bool
		emailed bool
	}{
		// What existed before: mailing does not bind.
		{"email is mailed and not bound", auth.InvitationInput{Role: auth.RoleViewer, Email: "a@example.org"}, false, true},
		{"sendEmail false ignores the address", auth.InvitationInput{Role: auth.RoleViewer, Email: "b@example.org", SendEmail: ptr(false)}, false, false},
		// New.
		{"bind alone sends nothing", auth.InvitationInput{Role: auth.RoleViewer, Email: "C@Example.org", BindToEmail: true}, true, false},
		{"bind with sendEmail true mails and binds", auth.InvitationInput{Role: auth.RoleViewer, Email: "d@example.org", BindToEmail: true, SendEmail: ptr(true)}, true, true},
		{"bind with sendEmail false", auth.InvitationInput{Role: auth.RoleViewer, Email: "e@example.org", BindToEmail: true, SendEmail: ptr(false)}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := mails()
			inv, _, emailed, err := w.svc.CreateInvitationFor(w.ctx, w.owner, c.in)
			if err != nil || emailed != c.emailed || inv.EmailBound != c.bound {
				t.Fatalf("emailed %v bound %v err %v", emailed, inv.EmailBound, err)
			}
			if got := mails() - before; (got == 1) != c.emailed || got > 1 {
				t.Fatalf("%d mails queued", got)
			}
			h := boundRow(inv.ID)
			if c.bound {
				// Only the hash of the normalised address is kept.
				sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(c.in.Email))))
				if string(h) != string(sum[:]) {
					t.Fatalf("hash %x", h)
				}
			} else if h != nil {
				t.Fatal("an unbound invitation has a hash")
			}
			if err := w.svc.RevokeInvitation(w.ctx, w.owner, inv.ID); err != nil {
				t.Fatal(err)
			}
		})
	}

	// Binding needs an address, and the address follows the registration rules.
	for _, c := range []struct {
		in    auth.InvitationInput
		field string
	}{
		{auth.InvitationInput{Role: auth.RoleViewer, BindToEmail: true}, "bindToEmail"},
		{auth.InvitationInput{Role: auth.RoleViewer, BindToEmail: true, Email: "  "}, "bindToEmail"},
		{auth.InvitationInput{Role: auth.RoleViewer, BindToEmail: true, Email: "nope"}, "email"},
		{auth.InvitationInput{Role: auth.RoleViewer, BindToEmail: true, Email: "nope", SendEmail: ptr(true)}, "email"},
	} {
		before := w.count("invitations")
		_, _, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, c.in)
		var verr *validation.Error
		if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != c.field || w.count("invitations") != before {
			t.Fatalf("%+v: %v", c.in, err)
		}
	}

	// The list says which links are bound, never to what.
	if _, _, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleViewer, Email: "f@example.org", BindToEmail: true}); err != nil {
		t.Fatal(err)
	}
	list, err := w.svc.ListInvitations(w.ctx, w.tenant)
	if err != nil {
		t.Fatal(err)
	}
	var bound int
	for _, i := range list {
		if i.EmailBound {
			bound++
		}
	}
	if len(list) == 0 || bound != 4 {
		t.Fatalf("%d bound of %d", bound, len(list))
	}

	// Without a mail server a link can still be bound, and sending still needs mail.
	off := newVerifyEnv(t, "auth_inv_bind_off", false)
	a := off.actor(off.register("owner@example.com"))
	inv, _, emailed, err := off.svc.CreateInvitationFor(off.ctx, a, auth.InvitationInput{Role: auth.RoleViewer, Email: "g@example.org", BindToEmail: true})
	if err != nil || emailed || !inv.EmailBound {
		t.Fatalf("bind without mail: %+v %v %v", inv, emailed, err)
	}
	_, _, _, err = off.svc.CreateInvitationFor(off.ctx, a, auth.InvitationInput{Role: auth.RoleViewer, Email: "g@example.org", BindToEmail: true, SendEmail: ptr(true)})
	if f := fieldNames(err); len(f) != 1 || f[0] != "email" {
		t.Fatalf("sendEmail true without mail: %v", err)
	}
}

// boundInvite creates an invitation bound to addr (nothing mailed) and returns its token.
func (w *inviteWorld) boundInvite(addr string) (uuid.UUID, string) {
	w.t.Helper()
	inv, token, _, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleMember, Email: addr, BindToEmail: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return inv.ID, token
}

func (w *inviteWorld) open(id uuid.UUID) bool {
	w.t.Helper()
	var accepted bool
	if err := w.admin.QueryRow(w.ctx, `SELECT accepted_at IS NOT NULL FROM invitations WHERE id = $1`, id).Scan(&accepted); err != nil {
		w.t.Fatal(err)
	}
	return !accepted
}

func TestIntegrationBoundInvitationRegistration(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_bound_reg")
	const pwd = "a long enough password"
	id, token := w.boundInvite("Guest@Example.org")
	users, members := w.count("users"), w.count("memberships")

	pv, err := w.svc.LookupInvitation(w.ctx, token, testIP)
	if err != nil || !pv.EmailBound {
		t.Fatalf("preview %+v %v", pv, err)
	}

	// Another typed address: refused, nothing created, the invitation stays open.
	_, err = w.svc.Register(w.ctx, auth.RegisterInput{Email: "other@example.org", Password: pwd, IP: testIP, InviteToken: token})
	if !errors.Is(err, auth.ErrInvitationEmailMismatch) {
		t.Fatalf("wrong address: %v", err)
	}
	if w.count("users") != users || w.count("memberships") != members || !w.open(id) {
		t.Fatal("a refused registration changed something")
	}
	// A malformed address is a validation error before the invitation is looked at.
	if _, err := w.svc.Register(w.ctx, auth.RegisterInput{Email: "nope", Password: pwd, IP: testIP, InviteToken: token}); fieldNames(err) == nil {
		t.Fatalf("malformed: %v", err)
	}

	// The invited address in another case and with spaces matches.
	res, err := w.svc.Register(w.ctx, auth.RegisterInput{Email: " GUEST@example.ORG ", Password: pwd, IP: testIP, InviteToken: token})
	if err != nil || res.Tenant.ID != w.tenant || res.Role != auth.RoleMember {
		t.Fatalf("right address: %+v %v", res.SessionInfo, err)
	}
	if w.open(id) {
		t.Fatal("invitation not used up")
	}
}

func TestIntegrationBoundInvitationExistingAccountAndAccept(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_bound_existing")
	const pwd = "a perfectly fine password" // the password of w.register
	// A person with an own account elsewhere: password join and signed-in accept.
	other := w.register("other@example.org")
	id, token := w.boundInvite("someone@example.org")

	// An existing account typed with its own address and password: the address is not the bound one.
	_, err := w.svc.Register(w.ctx, auth.RegisterInput{Email: "other@example.org", Password: "not " + pwd, IP: testIP, InviteToken: token})
	if err == nil {
		t.Fatal("wrong password accepted")
	}
	_, err = w.svc.Register(w.ctx, auth.RegisterInput{Email: "other@example.org", Password: pwd, IP: testIP, InviteToken: token})
	// The helper registers with this password, so the credentials are right; the address is not.
	if !errors.Is(err, auth.ErrInvitationEmailMismatch) {
		t.Fatalf("existing account, other address: %v", err)
	}
	if !w.open(id) || w.count("memberships WHERE user_id = '"+other.User.ID.String()+"'") != 1 {
		t.Fatal("refused join changed something")
	}
	// A signed-in accept counts the account's own address.
	if _, err := w.svc.AcceptInvitation(w.ctx, w.principal(other), token, testIP, "ua"); !errors.Is(err, auth.ErrInvitationEmailMismatch) {
		t.Fatalf("accept: %v", err)
	}
	if !w.open(id) {
		t.Fatal("refused accept used the invitation")
	}

	// An existing account whose own address is the bound one joins, by password and by accept.
	mine := w.register("mine@example.org")
	id2, token2 := w.boundInvite("Mine@example.org")
	res, err := w.svc.Register(w.ctx, auth.RegisterInput{Email: "mine@example.org", Password: pwd, IP: testIP, InviteToken: token2})
	if err != nil || res.User.ID != mine.User.ID || res.Tenant.ID != w.tenant {
		t.Fatalf("existing account, bound address: %+v %v", res.SessionInfo, err)
	}
	if w.open(id2) {
		t.Fatal("not used up")
	}
	third := w.register("third@example.org")
	_, token3 := w.boundInvite("third@example.org")
	if res, err := w.svc.AcceptInvitation(w.ctx, w.principal(third), token3, testIP, "ua"); err != nil || res.Tenant.ID != w.tenant {
		t.Fatalf("accept with the bound address: %v", err)
	}
}

func TestIntegrationUnboundInvitationUnchanged(t *testing.T) {
	w := newInviteWorld(t, "auth_inv_unbound")
	const pwd = "a long enough password"
	// Mailed to one address, not bound: any address may use it.
	inv, token, emailed, err := w.svc.CreateInvitationFor(w.ctx, w.owner, auth.InvitationInput{Role: auth.RoleMember, Email: "mailed@example.org"})
	if err != nil || !emailed || inv.EmailBound {
		t.Fatal(inv, emailed, err)
	}
	pv, err := w.svc.LookupInvitation(w.ctx, token, testIP)
	if err != nil || pv.EmailBound {
		t.Fatalf("preview %+v %v", pv, err)
	}
	if _, err := w.svc.Register(w.ctx, auth.RegisterInput{Email: "anyone@example.org", Password: pwd, IP: testIP, InviteToken: token}); err != nil {
		t.Fatal(err)
	}
	if w.open(inv.ID) {
		t.Fatal("not used up")
	}
}

func TestIntegrationBoundInvitationOAuth(t *testing.T) {
	e := newOAuthEnv(t, "auth_oauth_bound", auth.Options{})
	admin := e.adminLogin()
	actor := auth.Actor{TenantID: admin.Tenant.ID, UserID: admin.User.ID}
	bound := func(addr string) []byte {
		_, token, _, err := e.svc.CreateInvitationFor(e.ctx, actor, auth.InvitationInput{Role: auth.RoleMember, Email: addr, BindToEmail: true})
		if err != nil {
			t.Fatal(err)
		}
		h, err := e.svc.CheckInvitation(e.ctx, token, oauthIP)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	used := func(h []byte) bool {
		return e.count(`SELECT count(*) FROM invitations WHERE token_hash = $1 AND accepted_at IS NOT NULL`, h) == 1
	}

	// A provider-verified address that is not the invited one: refused, audited, nothing created.
	h := bound("invited@example.com")
	users := e.count(`SELECT count(*) FROM users`)
	if _, err := e.login(auth.OAuthLoginInput{Identity: github("2001", "stranger@example.com"), InviteHash: h}); !errors.Is(err, auth.ErrInvitationEmailMismatch) {
		t.Fatalf("wrong provider address: %v", err)
	}
	if e.count(`SELECT count(*) FROM users`) != users || used(h) {
		t.Fatal("a refused sign-in changed something")
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.oauth_login_failed' AND metadata->>'reason' = 'invitation_email_mismatch'`); n != 1 {
		t.Fatalf("%d mismatch audit rows", n)
	}
	// The right one, in any case, joins.
	res, err := e.login(auth.OAuthLoginInput{Identity: github("2002", "Invited@Example.com"), InviteHash: h})
	if err != nil || res.Tenant.ID != admin.Tenant.ID || !used(h) {
		t.Fatalf("right provider address: %v", err)
	}

	// Account linking: the provider address belongs to an existing account, so the identity is
	// linked by email and then the invitation is checked. A matching link joins.
	const pwd = "a long enough password"
	mine, err := e.svc.Register(e.ctx, auth.RegisterInput{Email: "mine@example.com", Password: pwd, IP: oauthIP})
	if err != nil {
		t.Fatal(err)
	}
	h2 := bound("mine@example.com")
	joined, err := e.login(auth.OAuthLoginInput{Identity: github("2003", "mine@example.com"), InviteHash: h2})
	if err != nil || joined.User.ID != mine.User.ID || joined.Tenant.ID != admin.Tenant.ID {
		t.Fatalf("link then join: %v", err)
	}
	// A bound link for another address: the link stays (the provider proved the address), the join is refused.
	h3 := bound("elsewhere@example.com")
	mine2, err := e.svc.Register(e.ctx, auth.RegisterInput{Email: "mine2@example.com", Password: pwd, IP: oauthIP})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.login(auth.OAuthLoginInput{Identity: github("2004", "mine2@example.com"), InviteHash: h3}); !errors.Is(err, auth.ErrInvitationEmailMismatch) {
		t.Fatalf("link then refused join: %v", err)
	}
	if used(h3) || e.count(`SELECT count(*) FROM memberships WHERE user_id = $1 AND tenant_id = $2`, mine2.User.ID, admin.Tenant.ID) != 0 {
		t.Fatal("joined with a refused invitation")
	}
	// An unbound link works with any provider address.
	_, token, err := e.svc.CreateInvitation(e.ctx, actor, auth.RoleMember, "")
	if err != nil {
		t.Fatal(err)
	}
	hu, _ := e.svc.CheckInvitation(e.ctx, token, oauthIP)
	if _, err := e.login(auth.OAuthLoginInput{Identity: github("2005", "whoever@example.com"), InviteHash: hu}); err != nil {
		t.Fatal(err)
	}
}

// The owner link of a new organization is bound to the owner's address and still sends no mail.
func TestIntegrationFirstOwnerLinkIsBound(t *testing.T) {
	w := newInviteWorld(t, "auth_first_owner_bound")
	pool := w.env.Pool(t, w.env.APIURL, 2)
	var inv *auth.Invitation
	err := db.WithTx(w.ctx, pool, func(tx pgx.Tx) error {
		if err := db.SetTenant(w.ctx, tx, w.tenant); err != nil {
			return err
		}
		var err error
		_, inv, _, _, err = w.svc.AddFirstOwnerTx(w.ctx, tx, auth.PlatformActor(w.tenant, w.ownerID, testIP), "New.Owner@Example.org")
		return err
	})
	if err != nil || inv == nil || !inv.EmailBound {
		t.Fatalf("invitation %+v err %v", inv, err)
	}
	if n := w.count("mail_outbox WHERE kind = 'invitation'"); n != 0 {
		t.Fatalf("%d mails queued", n)
	}
}

// A disabled account cannot be the first owner, and the answer names the address field.
func TestIntegrationFirstOwnerDisabledAccount(t *testing.T) {
	w := newInviteWorld(t, "auth_first_owner_disabled")
	exec(t, w.admin, `UPDATE users SET status = 'disabled' WHERE email = 'viewer@example.com'`)
	pool := w.env.Pool(t, w.env.APIURL, 2)
	err := db.WithTx(w.ctx, pool, func(tx pgx.Tx) error {
		if err := db.SetTenant(w.ctx, tx, w.tenant); err != nil {
			return err
		}
		_, _, _, _, err := w.svc.AddFirstOwnerTx(w.ctx, tx, auth.PlatformActor(w.tenant, w.ownerID, testIP), "viewer@example.com")
		return err
	})
	var verr *validation.Error
	if !errors.As(err, &verr) || verr.Fields[0].Field != "ownerEmail" {
		t.Fatalf("want a validation error on ownerEmail, got %v", err)
	}
}
