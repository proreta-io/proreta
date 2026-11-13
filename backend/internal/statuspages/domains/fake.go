package domains

import (
	"context"
	"errors"
	"time"
)

// ErrTemporary is what Fake returns for a name marked as failing: a timeout or SERVFAIL.
var ErrTemporary = errors.New("domains: temporary DNS error")

// maxCNAMEHops bounds a CNAME chain, as the real resolver does.
const maxCNAMEHops = 8

// Fake is a map-backed DomainResolver for tests: TXT records, CNAME chains, loops, missing
// names, temporary errors and slow answers, no network. Names are lowercase without a trailing dot.
type Fake struct {
	TXT map[string][]string
	// CNAME maps a name to the name it points at; a chain is followed up to eight hops, a longer
	// one or a loop is ErrTemporary.
	CNAME map[string]string
	// Fail lists names whose lookups return ErrTemporary.
	Fail map[string]bool
	// Delay is how long every lookup takes; it returns the context's error when that ends first.
	Delay time.Duration
}

func (f *Fake) wait(ctx context.Context) error {
	if f.Delay == 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(f.Delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LookupTXT implements DomainResolver.
func (f *Fake) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.Fail[name] {
		return nil, ErrTemporary
	}
	v, ok := f.TXT[name]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

// LookupCNAME implements DomainResolver.
func (f *Fake) LookupCNAME(ctx context.Context, name string) (string, error) {
	if err := f.wait(ctx); err != nil {
		return "", err
	}
	cur := name
	for hop := 0; hop <= maxCNAMEHops; hop++ {
		if f.Fail[cur] {
			return "", ErrTemporary
		}
		next, ok := f.CNAME[cur]
		if !ok {
			if cur == name {
				return "", ErrNotFound
			}
			return cur, nil
		}
		cur = next
	}
	return "", ErrTemporary
}
