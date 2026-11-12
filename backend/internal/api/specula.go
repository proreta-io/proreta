package api

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/specula"
)

func (s *server) speculaActor(ctx context.Context) (specula.Actor, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return specula.Actor{}, err
	}
	return specula.Actor{TenantID: p.TenantID, UserID: p.UserID, IP: httpx.ClientIP(httpRequest(ctx), s.cfg.TrustedProxyHops)}, nil
}

// canManageSpecula reports whether the actor sees every Specula of the organization.
func canManageSpecula(a authz.Actor) bool { return authz.Has(a.Role, "specula.manage") }

// visible reports whether the actor may see sp: managers see all; other roles see a Specula only
// when one of the projects they see may use it.
func visibleSpecula(a authz.Actor, sp specula.Specula) bool {
	if canManageSpecula(a) {
		return true
	}
	if sp.RevokedAt != nil {
		return false
	}
	if sp.AllProjects {
		return true
	}
	for _, p := range sp.ProjectIDs {
		if a.Sees(p) {
			return true
		}
	}
	return false
}

func toSpecula(sp specula.Specula, manager bool, now time.Time) gen.Specula {
	ids := sp.ProjectIDs
	if !manager || ids == nil {
		ids = []uuid.UUID{}
	}
	return gen.Specula{
		Id: sp.ID, Name: sp.Name, AllProjects: sp.AllProjects, ProjectIds: ids, Status: gen.SpeculaStatus(sp.Status(now)),
		Version: sp.Version, Hostname: sp.Hostname, LastSeenAt: sp.LastSeenAt, CreatedAt: sp.CreatedAt, RevokedAt: sp.RevokedAt,
	}
}

func (s *server) ListSpecula(ctx context.Context, req gen.ListSpeculaRequestObject) (gen.ListSpeculaResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	access, err := s.access(ctx)
	if err != nil {
		return nil, err
	}
	// A project filter answers 404 for a project the caller does not see.
	if _, err := s.projectFilter(ctx, req.Params.ProjectId); err != nil {
		return nil, err
	}
	list, err := s.specula.List(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	manager := canManageSpecula(access)
	includeRevoked := manager && req.Params.IncludeRevoked != nil && *req.Params.IncludeRevoked
	now := time.Now()
	items := make([]gen.Specula, 0, len(list))
	for _, sp := range list {
		switch {
		case !visibleSpecula(access, sp):
			continue
		case sp.RevokedAt != nil && !includeRevoked:
			continue
		case req.Params.ProjectId != nil && !sp.UsableBy(*req.Params.ProjectId):
			continue
		}
		items = append(items, toSpecula(sp, manager, now))
	}
	return gen.ListSpecula200JSONResponse{Items: items}, nil
}

func (s *server) GetSpecula(ctx context.Context, req gen.GetSpeculaRequestObject) (gen.GetSpeculaResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	access, err := s.access(ctx)
	if err != nil {
		return nil, err
	}
	sp, err := s.specula.Get(ctx, p.TenantID, req.SpeculaId)
	if err != nil {
		return nil, err
	}
	if !visibleSpecula(access, sp) {
		return nil, specula.ErrNotFound
	}
	return gen.GetSpecula200JSONResponse(toSpecula(sp, canManageSpecula(access), time.Now())), nil
}

func (s *server) CreateSpecula(ctx context.Context, req gen.CreateSpeculaRequestObject) (gen.CreateSpeculaResponseObject, error) {
	a, err := s.speculaActor(ctx)
	if err != nil {
		return nil, err
	}
	in := specula.CreateInput{Name: req.Body.Name, AllProjects: true}
	if req.Body.AllProjects != nil {
		in.AllProjects = *req.Body.AllProjects
	}
	if req.Body.ProjectIds != nil {
		in.ProjectIDs = *req.Body.ProjectIds
	}
	sp, token, err := s.specula.Create(ctx, a, in)
	if err != nil {
		return nil, err
	}
	return gen.CreateSpecula201JSONResponse{Specula: toSpecula(sp, true, time.Now()), Token: token}, nil
}

func (s *server) UpdateSpecula(ctx context.Context, req gen.UpdateSpeculaRequestObject) (gen.UpdateSpeculaResponseObject, error) {
	a, err := s.speculaActor(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	sp, err := s.specula.Update(ctx, a, req.SpeculaId, specula.UpdateInput{Name: b.Name, AllProjects: b.AllProjects, ProjectIDs: b.ProjectIds})
	if err != nil {
		return nil, err
	}
	return gen.UpdateSpecula200JSONResponse(toSpecula(sp, true, time.Now())), nil
}

func (s *server) RevokeSpecula(ctx context.Context, req gen.RevokeSpeculaRequestObject) (gen.RevokeSpeculaResponseObject, error) {
	a, err := s.speculaActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.specula.Revoke(ctx, a, req.SpeculaId); err != nil {
		return nil, err
	}
	return gen.RevokeSpecula204Response{}, nil
}
