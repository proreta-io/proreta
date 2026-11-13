package statuspages

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

// SystemResolver is a minimal domains.DomainResolver over the system resolver, for the api's
// synchronous "verify now". It only asks DNS and never connects to the host. The verification job
// has its own resolver with answer caps and may replace this one.
type SystemResolver struct {
	// R is the resolver to use; nil means net.DefaultResolver.
	R *net.Resolver
}

const (
	lookupTimeout = 3 * time.Second
	// maxTXTRecords and maxTXTLength cap what one answer may feed into the checks.
	maxTXTRecords = 16
	maxTXTLength  = 512
)

func (s SystemResolver) resolver() *net.Resolver {
	if s.R != nil {
		return s.R
	}
	return net.DefaultResolver
}

// notFound reports a definite "no such record": NXDOMAIN or an empty answer. A timeout, SERVFAIL
// or any other error is not one, so it can never count as a missing record.
func notFound(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

// LookupTXT implements domains.DomainResolver.
func (s SystemResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	txt, err := s.resolver().LookupTXT(ctx, name)
	if err != nil {
		if notFound(err) {
			return nil, domains.ErrNotFound
		}
		return nil, err
	}
	if len(txt) > maxTXTRecords {
		txt = txt[:maxTXTRecords]
	}
	for i, v := range txt {
		if len(v) > maxTXTLength {
			txt[i] = v[:maxTXTLength]
		}
	}
	return txt, nil
}

// LookupCNAME implements domains.DomainResolver. The standard resolver answers a name without a
// CNAME with the name itself, which is "no CNAME" here.
func (s SystemResolver) LookupCNAME(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	target, err := s.resolver().LookupCNAME(ctx, name)
	if err != nil {
		if notFound(err) {
			return "", domains.ErrNotFound
		}
		return "", err
	}
	target = strings.TrimSuffix(strings.ToLower(target), ".")
	if target == "" || target == name {
		return "", domains.ErrNotFound
	}
	return target, nil
}
