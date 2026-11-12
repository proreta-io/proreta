package monitors

import (
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

func TestCheckRequest(t *testing.T) {
	cases := []struct {
		name                string
		method, body, ctype string
		wantType            string
		wantField           string // empty: valid
		wantMsg             string
	}{
		{"plain get", "GET", "", "", "", "", ""},
		{"head", "HEAD", "", "", "", "", ""},
		{"post without body", "POST", "", "", "", "", ""},
		{"content type ignored without body", "POST", "", "text/plain", "", "", ""},
		{"post json default", "POST", `{"a":1}`, "", "application/json", "", ""},
		{"put text", "PUT", "hi", "text/plain", "text/plain", "", ""},
		{"patch form", "PATCH", "a=b", "application/x-www-form-urlencoded", "application/x-www-form-urlencoded", "", ""},
		{"delete xml", "DELETE", "<a/>", "application/xml", "application/xml", "", ""},
		{"unicode body", "POST", "héllo ✓", "", "application/json", "", ""},
		{"body at limit", "POST", strings.Repeat("a", MaxRequestBodyBytes), "", "application/json", "", ""},
		{"body too large", "POST", strings.Repeat("a", MaxRequestBodyBytes+1), "", "", fieldRequestBody, "must be at most 16384 bytes"},
		{"body with GET", "GET", "x", "", "", fieldRequestBody, "is not allowed with GET or HEAD"},
		{"body with HEAD", "HEAD", "x", "text/plain", "", fieldRequestBody, "is not allowed with GET or HEAD"},
		{"unknown method", "TRACE", "", "", "", fieldMethod, "must be one of GET, HEAD, POST, PUT, PATCH or DELETE"},
		{"lowercase method", "post", "", "", "", fieldMethod, "must be one of GET, HEAD, POST, PUT, PATCH or DELETE"},
		{"unknown content type", "POST", "x", "text/html", "", fieldContentType, "must be one of application/json, text/plain, application/x-www-form-urlencoded or application/xml"},
		{"invalid utf-8", "POST", "a\xffb", "", "", fieldRequestBody, "must be valid UTF-8 text without NUL characters"},
		{"nul", "POST", "a\x00b", "", "", fieldRequestBody, "must be valid UTF-8 text without NUL characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var verr validation.Error
			got := checkRequest(&verr, tc.method, tc.body, tc.ctype)
			err := verr.Err()
			if tc.wantField == "" {
				if err != nil || got != tc.wantType {
					t.Fatalf("got type %q, err %v; want type %q", got, err, tc.wantType)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("err = %v, want a problem on %s", err, tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want message %q", err, tc.wantMsg)
			}
		})
	}
}
