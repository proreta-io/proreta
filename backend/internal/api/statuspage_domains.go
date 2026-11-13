package api

import (
	"context"
	"errors"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
)

// errCustomDomainsDisabled answers every status page domain operation while
// PRORETA_STATUS_PAGE_CUSTOM_DOMAINS is off.
var errCustomDomainsDisabled = errors.New("custom domains disabled")

// errCustomDomainsNotWired is what an operation returns with the switch on but no service behind
// it yet; it is a 500 on purpose, so nobody mistakes the half-built feature for a working one.
var errCustomDomainsNotWired = errors.New("custom domains: service not wired")

// domainsGate is the first step of every domain operation: it refuses with 404 while the feature
// is off, and for now also when it is on, until the service behind these operations exists.
func (s *server) domainsGate() error {
	if !s.cfg.StatusPageCustomDomains {
		return errCustomDomainsDisabled
	}
	return errCustomDomainsNotWired
}

func (s *server) ListStatusPageDomains(_ context.Context, _ gen.ListStatusPageDomainsRequestObject) (gen.ListStatusPageDomainsResponseObject, error) {
	return nil, s.domainsGate()
}

func (s *server) AddStatusPageDomain(_ context.Context, _ gen.AddStatusPageDomainRequestObject) (gen.AddStatusPageDomainResponseObject, error) {
	return nil, s.domainsGate()
}

func (s *server) VerifyStatusPageDomain(_ context.Context, _ gen.VerifyStatusPageDomainRequestObject) (gen.VerifyStatusPageDomainResponseObject, error) {
	return nil, s.domainsGate()
}

func (s *server) SetPrimaryStatusPageDomain(_ context.Context, _ gen.SetPrimaryStatusPageDomainRequestObject) (gen.SetPrimaryStatusPageDomainResponseObject, error) {
	return nil, s.domainsGate()
}

func (s *server) RemoveStatusPageDomain(_ context.Context, _ gen.RemoveStatusPageDomainRequestObject) (gen.RemoveStatusPageDomainResponseObject, error) {
	return nil, s.domainsGate()
}
