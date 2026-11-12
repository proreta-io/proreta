package monitors

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// What an http monitor sends. The database enforces the same limits (monitors_method_check and
// the constraints next to it).
const (
	DefaultMethod      = "GET"
	DefaultContentType = "application/json"
	// MaxRequestBodyBytes is the largest request body.
	MaxRequestBodyBytes = 16384

	fieldMethod      = "http.method"
	fieldRequestBody = "http.requestBody"
	fieldContentType = "http.contentType"
)

var (
	// Methods lists the accepted request methods.
	Methods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}
	// ContentTypes lists the accepted content types of a request body.
	ContentTypes = []string{"application/json", "text/plain", "application/x-www-form-urlencoded", "application/xml"}
	bodyMethods  = []string{"POST", "PUT", "PATCH", "DELETE"}
)

// ValidateMethod returns a message when m is not an accepted method.
func ValidateMethod(m string) string {
	if !slices.Contains(Methods, m) {
		return "must be one of GET, HEAD, POST, PUT, PATCH or DELETE"
	}
	return ""
}

// ValidateContentType returns a message when ct is not an accepted content type.
func ValidateContentType(ct string) string {
	if !slices.Contains(ContentTypes, ct) {
		return "must be one of application/json, text/plain, application/x-www-form-urlencoded or application/xml"
	}
	return ""
}

// ValidateRequestBody returns a message when body is too large or not text.
func ValidateRequestBody(body string) string {
	if len(body) > MaxRequestBodyBytes {
		return "must be at most 16384 bytes"
	}
	if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return "must be valid UTF-8 text without NUL characters"
	}
	return ""
}

// checkRequest validates the effective request of an http monitor, adds field errors to verr
// and returns the content type to store: empty without a body, the default when a body has none.
func checkRequest(verr *validation.Error, method, body, contentType string) string {
	if msg := ValidateMethod(method); msg != "" {
		verr.Add(fieldMethod, msg)
	}
	if msg := ValidateRequestBody(body); msg != "" {
		verr.Add(fieldRequestBody, msg)
	} else if body != "" && ValidateMethod(method) == "" && !slices.Contains(bodyMethods, method) {
		verr.Add(fieldRequestBody, "is not allowed with GET or HEAD")
	}
	if body == "" {
		return ""
	}
	if contentType == "" {
		return DefaultContentType
	}
	if msg := ValidateContentType(contentType); msg != "" {
		verr.Add(fieldContentType, msg)
	}
	return contentType
}
