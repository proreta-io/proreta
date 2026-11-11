package mail

import (
	"errors"
	netmail "net/mail"
	"strings"
)

// maxAddressLen is the longest address SMTP accepts (RFC 5321 path limit).
const maxAddressLen = 254

// parseBare validates a recipient: one plain address, no display name, no comment, no line
// break. It returns the address in the form to put in a header.
func parseBare(raw string) (string, error) {
	if raw == "" || len(raw) > maxAddressLen || strings.ContainsAny(raw, "\r\n\x00 <>,;\"") {
		return "", errors.New("mail: invalid recipient")
	}
	a, err := netmail.ParseAddress(raw)
	if err != nil || a.Name != "" || a.Address != raw || !strings.Contains(raw, "@") {
		return "", errors.New("mail: invalid recipient")
	}
	return a.Address, nil
}

// ValidRecipient reports whether raw is a plain address that can receive mail.
func ValidRecipient(raw string) bool {
	_, err := parseBare(raw)
	return err == nil
}
