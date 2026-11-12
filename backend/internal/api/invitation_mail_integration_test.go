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
	"github.com/proreta-io/proreta/backend/internal/testsupport/smtptest"
)

var inviteLink = regexp.MustCompile(`https://proreta\.example\.com/invite#([A-Za-z0-9_-]{43})`)

// TestIntegrationInvitationEmailEndToEnd creates an invitation with an address over HTTP,
// delivers the queued mail to an in-process SMTP server, and joins through the emailed link.
func TestIntegrationInvitationEmailEndToEnd(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiinvmail"))
	logs := &lockedBuffer{}
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	s := newRegistrationServer(t, env, auth.Options{Mail: mail.NewQueue(keys, mail.AllFeatures, nil)}, true, logs)
	srv, deliverer := startMail(t, env, keys, logs)
	cookie, csrf, _ := s.register("owner@example.com")
	hdr := map[string]string{"Cookie": cookie["Cookie"], "X-CSRF-Token": csrf, "X-Forwarded-For": "198.51.100.30"}

	r := s.do(http.MethodPost, "/api/v1/invitations", `{"role":"member","email":"Guest@Example.org"}`, hdr)
	if r.code != http.StatusCreated || r.body["emailQueued"] != true {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	inviteURL, _ := r.body["inviteUrl"].(string)
	if label := r.body["invitation"].(map[string]any)["label"]; label != "guest@example.org" {
		t.Fatalf("label = %v", label)
	}
	// The owner's own verification mail is queued too; the invitation is the second message.
	if n := deliverer.RunOnce(context.Background()); n != 2 {
		t.Fatalf("delivered %d", n)
	}
	var msgs []smtptest.Message
	for _, m := range srv.Messages() {
		if m.To[0] == "guest@example.org" {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("messages to the guest: %+v", srv.Messages())
	}
	text, html, err := msgs[0].Bodies()
	if err != nil {
		t.Fatal(err)
	}
	m := inviteLink.FindStringSubmatch(text)
	if m == nil || m[0] != inviteURL || !strings.Contains(html, inviteURL) {
		t.Fatalf("the emailed link is not the invitation's link %q:\n%s", inviteURL, text)
	}
	if !strings.Contains(msgs[0].Data, "Subject: You are invited to") {
		t.Fatalf("subject: %s", msgs[0].Data[:min(400, len(msgs[0].Data))])
	}
	// Only the organization of the invitation is named, and not the inviter's login.
	if strings.Contains(text+html, "owner@example.com") {
		t.Fatal("the inviter's login is in the message")
	}

	// The link works like a copied one: look it up and register through it.
	token := m[1]
	if lk := s.do(http.MethodPost, "/api/v1/invitations/lookup", `{"token":"`+token+`"}`, nil); lk.code != http.StatusOK || lk.body["role"] != "member" {
		t.Fatalf("lookup: %d %s", lk.code, lk.raw)
	}
	reg := s.do(http.MethodPost, "/api/v1/auth/register", `{"email":"someone-else@example.net","password":"a perfectly fine password","inviteToken":"`+token+`"}`,
		map[string]string{"X-Forwarded-For": "198.51.100.31"})
	if reg.code != http.StatusCreated || reg.body["role"] != "member" {
		t.Fatalf("register through the emailed link with another address: %d %s", reg.code, reg.raw)
	}

	// Neither the token nor an address is in the logs.
	for _, secret := range []string{token, "guest@example.org", "owner@example.com", "mailer-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

func TestIntegrationInvitationEmailRefusedWithoutEmail(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiinvmailoff"))
	s := newRegistrationServer(t, env, auth.Options{}, true, &lockedBuffer{})
	cookie, csrf, _ := s.register("owner@example.com")
	hdr := map[string]string{"Cookie": cookie["Cookie"], "X-CSRF-Token": csrf, "X-Forwarded-For": "198.51.100.30"}

	r := s.do(http.MethodPost, "/api/v1/invitations", `{"role":"member","email":"guest@example.org"}`, hdr)
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, `"field":"email"`) {
		t.Fatalf("email without SMTP: %d %s", r.code, r.raw)
	}
	r = s.do(http.MethodPost, "/api/v1/invitations", `{"role":"member"}`, hdr)
	if r.code != http.StatusCreated || r.body["emailQueued"] != false || !strings.Contains(r.raw, "/invite#") {
		t.Fatalf("plain invitation: %d %s", r.code, r.raw)
	}
}

// TestIntegrationProjectInvitationEmailNamesProject: the emailed text of a project invitation names
// the project and the role in it.
func TestIntegrationProjectInvitationEmailNamesProject(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiinvmailproject"))
	logs := &lockedBuffer{}
	keys, err := crypto.NewKeyring(map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	s := newRegistrationServer(t, env, auth.Options{Mail: mail.NewQueue(keys, mail.AllFeatures, nil)}, true, logs)
	srv, deliverer := startMail(t, env, keys, logs)
	cookie, csrf, _ := s.register("owner@example.com")
	hdr := map[string]string{"Cookie": cookie["Cookie"], "X-CSRF-Token": csrf, "X-Forwarded-For": "198.51.100.32"}
	pr := s.do(http.MethodPost, "/api/v1/projects", `{"name":"Storefront"}`, hdr)
	if pr.code != http.StatusCreated {
		t.Fatalf("project: %d %s", pr.code, pr.raw)
	}
	r := s.do(http.MethodPost, "/api/v1/invitations", `{"role":"maintainer","projectId":"`+pr.body["id"].(string)+`","email":"guest@example.org"}`, hdr)
	if r.code != http.StatusCreated || r.body["emailQueued"] != true {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	deliverer.RunOnce(context.Background())
	for _, m := range srv.Messages() {
		if m.To[0] != "guest@example.org" {
			continue
		}
		text, _, err := m.Bodies()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "the project Storefront in ") || !strings.Contains(text, "as maintainer") {
			t.Fatalf("text:\n%s", text)
		}
		if !strings.Contains(m.Data, "Subject: ") || !strings.Contains(m.Data, "Storefront") {
			t.Fatalf("subject: %s", m.Data[:min(400, len(m.Data))])
		}
		return
	}
	t.Fatalf("no message to the guest: %+v", srv.Messages())
}
