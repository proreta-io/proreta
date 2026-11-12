package monitors

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/proreta-io/proreta/backend/internal/safehttp"
)

const (
	maxNameRunes = 80
)

// Fixed check settings of http monitors: every check runs once a minute with a 10 s deadline.
// The database enforces them too (the monitors_mvp_* constraints).
const (
	IntervalSeconds         = 60
	TimeoutSeconds          = 10
	DefaultFailureThreshold = 2
	DefaultExpectedStatus   = "2xx"
)

// Heartbeat bounds; the database enforces them too (monitors_heartbeat_check).
const (
	MinExpectedIntervalSeconds     = 60
	MaxExpectedIntervalSeconds     = 86400
	DefaultExpectedIntervalSeconds = 300
	MaxGraceSeconds                = 3600
	DefaultGraceSeconds            = 60
)

var expectedRegex = regexp.MustCompile(`^(2xx|2[0-9]{2}|[45][0-9]{2})$`)

// ValidateURL checks a monitor target and returns it normalised (lowercase scheme and host),
// or a message for the user; see safehttp.ValidatePublicHTTPSURL.
func ValidateURL(raw string) (string, string) {
	return safehttp.ValidatePublicHTTPSURL(raw)
}

// ValidateName trims a monitor name and checks it, returning a message for the user.
func ValidateName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "is required"
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", "must be at most 80 characters"
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", "must not contain control characters"
		}
		// Format characters (bidi overrides, zero-width joiners and spaces, the BOM) let a name
		// render differently from what it contains, in the dashboard and in Slack. This also
		// rejects emoji joined with U+200D.
		if unicode.Is(unicode.Cf, r) {
			return "", "must not contain invisible formatting characters"
		}
	}
	return name, ""
}

// ValidateExpectedStatus accepts "2xx", one status code from 200 to 299 or one from 400 to 599.
func ValidateExpectedStatus(s string) string {
	if !expectedRegex.MatchString(s) {
		return `must be "2xx", a status code from 200 to 299 or one from 400 to 599`
	}
	return ""
}

// ValidateExpectedInterval checks a heartbeat's expected ping interval in seconds.
func ValidateExpectedInterval(n int) string {
	if n < MinExpectedIntervalSeconds || n > MaxExpectedIntervalSeconds {
		return "must be between 60 and 86400 seconds"
	}
	return ""
}

// ValidateGrace checks a heartbeat's grace period in seconds.
func ValidateGrace(n int) string {
	if n < 0 || n > MaxGraceSeconds {
		return "must be between 0 and 3600 seconds"
	}
	return ""
}
