package api

import (
	"context"

	"github.com/proreta-io/proreta/backend/internal/admin"
	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/plans"
)

func toAdminPlan(p admin.Plan) gen.AdminPlan {
	out := gen.AdminPlan{Key: p.Key, Name: p.Name, System: p.System, Organizations: p.Organizations, Deletable: p.Deletable,
		Limits: gen.ResourceCounts{Monitors: p.Limits.MaxMonitors, Heartbeats: p.Limits.MaxHeartbeats, Channels: p.Limits.MaxChannels,
			Members: p.Limits.MaxMembers, Projects: p.Limits.MaxProjects, StatusPages: p.Limits.MaxStatusPages, Specula: p.Limits.MaxAgents, StatusPageDomains: p.Limits.MaxStatusPageDomains}}
	if u := p.PeakUsage; u != nil {
		out.PeakUsage = &gen.ResourceCounts{Monitors: u.Monitors, Heartbeats: u.Heartbeats, Channels: u.Channels, Members: u.Members,
			Projects: u.Projects, StatusPages: u.StatusPages, Specula: u.Agents, StatusPageDomains: u.StatusPageDomains}
	}
	return out
}

func limitsFromAPI(c gen.ResourceCounts) plans.Limits {
	return plans.Limits{MaxMonitors: c.Monitors, MaxHeartbeats: c.Heartbeats, MaxChannels: c.Channels, MaxMembers: c.Members,
		MaxProjects: c.Projects, MaxStatusPages: c.StatusPages, MaxAgents: c.Specula, MaxStatusPageDomains: c.StatusPageDomains}
}

func (s *server) AdminListPlans(ctx context.Context, _ gen.AdminListPlansRequestObject) (gen.AdminListPlansResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.admin.ListPlans(ctx, a)
	if err != nil {
		return nil, err
	}
	out := gen.AdminListPlans200JSONResponse{Items: make([]gen.AdminPlan, 0, len(list))}
	for _, p := range list {
		out.Items = append(out.Items, toAdminPlan(p))
	}
	return out, nil
}

func (s *server) AdminGetPlan(ctx context.Context, req gen.AdminGetPlanRequestObject) (gen.AdminGetPlanResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.admin.GetPlan(ctx, a, req.Key)
	if err != nil {
		return nil, err
	}
	return gen.AdminGetPlan200JSONResponse(toAdminPlan(p)), nil
}

func (s *server) AdminCreatePlan(ctx context.Context, req gen.AdminCreatePlanRequestObject) (gen.AdminCreatePlanResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.admin.CreatePlan(ctx, a, req.Body.Key, req.Body.Name, limitsFromAPI(req.Body.Limits))
	if err != nil {
		return nil, err
	}
	return gen.AdminCreatePlan201JSONResponse(toAdminPlan(p)), nil
}

func (s *server) AdminUpdatePlan(ctx context.Context, req gen.AdminUpdatePlanRequestObject) (gen.AdminUpdatePlanResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.admin.UpdatePlan(ctx, a, req.Key, req.Body.Name, limitsFromAPI(req.Body.Limits))
	if err != nil {
		return nil, err
	}
	return gen.AdminUpdatePlan200JSONResponse(toAdminPlan(p)), nil
}

func (s *server) AdminDeletePlan(ctx context.Context, req gen.AdminDeletePlanRequestObject) (gen.AdminDeletePlanResponseObject, error) {
	a, err := s.adminActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.admin.DeletePlan(ctx, a, req.Key); err != nil {
		return nil, err
	}
	return gen.AdminDeletePlan204Response{}, nil
}
