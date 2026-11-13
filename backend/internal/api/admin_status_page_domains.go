package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/statuspages/domains"
)

func (s *server) AdminListStatusPageDomains(ctx context.Context, req gen.AdminListStatusPageDomainsRequestObject) (gen.AdminListStatusPageDomainsResponseObject, error) {
	if !s.cfg.StatusPageCustomDomains {
		return nil, errCustomDomainsDisabled
	}
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	pg := page(req.Params.Query, req.Params.Limit, req.Params.Offset)
	var states []string
	if req.Params.State != nil {
		for _, st := range *req.Params.State {
			states = append(states, string(st))
		}
	}
	list, err := s.admin.ListStatusPageDomains(ctx, a, pg, states)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListStatusPageDomains200JSONResponse{Items: make([]gen.AdminStatusPageDomain, 0, len(list.Items)), Total: list.Total,
		Limit: effectiveLimit(pg.Limit), Offset: pg.Offset}
	for _, d := range list.Items {
		out.Items = append(out.Items, toAdminStatusPageDomain(d))
	}
	return out, nil
}

func (s *server) AdminDisableStatusPageDomain(ctx context.Context, req gen.AdminDisableStatusPageDomainRequestObject) (gen.AdminDisableStatusPageDomainResponseObject, error) {
	d, err := s.setDomainDisabled(ctx, req.DomainId, req.Body.Reason, true)
	if err != nil {
		return nil, err
	}
	return gen.AdminDisableStatusPageDomain200JSONResponse(d), nil
}

func (s *server) AdminEnableStatusPageDomain(ctx context.Context, req gen.AdminEnableStatusPageDomainRequestObject) (gen.AdminEnableStatusPageDomainResponseObject, error) {
	d, err := s.setDomainDisabled(ctx, req.DomainId, req.Body.Reason, false)
	if err != nil {
		return nil, err
	}
	return gen.AdminEnableStatusPageDomain200JSONResponse(d), nil
}

func (s *server) setDomainDisabled(ctx context.Context, id gen.StatusPageDomainId, reason string, disabled bool) (gen.AdminStatusPageDomain, error) {
	if !s.cfg.StatusPageCustomDomains {
		return gen.AdminStatusPageDomain{}, errCustomDomainsDisabled
	}
	a, err := s.adminActor(ctx)
	if err != nil {
		return gen.AdminStatusPageDomain{}, err
	}
	d, err := s.admin.SetStatusPageDomainDisabled(ctx, a, id, disabled, reason)
	if err != nil {
		return gen.AdminStatusPageDomain{}, err
	}
	return toAdminStatusPageDomain(d), nil
}

func toAdminStatusPageDomain(d admin.StatusPageDomain) gen.AdminStatusPageDomain {
	out := gen.AdminStatusPageDomain{Id: d.ID, Host: d.Host, HostUnicode: domains.Display(d.Host), State: gen.StatusPageDomainState(d.State),
		Primary: d.Primary, Disabled: d.DisabledAt != nil, DisabledAt: d.DisabledAt, OrganizationId: d.OrganizationID,
		OrganizationSlug: d.OrganizationSlug, StatusPageId: d.StatusPageID, StatusPageSlug: d.StatusPageSlug,
		VerifiedAt: d.VerifiedAt, LastCheckedAt: d.LastCheckedAt, CreatedAt: d.CreatedAt}
	if d.LastError != nil {
		e := gen.StatusPageDomainError(*d.LastError)
		out.LastError = &e
	}
	return out
}
