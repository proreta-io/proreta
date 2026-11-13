package domains

import (
	"context"
	"errors"
	"strings"
)

// Errors a DomainResolver returns.
var (
	// ErrNotFound: the resolver answered that the name or record does not exist (NXDOMAIN or
	// no data). Only this error makes a record Missing; anything else is Unknown.
	ErrNotFound = errors.New("domains: no such record")
)

// DomainResolver is the DNS the verification needs. It is declared here, by its consumer; the
// job passes an implementation with timeouts and answer caps, tests pass Fake. No implementation
// connects to the host itself.
type DomainResolver interface {
	// LookupTXT returns the TXT strings at name, ErrNotFound when there are none.
	LookupTXT(ctx context.Context, name string) ([]string, error)
	// LookupCNAME follows the CNAME chain at name and returns the final target without a trailing
	// dot, ErrNotFound when name has no CNAME.
	LookupCNAME(ctx context.Context, name string) (string, error)
}

// Probe asks the resolver about host and returns what it said: the TXT record at
// TXTName(host) must hold TXTValue(token), and the CNAME chain of host must end at cnameTarget.
func Probe(ctx context.Context, r DomainResolver, host, token, cnameTarget string) Facts {
	var f Facts
	txt, err := r.LookupTXT(ctx, TXTName(host))
	switch {
	case err == nil:
		f.TXT = Missing
		for _, v := range txt {
			if strings.TrimSpace(v) == TXTValue(token) {
				f.TXT = Present
				break
			}
		}
	case errors.Is(err, ErrNotFound):
		f.TXT = Missing
	}
	target, err := r.LookupCNAME(ctx, host)
	switch {
	case err == nil:
		f.CNAME = Missing
		if Normalize(target) == Normalize(cnameTarget) && cnameTarget != "" {
			f.CNAME = Present
		}
	case errors.Is(err, ErrNotFound):
		f.CNAME = Missing
	}
	return f
}
