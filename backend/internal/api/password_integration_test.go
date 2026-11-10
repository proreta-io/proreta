package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func TestIntegrationChangePassword(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apipassword"))
	c := newRegistrationServer(t, env, auth.Options{}, true, &lockedBuffer{})
	const newPassword = "another long passphrase"
	xff := map[string]string{"X-Forwarded-For": "198.51.100.30"}
	with := func(cookie map[string]string, csrf string) map[string]string {
		h := map[string]string{"X-Forwarded-For": xff["X-Forwarded-For"], "X-CSRF-Token": csrf}
		for k, v := range cookie {
			h[k] = v
		}
		return h
	}
	body := func(current, next string) string {
		return fmt.Sprintf(`{"currentPassword":%q,"newPassword":%q}`, current, next)
	}

	cookie, csrf, _ := c.register("pw-user@example.com")
	otherCookie, _ := session(t, loginAs(t, c, "pw-user@example.com"))
	h := with(cookie, csrf)

	if r := c.do(http.MethodPost, "/api/v1/auth/password", body(ownerPassword, newPassword), xff); r.code != http.StatusUnauthorized {
		t.Fatalf("no session: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/auth/password", body(ownerPassword, newPassword), with(cookie, "wrong")); r.code != http.StatusForbidden || r.body["type"] != "urn:proreta:problem:csrf" {
		t.Fatalf("wrong csrf: %d %s", r.code, r.raw)
	}
	if r := c.do(http.MethodPost, "/api/v1/auth/password", `{"currentPassword":"x"}`, h); r.code != http.StatusBadRequest {
		t.Fatalf("missing field: %d %s", r.code, r.raw)
	}

	// A wrong current password is a field error, never a 401 (the web app signs out on 401).
	r := c.do(http.MethodPost, "/api/v1/auth/password", body("not my password", newPassword), h)
	if r.code != http.StatusUnprocessableEntity || r.body["type"] != "urn:proreta:problem:validation" || !strings.Contains(r.raw, `"currentPassword"`) {
		t.Fatalf("wrong current: %d %s", r.code, r.raw)
	}
	r = c.do(http.MethodPost, "/api/v1/auth/password", body(ownerPassword, "short"), h)
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.raw, `"newPassword"`) {
		t.Fatalf("short: %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, ownerPassword) || strings.Contains(r.raw, "short\"") && strings.Contains(r.raw, "currentPassword\":\"") {
		t.Fatalf("problem echoes a password: %s", r.raw)
	}

	r = c.do(http.MethodPost, "/api/v1/auth/password", body(ownerPassword, newPassword), h)
	if r.code != http.StatusOK {
		t.Fatalf("change: %d %s", r.code, r.raw)
	}
	newCookie, newCSRF := session(t, r)
	if newCookie["Cookie"] == cookie["Cookie"] || newCSRF == csrf {
		t.Fatal("the session was not replaced")
	}
	if cc := sessionCookie(t, r); !cc.HttpOnly || !cc.Secure {
		t.Fatalf("cookie attributes: %+v", cc)
	}
	if r := c.do(http.MethodGet, "/api/v1/auth/me", "", with(newCookie, newCSRF)); r.code != http.StatusOK {
		t.Fatalf("new session: %d %s", r.code, r.raw)
	}
	for name, ck := range map[string]map[string]string{"old current session": cookie, "other session": otherCookie} {
		if r := c.do(http.MethodGet, "/api/v1/auth/me", "", ck); r.code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", name, r.code, r.raw)
		}
	}
	if r := c.do(http.MethodPost, "/api/v1/auth/login", `{"login":"pw-user@example.com","password":"`+ownerPassword+`"}`, xff); r.code != http.StatusUnauthorized {
		t.Fatalf("old password: %d", r.code)
	}
	if r := c.do(http.MethodPost, "/api/v1/auth/login", `{"login":"pw-user@example.com","password":"`+newPassword+`"}`, xff); r.code != http.StatusOK {
		t.Fatalf("new password: %d %s", r.code, r.raw)
	}
	if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE action = 'auth.password_changed' AND entity_type = 'user' AND metadata ? 'sessionsRevoked'`); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	if n := countRows(t, env, `SELECT count(*) FROM audit_log WHERE metadata::text LIKE '%' || $1 || '%' OR metadata::text LIKE '%' || $2 || '%'`, ownerPassword, newPassword); n != 0 {
		t.Fatalf("a password reached the audit log")
	}

	// A user that signed in only with a provider has no password.
	cookie2, csrf2, _ := c.register("oauth-only@example.com")
	pgtestExec(t, env, `UPDATE users SET password_hash = NULL WHERE email = 'oauth-only@example.com'`)
	r = c.do(http.MethodPost, "/api/v1/auth/password", body(ownerPassword, newPassword), with(cookie2, csrf2))
	if r.code != http.StatusConflict || r.body["type"] != "urn:proreta:problem:no-password" {
		t.Fatalf("no password: %d %s", r.code, r.raw)
	}
}

func pgtestExec(t *testing.T, env *pgtest.Env, sql string) {
	t.Helper()
	pgtest.MustExec(t, env.AdminConn(t), sql)
}
