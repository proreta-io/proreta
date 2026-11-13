package domains

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// TXTPrefix is the label put before the host for the ownership record.
const TXTPrefix = "_proreta-status."

const txtValuePrefix = "proreta-verify="

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NewToken returns a new ownership token: 16 random bytes as 32 lowercase hex characters. The token
// is public by design, it is published in DNS.
func NewToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidToken reports whether s has the form NewToken returns.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// TXTName is the DNS name of the ownership record of host.
func TXTName(host string) string { return TXTPrefix + host }

// TXTValue is the value of the ownership record for token.
func TXTValue(token string) string { return txtValuePrefix + token }
