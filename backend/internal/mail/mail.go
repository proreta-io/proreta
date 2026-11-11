// Package mail is outgoing email: the templates, the SMTP sender and the queue (the
// mail_outbox table) that the api writes in the transaction of the state change and cmd/jobs
// delivers later with retries. Email is optional; until the platform admin has configured and tested a server nothing is
// queued and every flow works as before, showing its link in the UI instead. The settings live
// in the database and are edited in the admin panel (email_settings), so they change without a
// redeploy.
//
// A message holds a recipient and a link with a bearer token, so nothing here logs a body, an
// address or a token: failures are reported as a kind and an SMTP code only.
package mail

import (
	"errors"
	"fmt"
	"strings"
)

// Kind names a message template. The value is stored in mail_outbox.kind.
type Kind string

// Kinds of message.
const (
	// KindTest is the message the admin panel's "Send test email" queues to check the SMTP
	// settings. It is sent once, never retried.
	KindTest Kind = "test"
	// KindVerifyEmail asks the recipient to confirm an address by opening a one-time link.
	KindVerifyEmail Kind = "verify_email"
	// KindInvitation carries an organization invitation link to the person invited.
	KindInvitation Kind = "invitation"
	// KindPasswordReset carries the one-time link to choose a new password.
	KindPasswordReset Kind = "password_reset"
)

// Message is one email ready to send. Both bodies are always set.
type Message struct {
	// To is a bare address (no display name).
	To      string
	Subject string
	Text    string
	HTML    string
}

// Error kinds of SendError.Kind; they end up in mail_outbox.last_error and metrics.
const (
	KindConnect    = "connect"
	KindRefused    = "refused"
	KindDNS        = "dns"
	KindBlocked    = "blocked_address"
	KindTLS        = "tls"
	KindAuth       = "auth"
	KindRecipient  = "recipient_rejected"
	KindRejected   = "rejected"
	KindTimeout    = "timeout"
	KindOther      = "other"
	KindBadMessage = "bad_message"
)

// SendError is a failed send. It deliberately carries no server text: a reply can echo the
// recipient address or the message.
type SendError struct {
	Kind string
	// Code is the SMTP reply code, or 0.
	Code int
	// Permanent means retrying the same message cannot help (a 5xx reply to the recipient or the
	// data, or a message that cannot be built). Connection, TLS and login failures are not
	// permanent: they are configuration or availability problems that may be fixed meanwhile.
	Permanent bool
}

func (e *SendError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("mail: send failed: %s (smtp %d)", e.Kind, e.Code)
	}
	return "mail: send failed: " + e.Kind
}

// ErrorKind returns the label stored for a send error: the kind, with the SMTP code when there
// is one. Anything that is not a *SendError is "other".
func ErrorKind(err error) string {
	var se *SendError
	if !errors.As(err, &se) {
		return KindOther
	}
	if se.Code != 0 {
		return fmt.Sprintf("%s %d", se.Kind, se.Code)
	}
	return se.Kind
}

// IsPermanent reports whether err is a *SendError that retrying cannot fix.
func IsPermanent(err error) bool {
	var se *SendError
	return errors.As(err, &se) && se.Permanent
}

func domainOf(address string) string {
	_, d, _ := strings.Cut(address, "@")
	return d
}
