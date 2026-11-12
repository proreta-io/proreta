// Package monitorrequest holds what an http monitor sends besides its URL: the method, custom
// headers and a body. Header values and the body can carry tokens, so they are sealed with the
// request keyring (a key the worker holds instead of the installation's data keys) and never
// leave the sealed form except in the process that sends the request.
package monitorrequest

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/checks"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
)

// Limits of a request.
const (
	MaxHeaders          = 10
	MaxHeaderNameBytes  = 64
	MaxHeaderValueBytes = 2048
	MaxBodyBytes        = 64 << 10
)

// Methods are the HTTP methods a monitor may use; the first is the default.
var Methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// ErrNoKeys means a sealed request cannot be opened or created because no request keyring is
// configured.
var ErrNoKeys = errors.New("monitorrequest: no request keyring")

var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// Header is one custom header.
type Header struct {
	Name  string `json:"n"`
	Value string `json:"v"`
}

// Secrets is the sealed part of a request.
type Secrets struct {
	Headers []Header `json:"h,omitempty"`
	Body    string   `json:"b,omitempty"`
}

// Empty reports whether there is nothing to seal.
func (s Secrets) Empty() bool { return len(s.Headers) == 0 && s.Body == "" }

// HTTPHeader returns the headers in the form the prober takes.
func (s Secrets) HTTPHeader() http.Header {
	if len(s.Headers) == 0 {
		return nil
	}
	h := make(http.Header, len(s.Headers))
	for _, x := range s.Headers {
		h.Add(x.Name, x.Value)
	}
	return h
}

// Names returns the header names in order.
func (s Secrets) Names() []string {
	out := make([]string, len(s.Headers))
	for i, h := range s.Headers {
		out[i] = h.Name
	}
	return out
}

// ValidMethod reports whether m is allowed.
func ValidMethod(m string) bool { return slices.Contains(Methods, m) }

// AllowsBody reports whether a request with method m may carry a body.
func AllowsBody(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// SafeMethod reports whether m is read-only by convention; the others act on the target.
func SafeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// ValidateHeaders checks a header list and returns a message per problem, keyed by the index of
// the header ("headers[2].name") or "headers" for the list itself.
func ValidateHeaders(h []Header) map[string]string {
	problems := map[string]string{}
	if len(h) > MaxHeaders {
		problems["headers"] = fmt.Sprintf("at most %d headers", MaxHeaders)
		return problems
	}
	seen := map[string]bool{}
	for i, x := range h {
		key := fmt.Sprintf("headers[%d]", i)
		switch {
		case x.Name == "" || len(x.Name) > MaxHeaderNameBytes || !headerName.MatchString(x.Name):
			problems[key+".name"] = "is not a valid header name"
		case checks.DeniedHeader(x.Name):
			problems[key+".name"] = "cannot be set"
		case seen[strings.ToLower(x.Name)]:
			problems[key+".name"] = "is repeated"
		}
		seen[strings.ToLower(x.Name)] = true
		if len(x.Value) > MaxHeaderValueBytes {
			problems[key+".value"] = fmt.Sprintf("must be at most %d bytes", MaxHeaderValueBytes)
		} else if !validValue(x.Value) {
			problems[key+".value"] = "must not contain control characters"
		}
	}
	return problems
}

func validValue(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidateBody checks a body.
func ValidateBody(b string) string {
	if len(b) > MaxBodyBytes {
		return fmt.Sprintf("must be at most %d bytes", MaxBodyBytes)
	}
	if !utf8.ValidString(b) || strings.ContainsRune(b, 0) {
		return "must be text"
	}
	return ""
}

func aad(tenantID, monitorID uuid.UUID) []byte {
	return []byte("monitors.request:" + tenantID.String() + ":" + monitorID.String())
}

// Seal encrypts s for one monitor and returns it as base64 text for monitors.config.
func Seal(keys *crypto.Keyring, tenantID, monitorID uuid.UUID, s Secrets) (string, error) {
	if keys == nil {
		return "", ErrNoKeys
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("monitorrequest: marshal: %w", err)
	}
	sealed, err := keys.Encrypt(raw, aad(tenantID, monitorID))
	if err != nil {
		return "", fmt.Errorf("monitorrequest: seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts what Seal produced for the same monitor. The errors never contain values.
func Open(keys *crypto.Keyring, tenantID, monitorID uuid.UUID, sealed string) (Secrets, error) {
	if keys == nil {
		return Secrets{}, ErrNoKeys
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return Secrets{}, errors.New("monitorrequest: malformed")
	}
	plain, err := keys.Decrypt(raw, aad(tenantID, monitorID))
	if err != nil {
		return Secrets{}, fmt.Errorf("monitorrequest: open: %w", err)
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return Secrets{}, errors.New("monitorrequest: malformed")
	}
	return s, nil
}
