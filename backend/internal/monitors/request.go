package monitors

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
)

const (
	fieldMethod  = "http.method"
	fieldHeaders = "http.headers"
	fieldBody    = "http.body"
)

// HeaderInput is one custom header in a request. On update a nil Value keeps the value the
// monitor already has for that name.
type HeaderInput struct {
	Name  string
	Value *string
}

// WithRequestKeys sets the keyring that seals header values and bodies. Without it a monitor
// can still choose a method but cannot carry headers or a body.
func (s *Service) WithRequestKeys(k *crypto.Keyring) *Service {
	s.requestKeys = k
	return s
}

// planRequest applies the method, headers and body of h to cur (a zero httpConfig on create).
// It returns the new config without the sealed value, the secrets to seal when dirty is true,
// and the changed field names. Values never reach verr or the field names.
func (s *Service) planRequest(verr *validation.Error, cur httpConfig, tenantID, id uuid.UUID, h *HTTPSettings) (cfg httpConfig, sec monitorrequest.Secrets, dirty bool, changed []string, err error) {
	cfg = cur
	method := cfg.Method
	if method == "" {
		method = monitorrequest.Methods[0]
	}
	if h.Method != nil {
		if !monitorrequest.ValidMethod(*h.Method) {
			verr.Add(fieldMethod, `must be one of GET, HEAD, POST, PUT, PATCH, DELETE`)
		} else {
			changed = appendIf(changed, fieldMethod, *h.Method != method)
			method = *h.Method
		}
	}
	cfg.Method = ""
	if method != monitorrequest.Methods[0] {
		cfg.Method = method
	}

	bodySet := cfg.BodySet
	if h.Headers != nil || h.Body != nil {
		if cur.Request != "" {
			if sec, err = monitorrequest.Open(s.requestKeys, tenantID, id, cur.Request); err != nil {
				return cfg, sec, false, nil, fmt.Errorf("monitor request: %w", err)
			}
		}
	}
	old := sec
	if h.Headers != nil {
		next := make([]monitorrequest.Header, 0, len(*h.Headers))
		for i, in := range *h.Headers {
			value := ""
			switch {
			case in.Value != nil:
				value = *in.Value
			default:
				idx := slices.IndexFunc(old.Headers, func(x monitorrequest.Header) bool { return strings.EqualFold(x.Name, in.Name) })
				if idx < 0 {
					verr.Add(fmt.Sprintf("%s[%d].value", fieldHeaders, i), "is required for a new header")
				} else {
					value = old.Headers[idx].Value
				}
			}
			next = append(next, monitorrequest.Header{Name: in.Name, Value: value})
		}
		problems := monitorrequest.ValidateHeaders(next)
		for _, k := range sortedKeys(problems) {
			verr.Add("http."+k, problems[k])
		}
		if len(problems) == 0 {
			changed = appendIf(changed, fieldHeaders, !slices.Equal(next, old.Headers))
			sec.Headers = next
		}
	}
	if h.Body != nil {
		if msg := monitorrequest.ValidateBody(*h.Body); msg != "" {
			verr.Add(fieldBody, msg)
		} else {
			changed = appendIf(changed, fieldBody, *h.Body != old.Body)
			sec.Body = *h.Body
			bodySet = *h.Body != ""
		}
	}
	if bodySet && !monitorrequest.AllowsBody(method) {
		verr.Add(fieldBody, "is not allowed with GET or HEAD")
	}
	if (slices.Contains(changed, fieldHeaders) || slices.Contains(changed, fieldBody)) && !sec.Empty() && s.requestKeys == nil {
		verr.Add(fieldHeaders, "headers and body are not available on this installation")
	}
	dirty = slices.Contains(changed, fieldHeaders) || slices.Contains(changed, fieldBody)
	if dirty {
		cfg.HeaderNames, cfg.BodySet = sec.Names(), bodySet
		if len(cfg.HeaderNames) == 0 {
			cfg.HeaderNames = nil
		}
	}
	return cfg, sec, dirty, changed, nil
}

// sealRequest stores sec sealed in cfg, or clears the sealed value when there is nothing to
// keep.
func (s *Service) sealRequest(cfg *httpConfig, tenantID, id uuid.UUID, sec monitorrequest.Secrets) error {
	if sec.Empty() {
		cfg.Request = ""
		return nil
	}
	sealed, err := monitorrequest.Seal(s.requestKeys, tenantID, id, sec)
	if err != nil {
		if errors.Is(err, monitorrequest.ErrNoKeys) {
			return err
		}
		return fmt.Errorf("monitor request: %w", err)
	}
	cfg.Request = sealed
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
