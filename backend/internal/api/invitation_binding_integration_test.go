package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// TestIntegrationInvitationBindingHTTP covers the wire contract of the expiry choice, the binding
// switch and the mismatch problem, on an installation without mail (binding must not need it).
func TestIntegrationInvitationBindingHTTP(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apiinvbind"))
	s := newRegistrationServer(t, env, auth.Options{}, true, &lockedBuffer{})
	cookie, csrf, _ := s.register("owner@example.com")
	hdr := map[string]string{"Cookie": cookie["Cookie"], "X-CSRF-Token": csrf, "X-Forwarded-For": "198.51.100.40"}
	post := func(body string) response {
		return s.do(http.MethodPost, "/api/v1/invitations", body, hdr)
	}

	for _, body := range []string{
		`{"role":"member","expiresIn":"2h"}`,
		`{"role":"member","expiresIn":"90d"}`,
	} {
		if r := post(body); r.code != http.StatusUnprocessableEntity && r.code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, r.code, r.raw)
		}
	}
	if r := post(`{"role":"member","bindToEmail":true}`); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, `"field":"bindToEmail"`) {
		t.Fatalf("bind without email: %d %s", r.code, r.raw)
	}

	r := post(`{"role":"member","email":"Guest@Example.org","bindToEmail":true,"expiresIn":"1d"}`)
	if r.code != http.StatusCreated || r.body["emailQueued"] != false {
		t.Fatalf("bound invitation: %d %s", r.code, r.raw)
	}
	inv := r.body["invitation"].(map[string]any)
	if inv["emailBound"] != true || strings.Contains(r.raw, "guest@example.org") && inv["label"] != "" {
		t.Fatalf("invitation: %s", r.raw)
	}
	link := r.body["inviteUrl"].(string)
	token := link[strings.Index(link, "#")+1:]

	if lk := s.do(http.MethodPost, "/api/v1/invitations/lookup", `{"token":"`+token+`"}`, nil); lk.code != http.StatusOK || lk.body["emailBound"] != true || strings.Contains(lk.raw, "guest") {
		t.Fatalf("lookup: %d %s", lk.code, lk.raw)
	}
	reg := func(email string) response {
		return s.do(http.MethodPost, "/api/v1/auth/register", `{"email":"`+email+`","password":"a perfectly fine password","inviteToken":"`+token+`"}`,
			map[string]string{"X-Forwarded-For": "198.51.100.41"})
	}
	if m := reg("stranger@example.org"); m.code != http.StatusConflict || m.body["type"] != "urn:proreta:problem:invitation-email-mismatch" ||
		strings.Contains(m.raw, "guest") {
		t.Fatalf("mismatch: %d %s", m.code, m.raw)
	}
	l := s.do(http.MethodGet, "/api/v1/invitations", "", hdr)
	if l.code != http.StatusOK || !strings.Contains(l.raw, `"emailBound":true`) || !strings.Contains(l.raw, `"status":"open"`) {
		t.Fatalf("list: %d %s", l.code, l.raw)
	}
	if ok := reg("guest@example.org"); ok.code != http.StatusCreated {
		t.Fatalf("right address: %d %s", ok.code, ok.raw)
	}

	// Without the new fields nothing changes: unbound, seven days.
	r = post(`{"role":"member"}`)
	if r.code != http.StatusCreated || r.body["invitation"].(map[string]any)["emailBound"] != false {
		t.Fatalf("plain: %d %s", r.code, r.raw)
	}
}
