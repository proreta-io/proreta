package monitorrequest

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
)

func keyring(t *testing.T, seed byte) *crypto.Keyring {
	t.Helper()
	k, err := crypto.NewKeyring(map[string][]byte{"r1": bytes.Repeat([]byte{seed}, 32)}, "r1")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTripAndBinding(t *testing.T) {
	keys := keyring(t, 1)
	tenant, mon := uuid.New(), uuid.New()
	in := Secrets{Headers: []Header{{"Authorization", "Bearer s3cret"}}, Body: `{"a":1}`}
	sealed, err := Seal(keys, tenant, mon, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "s3cret") {
		t.Fatal("sealed value contains plaintext")
	}
	out, err := Open(keys, tenant, mon, sealed)
	if err != nil || out.Body != in.Body || len(out.Headers) != 1 || out.Headers[0] != in.Headers[0] {
		t.Fatalf("got %+v, %v", out, err)
	}
	if _, err := Open(keys, tenant, uuid.New(), sealed); err == nil {
		t.Fatal("opened for another monitor")
	}
	if _, err := Open(keys, uuid.New(), mon, sealed); err == nil {
		t.Fatal("opened for another tenant")
	}
	if _, err := Open(keyring(t, 2), tenant, mon, sealed); err == nil {
		t.Fatal("opened with another key")
	}
}

func TestNoKeys(t *testing.T) {
	if _, err := Seal(nil, uuid.New(), uuid.New(), Secrets{Body: "x"}); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Open(nil, uuid.New(), uuid.New(), "x"); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("open: %v", err)
	}
}

func TestOpenErrorsHideValues(t *testing.T) {
	keys := keyring(t, 1)
	sealed, _ := Seal(keys, uuid.New(), uuid.New(), Secrets{Body: "topsecret"})
	_, err := Open(keys, uuid.New(), uuid.New(), sealed)
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("err = %v", err)
	}
}

func TestMethods(t *testing.T) {
	for _, m := range Methods {
		if !ValidMethod(m) {
			t.Errorf("%s should be valid", m)
		}
	}
	for _, m := range []string{"", "get", "OPTIONS", "TRACE", "CONNECT"} {
		if ValidMethod(m) {
			t.Errorf("%q should be invalid", m)
		}
	}
	for m, want := range map[string]bool{"GET": false, "HEAD": false, "POST": true, "PUT": true, "PATCH": true, "DELETE": true} {
		if AllowsBody(m) != want {
			t.Errorf("AllowsBody(%s) = %v", m, !want)
		}
		if SafeMethod(m) == want {
			t.Errorf("SafeMethod(%s) = %v", m, !want)
		}
	}
}

func TestValidateHeaders(t *testing.T) {
	long := strings.Repeat("a", MaxHeaderValueBytes+1)
	cases := []struct {
		name string
		in   []Header
		key  string // expected problem key, empty for none
	}{
		{"ok", []Header{{"Authorization", "Bearer x"}, {"X-Api-Key", "k"}}, ""},
		{"empty list", nil, ""},
		{"bad name", []Header{{"Bad Name", "v"}}, "headers[0].name"},
		{"empty name", []Header{{"", "v"}}, "headers[0].name"},
		{"denied", []Header{{"Host", "x"}}, "headers[0].name"},
		{"denied proxy", []Header{{"Proxy-Authorization", "x"}}, "headers[0].name"},
		{"repeated", []Header{{"X-A", "1"}, {"x-a", "2"}}, "headers[1].name"},
		{"long value", []Header{{"X-A", long}}, "headers[0].value"},
		{"newline", []Header{{"X-A", "a\r\nInjected: 1"}}, "headers[0].value"},
		{"nul", []Header{{"X-A", "a\x00"}}, "headers[0].value"},
		{"too many", make([]Header, MaxHeaders+1), "headers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ValidateHeaders(tc.in)
			if tc.key == "" {
				if len(p) != 0 {
					t.Fatalf("problems %v", p)
				}
				return
			}
			if p[tc.key] == "" {
				t.Fatalf("want problem at %s, got %v", tc.key, p)
			}
		})
	}
}

func TestValidateBody(t *testing.T) {
	if m := ValidateBody(strings.Repeat("a", MaxBodyBytes)); m != "" {
		t.Errorf("limit body: %s", m)
	}
	if ValidateBody(strings.Repeat("a", MaxBodyBytes+1)) == "" {
		t.Error("over the limit accepted")
	}
	if ValidateBody("a\x00b") == "" || ValidateBody("\xff\xfe") == "" {
		t.Error("non-text accepted")
	}
}
