package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/auth"
	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

func requestKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{4}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func fieldErrorOf(t *testing.T, r response) string {
	t.Helper()
	fe, _ := r.body["fieldErrors"].([]any)
	if r.code != http.StatusUnprocessableEntity || len(fe) != 1 {
		t.Fatalf("want one field error, got %d %s", r.code, r.raw)
	}
	return fe[0].(map[string]any)["field"].(string)
}

func TestIntegrationMonitorRequest(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apimonitorreq"))
	keys := requestKeyring(t)
	c := newServerKeys(t, env, auth.Options{}, keys)
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)
	admin := env.AdminConn(t)
	ctx := context.Background()
	const token, bodyText = "tok-s3cr3t-value", `{"secret-body":"b0dy-s3cr3t"}`

	r := c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"Orders","http":{"url":"https://example.com/ping","method":"POST",
		"headers":[{"name":"Authorization","value":"Bearer `+token+`"},{"name":"X-Env","value":"prod"}],"body":`+jsonString(bodyText)+`}}`, unsafe)
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	h := r.body["http"].(map[string]any)
	if h["method"] != "POST" || h["bodySet"] != true || len(h["headerNames"].([]any)) != 2 || h["headerNames"].([]any)[0] != "Authorization" {
		t.Fatalf("read model: %s", r.raw)
	}

	// Nothing secret in any read, in the stored config, or in the audit row.
	for _, path := range []string{"/api/v1/monitors/" + id, "/api/v1/monitors"} {
		if got := c.do(http.MethodGet, path, "", cookie); strings.Contains(got.raw, token) || strings.Contains(got.raw, "b0dy-s3cr3t") {
			t.Fatalf("%s returned a secret: %s", path, got.raw)
		}
	}
	var tenant uuid.UUID
	var config string
	if err := admin.QueryRow(ctx, `SELECT tenant_id, config::text FROM monitors WHERE id = $1`, id).Scan(&tenant, &config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, token) || strings.Contains(config, "b0dy-s3cr3t") || !strings.Contains(config, `"request"`) {
		t.Fatalf("stored config: %s", config)
	}
	open := func() monitorrequest.Secrets {
		t.Helper()
		var sealed string
		if err := admin.QueryRow(ctx, `SELECT config ->> 'request' FROM monitors WHERE id = $1`, id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		s, err := monitorrequest.Open(keys, tenant, uuid.MustParse(id), sealed)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := open(); s.Body != bodyText || s.Headers[0].Value != "Bearer "+token || s.Headers[1].Value != "prod" {
		t.Fatalf("sealed content: %+v", s)
	}

	// Omitting a header's value keeps it; changing another one replaces only that.
	r = c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"headers":[{"name":"authorization"},{"name":"X-Env","value":"staging"}]}}`, unsafe)
	if r.code != http.StatusOK {
		t.Fatalf("keep header: %d %s", r.code, r.raw)
	}
	if s := open(); s.Headers[0].Value != "Bearer "+token || s.Headers[1].Value != "staging" || s.Body != bodyText {
		t.Fatalf("after keep: %+v", s)
	}
	// A header without a stored value must carry one.
	if f := fieldErrorOf(t, c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"headers":[{"name":"X-New"}]}}`, unsafe)); f != "http.headers[0].value" {
		t.Fatalf("field = %s", f)
	}
	// Moving to GET while the body stays is refused; clearing the body in the same request is fine.
	if f := fieldErrorOf(t, c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"method":"GET"}}`, unsafe)); f != "http.body" {
		t.Fatalf("field = %s", f)
	}
	r = c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"method":"GET","body":""}}`, unsafe)
	if r.code != http.StatusOK || r.body["http"].(map[string]any)["bodySet"] != false || r.body["http"].(map[string]any)["method"] != "GET" {
		t.Fatalf("clear body: %d %s", r.code, r.raw)
	}
	if s := open(); s.Body != "" || len(s.Headers) != 2 {
		t.Fatalf("after clear: %+v", s)
	}
	// Removing every header and the body drops the sealed value.
	r = c.do(http.MethodPatch, "/api/v1/monitors/"+id, `{"http":{"headers":[]}}`, unsafe)
	var sealedLeft *string
	if err := admin.QueryRow(ctx, `SELECT config ->> 'request' FROM monitors WHERE id = $1`, id).Scan(&sealedLeft); err != nil || r.code != http.StatusOK || sealedLeft != nil {
		t.Fatalf("remove all: %d %s, sealed %v, %v", r.code, r.raw, sealedLeft, err)
	}

	var audit string
	if err := admin.QueryRow(ctx, `SELECT string_agg(metadata::text, ' ') FROM audit_log WHERE entity_id = $1`, id).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, "b0dy-s3cr3t", "staging"} {
		if strings.Contains(audit, secret) {
			t.Fatalf("audit metadata contains a secret: %s", audit)
		}
	}
	if !strings.Contains(audit, "http.headers") || !strings.Contains(audit, "http.body") || !strings.Contains(audit, "http.method") {
		t.Fatalf("audit lacks the changed field names: %s", audit)
	}
}

func TestIntegrationMonitorRequestValidation(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apimonitorreqv"))
	c := newServerKeys(t, env, auth.Options{}, requestKeyring(t))
	cookie, csrf := c.login()
	unsafe := withCSRF(cookie, csrf)
	create := func(httpBody string) response {
		return c.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"v","http":{"url":"https://example.com/",`+httpBody+`}}`, unsafe)
	}
	for body, field := range map[string]string{
		`"method":"POST","body":"x" ,"headers":[{"name":"Host","value":"evil"}]`:            "http.headers[0].name",
		`"method":"POST","headers":[{"name":"Bad Name","value":"v"}]`:                       "http.headers[0].name",
		`"method":"POST","headers":[{"name":"X-A","value":"a\r\nX-B: b"}]`:                  "http.headers[0].value",
		`"method":"POST","headers":[{"name":"X-A","value":"1"},{"name":"x-a","value":"2"}]`: "http.headers[1].name",
		`"method":"POST","headers":[{"name":"X-A"}]`:                                        "http.headers[0].value",
		`"body":"x"`:                        "http.body",
		`"method":"HEAD","body":"x"`:        "http.body",
		`"method":"POST","body":"a\u0000b"`: "http.body",
	} {
		if got := fieldErrorOf(t, create(body)); got != field {
			t.Errorf("%s: field %s, want %s", body, got, field)
		}
	}
	// Methods outside the list are rejected by the contract.
	for _, m := range []string{"OPTIONS", "TRACE", "get"} {
		if r := create(`"method":"` + m + `"`); r.code != http.StatusBadRequest {
			t.Errorf("method %s: %d %s", m, r.code, r.raw)
		}
	}
	// Without sealed storage on the installation, secrets are refused and methods still work.
	c2 := newServer(t, pgtest.New(t, pgtest.DBName("apimonitorreqn")), auth.Options{})
	cookie2, csrf2 := c2.login()
	unsafe2 := withCSRF(cookie2, csrf2)
	r := c2.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"n","http":{"url":"https://example.com/","method":"POST","headers":[{"name":"X-A","value":"1"}]}}`, unsafe2)
	if f := fieldErrorOf(t, r); f != "http.headers" {
		t.Fatalf("field = %s", f)
	}
	if r := c2.do(http.MethodPost, "/api/v1/monitors", `{"type":"http","name":"n","http":{"url":"https://example.com/","method":"PUT"}}`, unsafe2); r.code != http.StatusCreated {
		t.Fatalf("method only: %d %s", r.code, r.raw)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
