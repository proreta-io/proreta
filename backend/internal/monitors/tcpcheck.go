package monitors

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

// Limits of the send and expect settings of a tcp monitor.
const (
	MaxTCPSendBytes           = 4096
	MaxTCPExpectChars         = 512
	MinReadTimeoutSeconds     = 1
	MaxReadTimeoutSeconds     = TimeoutSeconds
	DefaultReadTimeoutSeconds = 5
	MaxResponseBytesLimit     = 64 << 10
	DefaultMaxResponseBytes   = 4096
	ExpectContains            = "contains"
	ExpectRegex               = "regex"
	SendHex                   = "hex"
	SendBase64                = "base64"
	SendText                  = "text"

	fieldTCPSend     = "tcp.send"
	fieldTCPExpect   = "tcp.expect"
	fieldReadTimeout = "tcp.readTimeoutSeconds"
	fieldMaxResponse = "tcp.maxResponseBytes"
	msgHostMoved     = "the host or port changed: send the value again, or remove it"
	msgNoSendKeys    = "send is not available on this installation"
)

// TCPSend is the bytes a tcp check writes after it connects. On update an empty Value removes them.
type TCPSend struct {
	Encoding string // hex, base64 or text
	Value    string
}

// TCPExpect is what the reply must contain. On update an empty Value removes the rule.
type TCPExpect struct {
	Mode  string // contains or regex
	Value string
}

// ExpectRule is the stored form of TCPExpect.
type ExpectRule struct {
	Mode  string `json:"mode"`
	Value string `json:"value"`
}

// planTCPCheck applies the send, expect and read limits of t to cfg and returns the bytes to
// send (the new ones when sendChanged, otherwise nil) and the changed field names; the bytes
// never reach verr or the names. hostMoved is true when an update changed the host or port: a
// stored send is write-only, so it must then be sent again or removed, or an editor who cannot
// read it could aim it at their own host.
func planTCPCheck(verr *validation.Error, cfg *tcpConfig, t *TCPSettings, hostMoved bool) (send []byte, sendChanged bool, changed []string) {
	if s := t.Send; s != nil {
		switch b, remove, msg := decodeSend(*s); {
		case msg != "":
			verr.Add(fieldTCPSend, msg)
		case remove:
			changed = appendIf(changed, fieldTCPSend, cfg.SendSet)
			sendChanged, cfg.SendSet = cfg.SendSet, false
		default:
			changed, sendChanged, cfg.SendSet = append(changed, fieldTCPSend), true, true
			send = b
		}
	} else if hostMoved && cfg.SendSet {
		verr.Add(fieldTCPSend, msgHostMoved)
	}

	if e := t.Expect; e != nil {
		switch rule, remove, msg := checkExpect(*e); {
		case msg != "":
			verr.Add(fieldTCPExpect, msg)
		case remove:
			changed = appendIf(changed, fieldTCPExpect, cfg.Expect != nil)
			cfg.Expect = nil
		default:
			changed = appendIf(changed, fieldTCPExpect, cfg.Expect == nil || *cfg.Expect != rule)
			cfg.Expect = &rule
		}
	}

	active := cfg.SendSet || cfg.Expect != nil
	if cfg.SendSet && cfg.Expect == nil && verr.Err() == nil {
		verr.Add(fieldTCPExpect, "is required when send is set")
	}
	if !active {
		if t.ReadTimeoutSeconds != nil {
			verr.Add(fieldReadTimeout, "needs send or expect")
		}
		if t.MaxResponseBytes != nil {
			verr.Add(fieldMaxResponse, "needs send or expect")
		}
		changed = appendIf(changed, fieldReadTimeout, cfg.ReadTimeoutSeconds != 0)
		changed = appendIf(changed, fieldMaxResponse, cfg.MaxResponseBytes != 0)
		cfg.ReadTimeoutSeconds, cfg.MaxResponseBytes = 0, 0
		return send, sendChanged, changed
	}
	if cfg.ReadTimeoutSeconds == 0 {
		cfg.ReadTimeoutSeconds = DefaultReadTimeoutSeconds
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if v := t.ReadTimeoutSeconds; v != nil {
		if *v < MinReadTimeoutSeconds || *v > MaxReadTimeoutSeconds {
			verr.Add(fieldReadTimeout, fmt.Sprintf("must be between %d and %d seconds", MinReadTimeoutSeconds, MaxReadTimeoutSeconds))
		} else {
			changed = appendIf(changed, fieldReadTimeout, *v != cfg.ReadTimeoutSeconds)
			cfg.ReadTimeoutSeconds = *v
		}
	}
	if v := t.MaxResponseBytes; v != nil {
		if *v < 1 || *v > MaxResponseBytesLimit {
			verr.Add(fieldMaxResponse, fmt.Sprintf("must be between 1 and %d bytes", MaxResponseBytesLimit))
		} else {
			changed = appendIf(changed, fieldMaxResponse, *v != cfg.MaxResponseBytes)
			cfg.MaxResponseBytes = *v
		}
	}
	if r := cfg.Expect; r != nil && r.Mode == ExpectContains && len(r.Value) > cfg.MaxResponseBytes && verr.Err() == nil {
		verr.Add(fieldTCPExpect, "is longer than the response size limit, so it could never match")
	}
	return send, sendChanged, changed
}

// decodeSend returns the bytes of s, or remove for an empty value, or a problem.
func decodeSend(s TCPSend) (b []byte, remove bool, msg string) {
	if s.Value == "" {
		return nil, true, ""
	}
	var err error
	switch s.Encoding {
	case SendHex:
		b, err = hex.DecodeString(strings.Join(strings.Fields(s.Value), ""))
	case SendBase64:
		// The browser's atob tolerates missing padding and spaces; so does the server.
		v := strings.Join(strings.Fields(s.Value), "")
		if b, err = base64.StdEncoding.DecodeString(v); err != nil {
			b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(v, "="))
		}
	case SendText:
		b = []byte(s.Value)
	default:
		return nil, false, "encoding must be hex, base64 or text"
	}
	if err != nil {
		return nil, false, "is not valid " + s.Encoding
	}
	if len(b) > MaxTCPSendBytes {
		return nil, false, fmt.Sprintf("must be at most %d bytes", MaxTCPSendBytes)
	}
	return b, false, ""
}

// checkExpect validates e; an empty value removes the rule.
func checkExpect(e TCPExpect) (rule ExpectRule, remove bool, msg string) {
	if e.Value == "" {
		return rule, true, ""
	}
	if len([]rune(e.Value)) > MaxTCPExpectChars {
		return rule, false, fmt.Sprintf("must be at most %d characters", MaxTCPExpectChars)
	}
	switch e.Mode {
	case ExpectContains:
	case ExpectRegex:
		if _, err := regexp.Compile(e.Value); err != nil {
			return rule, false, "is not a valid regular expression"
		}
	default:
		return rule, false, "mode must be contains or regex"
	}
	return ExpectRule(e), false, ""
}

// sealTCP rebuilds the sealed probe of a tcp monitor in cfg after planTCPCheck: the send bytes
// (send when sendChanged, otherwise the ones already stored, which are opened to keep them),
// the expect rule and the limits. A check with neither send nor expect stores nothing.
func (s *Service) sealTCP(cfg *tcpConfig, tenantID, id uuid.UUID, send []byte, sendChanged bool) error {
	if !cfg.SendSet && cfg.Expect == nil {
		cfg.Request = ""
		return nil
	}
	if s.requestKeys == nil {
		return monitorrequest.ErrNoKeys
	}
	if cfg.SendSet && !sendChanged {
		old, err := monitorrequest.Open(s.requestKeys, tenantID, id, cfg.Request)
		if err != nil || old.Probe == nil {
			return fmt.Errorf("monitor send: %w", errors.Join(err, errors.New("stored send is missing")))
		}
		send = old.Probe.Send
	}
	probe := monitorrequest.TCPProbe{Send: send, ReadTimeoutSeconds: cfg.ReadTimeoutSeconds, MaxResponseBytes: cfg.MaxResponseBytes}
	if cfg.Expect != nil {
		probe.ExpectMode, probe.ExpectValue = cfg.Expect.Mode, cfg.Expect.Value
	}
	sealed, err := monitorrequest.Seal(s.requestKeys, tenantID, id, monitorrequest.Secrets{Probe: &probe})
	if err != nil {
		return fmt.Errorf("monitor send: %w", err)
	}
	cfg.Request = sealed
	return nil
}
