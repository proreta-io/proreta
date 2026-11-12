package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Content types of a webhook channel (notification_channels.content_type).
const (
	ContentJSON = "json"
	ContentText = "text"
)

// Limits of webhook templates; the database enforces MaxTemplateBytes too.
const (
	MaxTemplateBytes = 8 << 10
	// MaxRenderedBytes caps the body sent to a webhook, whatever the values hold.
	MaxRenderedBytes = 16 << 10
)

// Placeholders lists every name a template may use, in display order.
var Placeholders = []string{
	"event", "status",
	"monitor.id", "monitor.name", "monitor.url", "monitor.type",
	"project.name", "organization.name",
	"incident.id", "incident.startedAt", "incident.resolvedAt", "incident.duration",
	"specula.id", "specula.name", "error.kind", "tls.expiresAt", "tls.daysLeft", "link",
}

// Template defaults, used when a channel is created without a template.
const (
	DefaultJSONTemplate = `{
  "event": "{{event}}",
  "status": "{{status}}",
  "monitor": {
    "name": "{{monitor.name}}",
    "url": "{{monitor.url}}"
  },
  "error": "{{error.kind}}",
  "link": "{{link}}"
}`
	DefaultTextTemplate = "[{{event}}] {{monitor.name}} is {{status}} {{monitor.url}} {{link}}"
)

// DefaultTemplate returns the template of a content type, "" for an unknown type.
func DefaultTemplate(contentType string) string {
	switch contentType {
	case ContentJSON:
		return DefaultJSONTemplate
	case ContentText:
		return DefaultTextTemplate
	}
	return ""
}

// placeholderPattern finds every {{...}} pair; the content is checked against Placeholders, so
// a malformed name is reported instead of being sent literally.
var placeholderPattern = regexp.MustCompile(`\{\{([^{}\n]*)\}\}`)

// ErrTemplateTooLarge means the rendered body exceeds MaxRenderedBytes.
var ErrTemplateTooLarge = errors.New("notify: rendered webhook body is too large")

// UnknownPlaceholderError names a placeholder that a template uses but that does not exist.
type UnknownPlaceholderError struct{ Name string }

func (e *UnknownPlaceholderError) Error() string {
	return fmt.Sprintf("unknown placeholder {{%s}}", e.Name)
}

// RenderTemplate replaces every {{name}} in tpl with data[name] (a missing value is empty). With
// ContentJSON each value is escaped as the inside of a JSON string, so no value can change the
// structure of the document; with ContentText values are inserted verbatim. It does not check
// that a JSON template yields valid JSON; ValidateTemplate does that with sample data.
func RenderTemplate(contentType, tpl string, data map[string]string) (string, error) {
	var unknown error
	out := placeholderPattern.ReplaceAllStringFunc(tpl, func(m string) string {
		name := strings.TrimSpace(m[2 : len(m)-2])
		if !slices.Contains(Placeholders, name) {
			if unknown == nil {
				unknown = &UnknownPlaceholderError{Name: printable(name)}
			}
			return ""
		}
		if contentType == ContentJSON {
			return jsonEscape(data[name])
		}
		return data[name]
	})
	if unknown != nil {
		return "", unknown
	}
	if len(out) > MaxRenderedBytes {
		return "", ErrTemplateTooLarge
	}
	return out, nil
}

// printable shortens a name taken from a template so it can be quoted back to the user.
func printable(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i >= 40 {
			b.WriteString("...")
			break
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// jsonEscape returns s escaped for the inside of a JSON string literal.
func jsonEscape(s string) string {
	if s == "" {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	// json.Marshal writes <, > and & as \u00XX; they are valid unescaped, and more readable.
	return jsonHTMLUnescaper.Replace(string(b[1 : len(b)-1]))
}

var jsonHTMLUnescaper = strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&")

// ValidateContentType checks a content type name.
func ValidateContentType(ct string) string {
	if ct != ContentJSON && ct != ContentText {
		return `must be "json" or "text"`
	}
	return ""
}

// ValidateTemplate returns a user-facing message when tpl cannot be used with contentType, or
// "" when it can: it is not empty, at most MaxTemplateBytes, valid UTF-8, uses only known
// placeholders, and, for JSON, renders with SampleData to a valid JSON document.
func ValidateTemplate(contentType, tpl string) string {
	if strings.TrimSpace(tpl) == "" {
		return "is required"
	}
	if len(tpl) > MaxTemplateBytes {
		return "must be at most 8192 bytes"
	}
	if !utf8.ValidString(tpl) {
		return "must be valid UTF-8 text"
	}
	out, err := RenderTemplate(contentType, tpl, SampleData("https://proreta.example"))
	var unk *UnknownPlaceholderError
	switch {
	case errors.As(err, &unk):
		return unk.Error()
	case errors.Is(err, ErrTemplateTooLarge):
		return "renders to more than 16384 bytes"
	case err != nil:
		return "cannot be rendered"
	}
	if contentType == ContentJSON {
		if msg := jsonProblem(out); msg != "" {
			return "is not valid JSON once rendered with sample data: " + msg
		}
	}
	return ""
}

// jsonProblem describes why s is not one valid JSON document, "" when it is.
func jsonProblem(s string) string {
	var v any
	err := json.Unmarshal([]byte(s), &v)
	if err == nil {
		return ""
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Sprintf("%s (at byte %d)", se.Error(), se.Offset)
	}
	return "invalid document"
}

// SampleData is the data of the test message and of the preview. The monitor name contains
// quotes on purpose, so a preview shows how values are escaped.
func SampleData(publicURL string) map[string]string {
	return map[string]string{
		"event":               EventTest,
		"status":              "down",
		"monitor.id":          "0190a8d0-0000-7000-8000-000000000001",
		"monitor.name":        `Example "API" monitor`,
		"monitor.url":         "https://example.com/health",
		"monitor.type":        "http",
		"project.name":        "Default",
		"organization.name":   "Example Inc.",
		"incident.id":         "0190a8d0-0000-7000-8000-000000000002",
		"incident.startedAt":  "2026-01-02T03:04:05Z",
		"incident.resolvedAt": "2026-01-02T03:09:05Z",
		"incident.duration":   "5m",
		"specula.id":          "0190a8d0-0000-7000-8000-000000000003",
		"specula.name":        "Office network",
		"error.kind":          "timeout",
		"tls.expiresAt":       "2026-02-01T00:00:00Z",
		"tls.daysLeft":        "30",
		"link":                strings.TrimRight(publicURL, "/") + "/monitors/0190a8d0-0000-7000-8000-000000000001",
	}
}
