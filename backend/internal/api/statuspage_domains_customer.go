package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

// domainsEnabled answers errCustomDomainsDisabled while PRORETA_STATUS_PAGE_CUSTOM_DOMAINS is off.
func (s *server) domainsEnabled() error {
	if !s.cfg.StatusPageCustomDomains {
		return errCustomDomainsDisabled
	}
	return nil
}

// domainActor is pageActor behind the feature switch.
func (s *server) domainActor(ctx context.Context) (statuspages.Actor, error) {
	if err := s.domainsEnabled(); err != nil {
		return statuspages.Actor{}, err
	}
	return s.pageActor(ctx)
}

func (s *server) ListStatusPageDomains(ctx context.Context, req gen.ListStatusPageDomainsRequestObject) (gen.ListStatusPageDomainsResponseObject, error) {
	a, err := s.domainActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.pages.ListDomains(ctx, a, req.StatusPageId)
	if err != nil {
		return nil, err
	}
	out := gen.ListStatusPageDomains200JSONResponse{Items: make([]gen.StatusPageDomain, len(list))}
	for i, d := range list {
		out.Items[i] = toStatusPageDomain(d)
	}
	return out, nil
}

func (s *server) AddStatusPageDomain(ctx context.Context, req gen.AddStatusPageDomainRequestObject) (gen.AddStatusPageDomainResponseObject, error) {
	a, err := s.domainActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.pages.AddDomain(ctx, a, req.StatusPageId, req.Body.Host)
	if err != nil {
		return nil, err
	}
	return gen.AddStatusPageDomain201JSONResponse(toStatusPageDomain(d)), nil
}

func (s *server) VerifyStatusPageDomain(ctx context.Context, req gen.VerifyStatusPageDomainRequestObject) (gen.VerifyStatusPageDomainResponseObject, error) {
	a, err := s.domainActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.pages.VerifyDomain(ctx, a, req.StatusPageId, req.DomainId)
	if err != nil {
		return nil, err
	}
	return gen.VerifyStatusPageDomain202JSONResponse(toStatusPageDomain(d)), nil
}

func (s *server) SetPrimaryStatusPageDomain(ctx context.Context, req gen.SetPrimaryStatusPageDomainRequestObject) (gen.SetPrimaryStatusPageDomainResponseObject, error) {
	a, err := s.domainActor(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.pages.SetPrimaryDomain(ctx, a, req.StatusPageId, req.DomainId)
	if err != nil {
		return nil, err
	}
	return gen.SetPrimaryStatusPageDomain200JSONResponse(toStatusPageDomain(d)), nil
}

func (s *server) RemoveStatusPageDomain(ctx context.Context, req gen.RemoveStatusPageDomainRequestObject) (gen.RemoveStatusPageDomainResponseObject, error) {
	a, err := s.domainActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.pages.RemoveDomain(ctx, a, req.StatusPageId, req.DomainId); err != nil {
		return nil, err
	}
	return gen.RemoveStatusPageDomain204Response{}, nil
}

func toStatusPageDomain(d statuspages.Domain) gen.StatusPageDomain {
	var lastErr *gen.StatusPageDomainError
	if d.LastError != nil {
		e := gen.StatusPageDomainError(*d.LastError)
		lastErr = &e
	}
	return gen.StatusPageDomain{
		Id: d.ID, Host: d.Host, HostUnicode: domains.Display(d.Host), State: gen.StatusPageDomainState(d.State), Primary: d.Primary, Disabled: d.Disabled,
		Verification: gen.StatusPageDomainVerification{TxtName: domains.TXTName(d.Host), TxtValue: domains.TXTValue(d.Token), CnameTarget: d.CNAMETarget},
		VerifiedAt:   d.VerifiedAt, LastCheckedAt: d.LastCheckedAt, LastError: lastErr, ExpiresAt: d.ExpiresAt(), CreatedAt: d.CreatedAt,
	}
}
