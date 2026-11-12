package api

import (
	"context"

	"github.com/google/uuid"

	"github.com/proreta-io/proreta/backend/internal/api/gen"
	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/platform/httpx"
	"github.com/proreta-io/proreta/backend/internal/queries"
)

// Reads go through the dashboard read models (internal/queries); writes through the monitors
// service, after which the monitor is read back so every response has the same shape (uptime,
// ongoing incident, last response time).

func (s *server) actor(ctx context.Context) (monitors.Actor, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return monitors.Actor{}, err
	}
	access, err := s.access(ctx)
	if err != nil {
		return monitors.Actor{}, err
	}
	return monitors.Actor{TenantID: p.TenantID, UserID: p.UserID, IP: httpx.ClientIP(httpRequest(ctx), s.cfg.TrustedProxyHops), Access: access}, nil
}

func (s *server) ListMonitors(ctx context.Context, req gen.ListMonitorsRequestObject) (gen.ListMonitorsResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	sc, err := s.projectFilter(ctx, req.Params.ProjectId)
	if err != nil {
		return nil, err
	}
	list, sum, err := s.queries.ListMonitors(ctx, p.TenantID, sc)
	if err != nil {
		return nil, err
	}
	items := make([]gen.MonitorListItem, len(list))
	for i, m := range list {
		http, heartbeat := monitorSettings(m)
		items[i] = gen.MonitorListItem{
			Id: m.ID, ProjectId: m.ProjectID, Type: gen.MonitorType(m.Type), Name: m.Name,
			NotificationsEnabled: m.NotificationsEnabled, Paused: m.Paused,
			Status: gen.MonitorStatus(m.Status), StatusSince: m.StatusSince, LastErrorKind: m.LastErrorKind,
			LastCheckedAt: m.LastCheckedAt, OngoingIncident: toOngoing(m.Ongoing),
			Uptime24h: m.Uptime24h, Http: http, Heartbeat: heartbeat,
		}
	}
	return gen.ListMonitors200JSONResponse{
		Items:   items,
		Summary: gen.MonitorSummary{Up: sum.Up, Down: sum.Down, Paused: sum.Paused, Unknown: sum.Unknown},
	}, nil
}

func (s *server) CreateMonitor(ctx context.Context, req gen.CreateMonitorRequestObject) (gen.CreateMonitorResponseObject, error) {
	a, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := monitors.CreateInput{
		Type: string(b.Type), Name: b.Name, NotificationsEnabled: b.NotificationsEnabled,
		ChannelIDs: b.ChannelIds, ProjectID: b.ProjectId,
	}
	if h := b.Http; h != nil {
		in.HTTP = &monitors.HTTPSettings{URL: &h.Url, ExpectedStatus: h.ExpectedStatus, IntervalSeconds: h.IntervalSeconds,
			TimeoutSeconds: h.TimeoutSeconds, FailureThreshold: h.FailureThreshold,
			Method: methodOf(h.Method), Headers: headersOf(h.Headers), Body: bodyOf(h.Body)}
	}
	if hb := b.Heartbeat; hb != nil {
		in.Heartbeat = &monitors.HeartbeatSettings{ExpectedIntervalSeconds: hb.ExpectedIntervalSeconds, GraceSeconds: hb.GraceSeconds}
	}
	m, err := s.monitors.Create(ctx, a, in)
	if err != nil {
		return nil, err
	}
	d, err := s.detail(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	if m.HeartbeatToken != "" && d.Heartbeat != nil {
		u := s.heartbeatURL(m.HeartbeatToken)
		d.Heartbeat.PingUrl = &u
	}
	return gen.CreateMonitor201JSONResponse(d), nil
}

func (s *server) GetMonitor(ctx context.Context, req gen.GetMonitorRequestObject) (gen.GetMonitorResponseObject, error) {
	d, err := s.detail(ctx, req.MonitorId)
	if err != nil {
		return nil, err
	}
	return gen.GetMonitor200JSONResponse(d), nil
}

func (s *server) UpdateMonitor(ctx context.Context, req gen.UpdateMonitorRequestObject) (gen.UpdateMonitorResponseObject, error) {
	a, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := monitors.UpdateInput{Name: b.Name, NotificationsEnabled: b.NotificationsEnabled, ChannelIDs: b.ChannelIds, ProjectID: b.ProjectId}
	if h := b.Http; h != nil {
		in.HTTP = &monitors.HTTPSettings{URL: h.Url, ExpectedStatus: h.ExpectedStatus, IntervalSeconds: h.IntervalSeconds,
			TimeoutSeconds: h.TimeoutSeconds, FailureThreshold: h.FailureThreshold,
			Method: methodOf(h.Method), Headers: headersOf(h.Headers), Body: bodyOf(h.Body)}
	}
	if hb := b.Heartbeat; hb != nil {
		in.Heartbeat = &monitors.HeartbeatSettings{ExpectedIntervalSeconds: hb.ExpectedIntervalSeconds, GraceSeconds: hb.GraceSeconds}
	}
	if _, err := s.monitors.Update(ctx, a, req.MonitorId, in); err != nil {
		return nil, err
	}
	d, err := s.detail(ctx, req.MonitorId)
	if err != nil {
		return nil, err
	}
	return gen.UpdateMonitor200JSONResponse(d), nil
}

func (s *server) DeleteMonitor(ctx context.Context, req gen.DeleteMonitorRequestObject) (gen.DeleteMonitorResponseObject, error) {
	a, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.monitors.Delete(ctx, a, req.MonitorId); err != nil {
		return nil, err
	}
	return gen.DeleteMonitor204Response{}, nil
}

func (s *server) PauseMonitor(ctx context.Context, req gen.PauseMonitorRequestObject) (gen.PauseMonitorResponseObject, error) {
	a, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.monitors.Pause(ctx, a, req.MonitorId); err != nil {
		return nil, err
	}
	d, err := s.detail(ctx, req.MonitorId)
	if err != nil {
		return nil, err
	}
	return gen.PauseMonitor200JSONResponse(d), nil
}

func (s *server) ResumeMonitor(ctx context.Context, req gen.ResumeMonitorRequestObject) (gen.ResumeMonitorResponseObject, error) {
	a, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.monitors.Resume(ctx, a, req.MonitorId); err != nil {
		return nil, err
	}
	d, err := s.detail(ctx, req.MonitorId)
	if err != nil {
		return nil, err
	}
	return gen.ResumeMonitor200JSONResponse(d), nil
}

func (s *server) GetMonitorStats(ctx context.Context, req gen.GetMonitorStatsRequestObject) (gen.GetMonitorStatsResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	rng := gen.N24h
	if req.Params.Range != nil {
		rng = *req.Params.Range
	}
	if !queries.ValidRange(string(rng)) {
		return nil, &badRequestError{detail: "range must be 24h, 7d or 30d"}
	}
	sc, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.queries.Stats(ctx, p.TenantID, sc, req.MonitorId, string(rng))
	if err != nil {
		return nil, err
	}
	series := make([]gen.LatencyPoint, len(st.Series))
	for i, pt := range st.Series {
		series[i] = gen.LatencyPoint{T: pt.T, AvgMs: pt.AvgMs, OkRatio: pt.OKRatio}
	}
	return gen.GetMonitorStats200JSONResponse{Range: rng, AvgMs: st.AvgMs, P95Ms: st.P95Ms, Series: series}, nil
}

func (s *server) ListMonitorChecks(ctx context.Context, req gen.ListMonitorChecksRequestObject) (gen.ListMonitorChecksResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	limit := queries.DefaultCheckLimit
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	sc, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.queries.Checks(ctx, p.TenantID, sc, req.MonitorId, limit)
	if err != nil {
		return nil, err
	}
	items := make([]gen.Check, len(list))
	for i, c := range list {
		items[i] = gen.Check{CheckedAt: c.CheckedAt, Location: c.Location, Ok: c.OK, HttpStatus: c.HTTPStatus,
			ResponseMs: c.ResponseMs, ErrorKind: c.ErrorKind}
	}
	return gen.ListMonitorChecks200JSONResponse{Items: items}, nil
}

func (s *server) ListMonitorIncidents(ctx context.Context, req gen.ListMonitorIncidentsRequestObject) (gen.ListMonitorIncidentsResponseObject, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return nil, err
	}
	sc, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.queries.Incidents(ctx, p.TenantID, sc, req.MonitorId)
	if err != nil {
		return nil, err
	}
	items := make([]gen.Incident, len(list))
	for i, in := range list {
		items[i] = gen.Incident{Id: in.ID, StartedAt: in.StartedAt, ResolvedAt: in.ResolvedAt, Reason: in.Reason}
	}
	return gen.ListMonitorIncidents200JSONResponse{Items: items}, nil
}

// detail reads a monitor back in the request's scope, so a response never shows a monitor the
// caller cannot read.
func (s *server) detail(ctx context.Context, id uuid.UUID) (gen.MonitorDetail, error) {
	p, err := s.principal(ctx)
	if err != nil {
		return gen.MonitorDetail{}, err
	}
	sc, err := s.scope(ctx)
	if err != nil {
		return gen.MonitorDetail{}, err
	}
	d, err := s.queries.Monitor(ctx, p.TenantID, sc, id)
	if err != nil {
		return gen.MonitorDetail{}, err
	}
	m := d.Monitor
	http, heartbeat := monitorSettings(m)
	return gen.MonitorDetail{
		Id: m.ID, ProjectId: m.ProjectID, Type: gen.MonitorType(m.Type), Name: m.Name,
		NotificationsEnabled: m.NotificationsEnabled, Paused: m.Paused,
		Status: gen.MonitorStatus(m.Status), StatusSince: m.StatusSince, LastErrorKind: m.LastErrorKind,
		LastCheckedAt: m.LastCheckedAt, OngoingIncident: toOngoing(m.Ongoing),
		Uptime:     gen.Uptime{H24: m.Uptime24h, D7: d.Uptime7d, D30: d.Uptime30d},
		ChannelIds: d.ChannelIDs,
		CreatedAt:  m.CreatedAt, UpdatedAt: m.UpdatedAt,
		Http: http, Heartbeat: heartbeat,
	}, nil
}

// monitorSettings is the type's sub-object of a monitor; the other one stays nil and is omitted.
// A heartbeat's expected interval is its interval_seconds.
func monitorSettings(m queries.Monitor) (*gen.HttpMonitor, *gen.HeartbeatMonitor) {
	if m.Type == monitors.TypeHeartbeat {
		return nil, &gen.HeartbeatMonitor{ExpectedIntervalSeconds: m.IntervalSeconds, GraceSeconds: deref(m.GraceSeconds), LastPingAt: m.LastPingAt}
	}
	names := m.HeaderNames
	if names == nil {
		names = []string{}
	}
	return &gen.HttpMonitor{Url: m.URL, ExpectedStatus: m.ExpectedStatus, Method: gen.HttpMethod(m.Method), HeaderNames: names, BodySet: m.BodySet,
		IntervalSeconds: m.IntervalSeconds,
		TimeoutSeconds:  m.TimeoutSeconds, FailureThreshold: m.FailureThreshold, TlsExpiresAt: m.TLSExpiresAt,
		LastResponseMs: m.LastResponseMs}, nil
}

func toOngoing(in *queries.Incident) *gen.OngoingIncident {
	if in == nil {
		return nil
	}
	return &gen.OngoingIncident{Id: in.ID, StartedAt: in.StartedAt, Reason: in.Reason}
}

func methodOf(m *gen.HttpMethod) *string {
	if m == nil {
		return nil
	}
	v := string(*m)
	return &v
}

func bodyOf(b *gen.HttpBody) *string {
	if b == nil {
		return nil
	}
	v := string(*b)
	return &v
}

func headersOf(h *[]gen.HttpHeaderInput) *[]monitors.HeaderInput {
	if h == nil {
		return nil
	}
	out := make([]monitors.HeaderInput, len(*h))
	for i, x := range *h {
		out[i] = monitors.HeaderInput{Name: x.Name, Value: x.Value}
	}
	return &out
}
