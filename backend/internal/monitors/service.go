// Package monitors owns monitors, monitor_locations, monitor_state and heartbeat_tokens:
// creating, editing, pausing, resuming and (soft) deleting HTTPS and heartbeat monitors under the
// tenant's plan quotas, heartbeat pings and their evaluation. Schedules
// (monitor_location_state) belong to internal/scheduler and are written through its service in
// the same transaction.
package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/audit"
	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/incidents"
	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/monitors/monitorsdb"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/plans"
	"github.com/proreta-io/proreta/backend/internal/platform/crypto"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/uuidv7"
	"github.com/proreta-io/proreta/backend/internal/platform/validation"
	"github.com/proreta-io/proreta/backend/internal/projects"
	"github.com/proreta-io/proreta/backend/internal/scheduler"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
)

// Statuses of a monitor.
const (
	StatusUnknown = "unknown"
	StatusUp      = "up"
	StatusDown    = "down"
)

// Audit actions written by this module.
const (
	ActionCreated = "monitor.created"
	ActionUpdated = "monitor.updated"
	ActionDeleted = "monitor.deleted"
	ActionPaused  = "monitor.paused"
	ActionResumed = "monitor.resumed"
	// ActionTokenRegenerated: a heartbeat got a new URL (the token is never recorded).
	ActionTokenRegenerated = "monitor.heartbeat_token_regenerated"
)

// Monitor types (monitors.type).
const (
	TypeHTTP      = "http"
	TypeTCP       = "tcp"
	TypeHeartbeat = "heartbeat"
)

var (
	// ErrNotFound means no live monitor with that id exists in the tenant.
	ErrNotFound = errors.New("monitors: not found")
	// ErrNotHeartbeat means the operation exists only for heartbeat monitors.
	ErrNotHeartbeat = errors.New("monitors: not a heartbeat monitor")
)

// Actor is who performs a change; it scopes every operation to one tenant, and Access to the
// projects the user may change monitors in.
type Actor struct {
	TenantID uuid.UUID
	UserID   uuid.UUID
	IP       netip.Addr
	Access   authz.Actor
}

// checkAccess refuses a change of a monitor in project: ErrNotFound when the actor does not see
// the project (so ids of other projects reveal nothing), authz.ErrProjectForbidden when its role
// there is below member (a viewer).
func (a Actor) checkAccess(project uuid.UUID) error {
	if !a.Access.Sees(project) {
		return ErrNotFound
	}
	if !a.Access.CanEditIn(project) {
		return authz.ErrProjectForbidden
	}
	return nil
}

// Monitor is a monitor with its aggregated state.
type Monitor struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Type      string
	Name      string
	// URL and ExpectedStatus are empty for a heartbeat.
	URL            string
	ExpectedStatus string
	// Method, HeaderNames and BodySet describe the request of an http monitor; header values and
	// the body are write-only and never returned.
	Method      string
	HeaderNames []string
	BodySet     bool
	// Host and Port are the target of a tcp monitor.
	Host string
	Port int
	// IntervalSeconds is the check interval, or a heartbeat's expected ping interval.
	IntervalSeconds      int
	TimeoutSeconds       int
	GraceSeconds         int // heartbeats only
	FailureThreshold     int
	NotificationsEnabled bool
	Paused               bool
	Status               string
	StatusSince          time.Time
	LastErrorKind        *string
	LastCheckedAt        *time.Time
	TLSExpiresAt         *time.Time
	CurrentIncidentID    *uuid.UUID
	CreatedAt            time.Time
	UpdatedAt            time.Time
	// HeartbeatToken is the secret of a new heartbeat's URL, set only in the result of Create. It
	// is never stored (only its hash is) and cannot be read again.
	HeartbeatToken string
}

// CreateInput is a new monitor. Nil fields take their defaults. Type is TypeHTTP when empty.
// The settings of the type are in the sub-struct of the same name, which is required; the other
// one must be nil.
type CreateInput struct {
	Type                 string
	Name                 string
	NotificationsEnabled *bool
	HTTP                 *HTTPSettings
	TCP                  *TCPSettings
	Heartbeat            *HeartbeatSettings
	// ProjectID is the monitor's project; nil means the Default project.
	ProjectID *uuid.UUID
	// ChannelIDs selects notification channels of the project or organization-wide ones (none:
	// the active channels of the project).
	ChannelIDs *[]uuid.UUID
	// SpeculaID runs the monitor at a Specula of the organization that the project may use; nil
	// runs it from Proreta's own worker. Only for http and tcp monitors.
	SpeculaID *uuid.UUID
}

// Location is where an http or tcp monitor runs: SpeculaID nil is Proreta's own worker.
type Location struct {
	SpeculaID *uuid.UUID
}

// HTTPSettings are the settings of an http monitor; nil fields take their defaults on create
// and keep their value on update. URL is required on create.
type HTTPSettings struct {
	URL            *string
	ExpectedStatus *string
	// Method defaults to GET. Headers replaces the custom headers (nil keeps them). Body
	// replaces the body: nil keeps it, an empty string removes it.
	Method           *string
	Headers          *[]HeaderInput
	Body             *string
	IntervalSeconds  *int
	TimeoutSeconds   *int
	FailureThreshold *int
}

// TCPSettings are the settings of a tcp monitor; nil fields take their defaults on create and
// keep their value on update. Host and Port are required on create.
type TCPSettings struct {
	Host             *string
	Port             *int
	FailureThreshold *int
	// Send, Expect, ReadTimeoutSeconds and MaxResponseBytes make the check talk to the service;
	// see tcpcheck.go. On update a nil keeps the stored value, an empty Value removes it.
	Send               *TCPSend
	Expect             *TCPExpect
	ReadTimeoutSeconds *int
	MaxResponseBytes   *int
}

// HeartbeatSettings are the settings of a heartbeat; nil fields take their defaults on create
// and keep their value on update.
type HeartbeatSettings struct {
	ExpectedIntervalSeconds *int
	GraceSeconds            *int
}

// UpdateInput changes the non-nil fields. Only the sub-struct of the monitor's type may be set.
type UpdateInput struct {
	Name                 *string
	NotificationsEnabled *bool
	HTTP                 *HTTPSettings
	TCP                  *TCPSettings
	Heartbeat            *HeartbeatSettings
	// ProjectID moves the monitor; selected channels of the old project are dropped.
	ProjectID *uuid.UUID
	// ChannelIDs replaces the channel selection.
	ChannelIDs *[]uuid.UUID
	// Location moves an http or tcp monitor; the target is checked again with the rules of the new
	// location and an open incident ends.
	Location *Location
}

// httpConfig is monitors.config for type http.
type httpConfig struct {
	URL            string `json:"url"`
	ExpectedStatus string `json:"expectedStatus"`
	// Method is omitted for GET. Request is the sealed headers and body (monitorrequest); the
	// names and the flag are kept in the clear so reads never open it.
	Method      string   `json:"method,omitempty"`
	HeaderNames []string `json:"headerNames,omitempty"`
	BodySet     bool     `json:"bodySet,omitempty"`
	Request     string   `json:"request,omitempty"`
}

// tcpConfig is monitors.config for type tcp.
type tcpConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Request is the sealed probe (monitorrequest.TCPProbe: the bytes to send, the expect rule
	// and the limits), read by the schedule queries as for http. SendSet, Expect and the limits
	// are kept in the clear so reads never open it. fromRow embeds this struct and httpConfig,
	// which share the key "request"; it reads neither field.
	SendSet            bool        `json:"sendSet,omitempty"`
	Request            string      `json:"request,omitempty"`
	Expect             *ExpectRule `json:"expect,omitempty"`
	ReadTimeoutSeconds int         `json:"readTimeoutSeconds,omitempty"`
	MaxResponseBytes   int         `json:"maxResponseBytes,omitempty"`
}

// heartbeatConfig is monitors.config for type heartbeat.
type heartbeatConfig struct {
	GraceSeconds int `json:"graceSeconds"`
}

// Field names of validation errors and audit records: the API's, with the sub-object prefix.
const (
	fieldSpeculaID               = "location.speculaId"
	fieldHTTP                    = "http"
	fieldHeartbeat               = "heartbeat"
	fieldTCP                     = "tcp"
	fieldHost                    = "tcp.host"
	fieldPort                    = "tcp.port"
	fieldTCPFailureThreshold     = "tcp.failureThreshold"
	fieldURL                     = "http.url"
	fieldExpectedStatus          = "http.expectedStatus"
	fieldIntervalSeconds         = "http.intervalSeconds"
	fieldTimeoutSeconds          = "http.timeoutSeconds"
	fieldFailureThreshold        = "http.failureThreshold"
	fieldExpectedIntervalSeconds = "heartbeat.expectedIntervalSeconds"
	fieldGraceSeconds            = "heartbeat.graceSeconds"

	msgHTTPOnly      = "is only for http and tcp monitors"
	msgHTTPOnlyType  = "is only for http monitors"
	msgTCPOnly       = "is only for tcp monitors"
	msgHeartbeatOnly = "is only for heartbeat monitors"
)

// Service implements the monitor lifecycle.
type Service struct {
	pool        *db.Pool
	sched       *scheduler.Service
	incidents   *incidents.Service
	requestKeys *crypto.Keyring
}

// NewService returns a Service.
func NewService(pool *db.Pool, sched *scheduler.Service, inc *incidents.Service) *Service {
	return &Service{pool: pool, sched: sched, incidents: inc}
}

// Create validates in, enforces the tenant's quota for the type (a *plans.LimitError when it is
// used up) and creates the monitor with its location, schedule and state rows (and, for a
// heartbeat, its token) in one transaction.
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (Monitor, error) {
	var verr validation.Error
	name, msg := ValidateName(in.Name)
	if msg != "" {
		verr.Add("name", msg)
	}
	var p createPlan
	var perr error
	switch in.Type {
	case "", TypeHTTP:
		perr = s.planHTTP(&verr, a, in, &p)
	case TypeTCP:
		planTCP(&verr, in, &p)
	case TypeHeartbeat:
		planHeartbeat(&verr, in, &p)
	default:
		verr.Add("type", `must be "http", "tcp" or "heartbeat"`)
	}
	if perr != nil {
		return Monitor{}, perr
	}
	if tc, ok := p.config.(tcpConfig); ok && (tc.SendSet || tc.Expect != nil) {
		if s.requestKeys == nil {
			verr.Add(fieldTCPSend, msgNoSendKeys)
		}
	}
	if err := verr.Err(); err != nil {
		return Monitor{}, err
	}
	id := uuidv7.New()
	if p.isHTTP {
		if err := s.sealRequest(&p.httpCfg, a.TenantID, id, p.httpSec); err != nil {
			return Monitor{}, err
		}
		p.config = p.httpCfg
	}
	if p.row.Type == TypeTCP {
		cfg := p.config.(tcpConfig)
		if err := s.sealTCP(&cfg, a.TenantID, id, p.tcpSend, p.tcpChanged); err != nil {
			return Monitor{}, err
		}
		p.config = cfg
	}
	raw, err := json.Marshal(p.config)
	if err != nil {
		return Monitor{}, fmt.Errorf("monitors: config: %w", err)
	}
	var token string
	var hash []byte
	if p.row.Type == TypeHeartbeat {
		if token, hash, err = NewHeartbeatToken(); err != nil {
			return Monitor{}, err
		}
	}

	p.row.ID, p.row.TenantID, p.row.Name, p.row.Config = id, a.TenantID, name, raw
	p.row.TimeoutSeconds, p.row.NotificationsEnabled = TimeoutSeconds, orDefault(in.NotificationsEnabled, true)
	var m Monitor
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		var err error
		m, err = s.insertMonitor(ctx, tx, a, in, &p, hash)
		return err
	})
	if err != nil {
		return Monitor{}, wrap("create", err)
	}
	m.HeartbeatToken = token
	return m, nil
}

// createPlan is a validated CreateInput: the row to insert (without id, name, config and project),
// the quota resource it counts against and, for an http monitor, the request still to be sealed.
type createPlan struct {
	row     monitorsdb.InsertMonitorParams
	config  any
	httpCfg httpConfig
	httpSec monitorrequest.Secrets
	isHTTP  bool
	// tcpSend is the new send of a tcp monitor, still to be sealed when tcpSendSet.
	tcpSend    []byte
	tcpChanged bool
	resource   plans.Resource
}

// planHTTP validates the http settings of in into p. Only an unexpected error is returned;
// field problems go to verr.
func (s *Service) planHTTP(verr *validation.Error, a Actor, in CreateInput, p *createPlan) error {
	p.resource = plans.ResourceMonitor
	p.row.Type = TypeHTTP
	if in.Heartbeat != nil {
		verr.Add(fieldHeartbeat, msgHeartbeatOnly)
	}
	if in.TCP != nil {
		verr.Add(fieldTCP, msgTCPOnly)
	}
	h := in.HTTP
	if h == nil {
		verr.Add(fieldHTTP, "is required for http monitors")
		h = &HTTPSettings{}
	}
	var target, msg string
	if h.URL == nil {
		if in.HTTP != nil {
			verr.Add(fieldURL, "is required")
		}
	} else if target, msg = validateTarget(*h.URL, in.SpeculaID != nil); msg != "" {
		verr.Add(fieldURL, msg)
	}
	checkFixed(verr, fieldIntervalSeconds, h.IntervalSeconds, IntervalSeconds)
	checkFixed(verr, fieldTimeoutSeconds, h.TimeoutSeconds, TimeoutSeconds)
	expected := orDefault(h.ExpectedStatus, DefaultExpectedStatus)
	if msg := ValidateExpectedStatus(expected); msg != "" {
		verr.Add(fieldExpectedStatus, msg)
	}
	threshold := orDefault(h.FailureThreshold, DefaultFailureThreshold)
	checkThreshold(verr, fieldFailureThreshold, threshold)
	p.httpCfg = httpConfig{URL: target, ExpectedStatus: expected}
	var perr error
	if p.httpCfg, p.httpSec, _, _, perr = s.planRequest(verr, p.httpCfg, a.TenantID, uuid.Nil, h); perr != nil {
		return perr
	}
	p.isHTTP = true
	p.row.IntervalSeconds, p.row.FailureThreshold = IntervalSeconds, int32(threshold) //nolint:gosec // validated 1..3
	return nil
}

// planTCP validates the tcp settings of in into p.
func planTCP(verr *validation.Error, in CreateInput, p *createPlan) {
	p.resource = plans.ResourceMonitor
	p.row.Type = TypeTCP
	if in.HTTP != nil {
		verr.Add(fieldHTTP, msgHTTPOnlyType)
	}
	if in.Heartbeat != nil {
		verr.Add(fieldHeartbeat, msgHeartbeatOnly)
	}
	t := in.TCP
	if t == nil {
		verr.Add(fieldTCP, "is required for tcp monitors")
		t = &TCPSettings{}
	}
	var cfg tcpConfig
	var msg string
	if t.Host == nil {
		if in.TCP != nil {
			verr.Add(fieldHost, "is required")
		}
	} else if cfg.Host, msg = ValidateHost(*t.Host, in.SpeculaID != nil); msg != "" {
		verr.Add(fieldHost, msg)
	}
	if t.Port == nil {
		if in.TCP != nil {
			verr.Add(fieldPort, "is required")
		}
	} else if cfg.Port = *t.Port; ValidatePort(cfg.Port) != "" {
		verr.Add(fieldPort, ValidatePort(cfg.Port))
	}
	threshold := orDefault(t.FailureThreshold, DefaultFailureThreshold)
	checkThreshold(verr, fieldTCPFailureThreshold, threshold)
	p.tcpSend, p.tcpChanged, _ = planTCPCheck(verr, &cfg, t, false)
	p.config = cfg
	p.row.IntervalSeconds, p.row.FailureThreshold = IntervalSeconds, int32(threshold) //nolint:gosec // validated 1..3
}

// planHeartbeat validates the heartbeat settings of in into p.
func planHeartbeat(verr *validation.Error, in CreateInput, p *createPlan) {
	p.row.Type, p.resource = TypeHeartbeat, plans.ResourceHeartbeat
	if in.SpeculaID != nil {
		verr.Add(fieldSpeculaID, msgHTTPOnly)
	}
	if in.HTTP != nil {
		verr.Add(fieldHTTP, msgHTTPOnlyType)
	}
	if in.TCP != nil {
		verr.Add(fieldTCP, msgTCPOnly)
	}
	hb := in.Heartbeat
	if hb == nil {
		verr.Add(fieldHeartbeat, "is required for heartbeat monitors")
		hb = &HeartbeatSettings{}
	}
	interval := orDefault(hb.ExpectedIntervalSeconds, DefaultExpectedIntervalSeconds)
	if msg := ValidateExpectedInterval(interval); msg != "" {
		verr.Add(fieldExpectedIntervalSeconds, msg)
	}
	grace := orDefault(hb.GraceSeconds, DefaultGraceSeconds)
	if msg := ValidateGrace(grace); msg != "" {
		verr.Add(fieldGraceSeconds, msg)
	}
	p.config = heartbeatConfig{GraceSeconds: grace}
	// A heartbeat goes down at its first missed deadline: its failure threshold is always 1.
	p.row.IntervalSeconds, p.row.FailureThreshold = int32(interval), 1 //nolint:gosec // validated 60..86400
}

// insertMonitor runs inside the tenant transaction of Create: quota, project and permission, the
// location, the rows of the monitor, its channel selection and the audit entry.
func (s *Service) insertMonitor(ctx context.Context, tx pgx.Tx, a Actor, in CreateInput, p *createPlan, hash []byte) (Monitor, error) {
	row, id := &p.row, p.row.ID
	// The tenant row lock taken here serialises concurrent creates, so the count stays true
	// until this transaction commits.
	if err := plans.CheckCreate(ctx, tx, a.TenantID, p.resource); err != nil {
		return Monitor{}, err
	}
	q := monitorsdb.New(tx)
	project, err := projects.ForUse(ctx, tx, a.TenantID, in.ProjectID)
	if errors.Is(err, projects.ErrNotFound) || (err == nil && !a.Access.Sees(project)) {
		return Monitor{}, projectFieldError()
	}
	if err != nil {
		return Monitor{}, err
	}
	if !a.Access.CanEditIn(project) {
		return Monitor{}, authz.ErrProjectForbidden
	}
	row.ProjectID = project
	location, err := s.sched.DefaultLocation(ctx, tx)
	if err != nil {
		return Monitor{}, err
	}
	if in.SpeculaID != nil {
		if location, err = s.speculaLocation(ctx, tx, a.TenantID, *in.SpeculaID, project); err != nil {
			return Monitor{}, err
		}
	}
	if err := q.InsertMonitor(ctx, *row); err != nil {
		return Monitor{}, fmt.Errorf("insert monitor: %w", err)
	}
	if err := q.InsertMonitorLocation(ctx, monitorsdb.InsertMonitorLocationParams{TenantID: a.TenantID, MonitorID: id, LocationID: location}); err != nil {
		return Monitor{}, fmt.Errorf("insert monitor location: %w", err)
	}
	if row.Type == TypeHeartbeat {
		// The location state only carries the incident machine's state; nothing claims it.
		if err := s.sched.InitParkedMonitorLocation(ctx, tx, a.TenantID, id, location); err != nil {
			return Monitor{}, err
		}
		if err := q.InsertHeartbeatToken(ctx, monitorsdb.InsertHeartbeatTokenParams{TenantID: a.TenantID, MonitorID: id, TokenHash: hash}); err != nil {
			return Monitor{}, fmt.Errorf("insert heartbeat token: %w", err)
		}
	} else if err := s.sched.InitMonitorLocation(ctx, tx, a.TenantID, id, location); err != nil {
		return Monitor{}, err
	}
	if err := q.InsertMonitorState(ctx, monitorsdb.InsertMonitorStateParams{TenantID: a.TenantID, MonitorID: id}); err != nil {
		return Monitor{}, fmt.Errorf("insert monitor state: %w", err)
	}
	if in.ChannelIDs != nil {
		if _, err := notify.SetMonitorChannels(ctx, tx, a.TenantID, id, project, *in.ChannelIDs); err != nil {
			return Monitor{}, err
		}
	}
	if err := s.audit(ctx, tx, a, ActionCreated, id, map[string]any{"type": row.Type, "projectId": project.String()}); err != nil {
		return Monitor{}, err
	}
	return get(ctx, q, a.TenantID, id)
}

// Update changes the given fields, also inside the type's settings. The interval and timeout of
// an http monitor are fixed: they may be sent only with their current value. A heartbeat's
// interval and grace may change; its deadline then follows the new values.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in UpdateInput) (Monitor, error) {
	var m Monitor
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := monitorsdb.New(tx)
		cur, err := lockForUpdate(ctx, q, a, id)
		if err != nil {
			return err
		}
		e, err := s.planUpdate(ctx, tx, a, id, cur, in)
		if err != nil {
			return err
		}
		project := cur.ProjectID
		var moveMeta map[string]any
		if in.ProjectID != nil && *in.ProjectID != project {
			if moveMeta, project, err = moveMonitor(ctx, tx, q, a, id, project, *in.ProjectID, &e.changed); err != nil {
				return err
			}
		}
		if in.ChannelIDs != nil {
			selected, err := notify.SetMonitorChannels(ctx, tx, a.TenantID, id, project, *in.ChannelIDs)
			if err != nil {
				return err
			}
			e.changed = appendIf(e.changed, "channelIds", selected)
		}
		// A moved project must still be allowed to use the Specula the monitor runs at.
		if e.wantSpecula != nil && project != cur.ProjectID && !e.locChanged {
			if _, err := s.speculaLocation(ctx, tx, a.TenantID, *e.wantSpecula, project); err != nil {
				return err
			}
		}
		if e.locChanged {
			if err := s.moveLocation(ctx, tx, a, id, project, e.wantSpecula); err != nil {
				return err
			}
			e.changed = append(e.changed, fieldSpeculaID)
		}
		if err := s.restartIfRetargeted(ctx, tx, q, a, id, e); err != nil {
			return err
		}
		if len(e.changed) > 0 {
			if err := s.saveUpdate(ctx, tx, q, a, id, e, moveMeta); err != nil {
				return err
			}
		}
		m, err = get(ctx, q, a.TenantID, id)
		return err
	})
	if err != nil {
		return Monitor{}, wrap("update", err)
	}
	return m, nil
}

// restartIfRetargeted restarts the monitor when the update gave it a new target (URL, host, port
// or location, or a new send or expect rule): its history says nothing about the new check. A
// monitor whose executor cannot run the new rule would otherwise keep a stale "up" forever. Like resume, and an open incident ends
// without a notification.
func (s *Service) restartIfRetargeted(ctx context.Context, tx pgx.Tx, q *monitorsdb.Queries, a Actor, id uuid.UUID, e *monitorEdit) error {
	if !slices.Contains(e.changed, fieldURL) && !slices.Contains(e.changed, fieldHost) && !slices.Contains(e.changed, fieldPort) && !slices.Contains(e.changed, fieldTCPSend) && !slices.Contains(e.changed, fieldTCPExpect) && !e.locChanged {
		return nil
	}
	if err := s.restart(ctx, tx, q, a.TenantID, id, true); err != nil {
		return err
	}
	reason := incidents.ReasonURLChanged
	if e.locChanged {
		reason = incidents.ReasonLocationChanged
	}
	return s.incidents.ResolveOpen(ctx, tx, a.TenantID, id, reason)
}

// lockForUpdate locks the monitor and returns it if the actor may change it.
func lockForUpdate(ctx context.Context, q *monitorsdb.Queries, a Actor, id uuid.UUID) (monitorsdb.LockMonitorRow, error) {
	cur, err := q.LockMonitor(ctx, monitorsdb.LockMonitorParams{TenantID: a.TenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return cur, ErrNotFound
	}
	if err != nil {
		return cur, fmt.Errorf("lock monitor: %w", err)
	}
	if err := a.checkAccess(cur.ProjectID); err != nil {
		return cur, err
	}
	if cur.Type == TypeHeartbeat {
		// Serialises with the evaluation in jobs, which reads the grace under this lock.
		if err := lockHeartbeat(ctx, q, a.TenantID, id); err != nil {
			return cur, err
		}
	}
	return cur, nil
}

// monitorEdit is an update resolved against the stored monitor: the values after the change and the
// names of the fields that differ.
type monitorEdit struct {
	name                 string
	interval, threshold  int
	notificationsEnabled bool
	changed              []string
	config               any
	// wantSpecula is where the monitor should run after the change (nil: Proreta's worker) and
	// locChanged says that differs from where it runs now.
	wantSpecula *uuid.UUID
	locChanged  bool
}

// planUpdate validates the request against the stored monitor and merges it over it. Field
// problems are reported together, in the order name, notificationsEnabled, then those of the
// monitor's type.
func (s *Service) planUpdate(ctx context.Context, tx pgx.Tx, a Actor, id uuid.UUID, cur monitorsdb.LockMonitorRow, in UpdateInput) (*monitorEdit, error) {
	e := &monitorEdit{name: cur.Name, interval: int(cur.IntervalSeconds), threshold: int(cur.FailureThreshold), notificationsEnabled: cur.NotificationsEnabled}

	// Where the monitor runs now and where it should run after this change.
	curSpecula, err := currentSpecula(ctx, tx, a.TenantID, id)
	if err != nil {
		return nil, err
	}
	e.wantSpecula = curSpecula
	if in.Location != nil {
		e.wantSpecula = in.Location.SpeculaID
	}
	e.locChanged = cur.Type != TypeHeartbeat && !sameID(curSpecula, e.wantSpecula)

	var verr validation.Error
	if in.Name != nil {
		v, msg := ValidateName(*in.Name)
		if msg != "" {
			verr.Add("name", msg)
		}
		e.changed, e.name = appendIf(e.changed, "name", v != e.name), v
	}
	if in.NotificationsEnabled != nil {
		e.changed, e.notificationsEnabled = appendIf(e.changed, "notificationsEnabled", *in.NotificationsEnabled != e.notificationsEnabled), *in.NotificationsEnabled
	}
	switch cur.Type {
	case TypeHeartbeat:
		err = planHeartbeatUpdate(&verr, cur, in, e)
	case TypeTCP:
		err = s.planTCPUpdate(&verr, a, id, cur, in, e)
	default:
		err = s.planHTTPUpdate(&verr, a, id, cur, in, e)
	}
	if err != nil {
		return nil, err
	}
	if err := verr.Err(); err != nil {
		return nil, err
	}
	return e, nil
}

func planHeartbeatUpdate(verr *validation.Error, cur monitorsdb.LockMonitorRow, in UpdateInput, e *monitorEdit) error {
	var cfg heartbeatConfig
	if err := json.Unmarshal(cur.Config, &cfg); err != nil {
		return fmt.Errorf("monitor config: %w", err)
	}
	if in.Location != nil {
		verr.Add(fieldSpeculaID, msgHTTPOnly)
	}
	if in.HTTP != nil {
		verr.Add(fieldHTTP, msgHTTPOnlyType)
	}
	if in.TCP != nil {
		verr.Add(fieldTCP, msgTCPOnly)
	}
	if hb := in.Heartbeat; hb != nil {
		if hb.ExpectedIntervalSeconds != nil {
			if msg := ValidateExpectedInterval(*hb.ExpectedIntervalSeconds); msg != "" {
				verr.Add(fieldExpectedIntervalSeconds, msg)
			}
			e.changed, e.interval = appendIf(e.changed, fieldExpectedIntervalSeconds, *hb.ExpectedIntervalSeconds != e.interval), *hb.ExpectedIntervalSeconds
		}
		if hb.GraceSeconds != nil {
			if msg := ValidateGrace(*hb.GraceSeconds); msg != "" {
				verr.Add(fieldGraceSeconds, msg)
			}
			e.changed, cfg.GraceSeconds = appendIf(e.changed, fieldGraceSeconds, *hb.GraceSeconds != cfg.GraceSeconds), *hb.GraceSeconds
		}
	}
	e.config = cfg
	return nil
}

func (s *Service) planTCPUpdate(verr *validation.Error, a Actor, id uuid.UUID, cur monitorsdb.LockMonitorRow, in UpdateInput, e *monitorEdit) error {
	var cfg tcpConfig
	if err := json.Unmarshal(cur.Config, &cfg); err != nil {
		return fmt.Errorf("monitor config: %w", err)
	}
	oldHost, oldPort := cfg.Host, cfg.Port
	if in.Heartbeat != nil {
		verr.Add(fieldHeartbeat, msgHeartbeatOnly)
	}
	if in.HTTP != nil {
		verr.Add(fieldHTTP, msgHTTPOnlyType)
	}
	if e.locChanged && (in.TCP == nil || in.TCP.Host == nil) {
		// The target stays but must satisfy the rules of the new location.
		v, msg := ValidateHost(cfg.Host, e.wantSpecula != nil)
		if msg != "" {
			verr.Add(fieldHost, msg)
		}
		cfg.Host = v
	}
	if t := in.TCP; t != nil {
		if t.Host != nil {
			v, msg := ValidateHost(*t.Host, e.wantSpecula != nil)
			if msg != "" {
				verr.Add(fieldHost, msg)
			}
			e.changed, cfg.Host = appendIf(e.changed, fieldHost, v != cfg.Host), v
		}
		if t.Port != nil {
			if msg := ValidatePort(*t.Port); msg != "" {
				verr.Add(fieldPort, msg)
			}
			e.changed, cfg.Port = appendIf(e.changed, fieldPort, *t.Port != cfg.Port), *t.Port
		}
		if t.FailureThreshold != nil {
			checkThreshold(verr, fieldTCPFailureThreshold, *t.FailureThreshold)
			e.changed, e.threshold = appendIf(e.changed, fieldTCPFailureThreshold, *t.FailureThreshold != e.threshold), *t.FailureThreshold
		}
	}
	t := in.TCP
	if t == nil {
		t = &TCPSettings{}
	}
	send, sendChanged, changed := planTCPCheck(verr, &cfg, t, cfg.Host != oldHost || cfg.Port != oldPort || e.locChanged)
	e.changed = append(e.changed, changed...)
	if (cfg.SendSet || cfg.Expect != nil) && len(changed) > 0 && s.requestKeys == nil {
		verr.Add(fieldTCPSend, msgNoSendKeys)
	}
	if len(changed) > 0 && verr.Err() == nil {
		if err := s.sealTCP(&cfg, a.TenantID, id, send, sendChanged); err != nil {
			return err
		}
	}
	e.config = cfg
	return nil
}

func (s *Service) planHTTPUpdate(verr *validation.Error, a Actor, id uuid.UUID, cur monitorsdb.LockMonitorRow, in UpdateInput, e *monitorEdit) error {
	var cfg httpConfig
	if err := json.Unmarshal(cur.Config, &cfg); err != nil {
		return fmt.Errorf("monitor config: %w", err)
	}
	if in.Heartbeat != nil {
		verr.Add(fieldHeartbeat, msgHeartbeatOnly)
	}
	if in.TCP != nil {
		verr.Add(fieldTCP, msgTCPOnly)
	}
	if e.locChanged && (in.HTTP == nil || in.HTTP.URL == nil) {
		// The target stays but must satisfy the rules of the new location.
		v, msg := validateTarget(cfg.URL, e.wantSpecula != nil)
		if msg != "" {
			verr.Add(fieldURL, msg)
		}
		cfg.URL = v
	}
	if h := in.HTTP; h != nil {
		oldURL := cfg.URL
		checkFixed(verr, fieldIntervalSeconds, h.IntervalSeconds, int(cur.IntervalSeconds))
		checkFixed(verr, fieldTimeoutSeconds, h.TimeoutSeconds, int(cur.TimeoutSeconds))
		if h.URL != nil {
			v, msg := validateTarget(*h.URL, e.wantSpecula != nil)
			if msg != "" {
				verr.Add(fieldURL, msg)
			}
			e.changed, cfg.URL = appendIf(e.changed, fieldURL, v != cfg.URL), v
		}
		if h.ExpectedStatus != nil {
			if msg := ValidateExpectedStatus(*h.ExpectedStatus); msg != "" {
				verr.Add(fieldExpectedStatus, msg)
			}
			e.changed, cfg.ExpectedStatus = appendIf(e.changed, fieldExpectedStatus, *h.ExpectedStatus != cfg.ExpectedStatus), *h.ExpectedStatus
		}
		if h.FailureThreshold != nil {
			checkThreshold(verr, fieldFailureThreshold, *h.FailureThreshold)
			e.changed, e.threshold = appendIf(e.changed, fieldFailureThreshold, *h.FailureThreshold != e.threshold), *h.FailureThreshold
		}
		hostMovedProblems(verr, oldURL, cfg, h)
		next, sec, dirty, reqChanged, perr := s.planRequest(verr, cfg, a.TenantID, id, h)
		if perr != nil {
			return perr
		}
		cfg, e.changed = next, append(e.changed, reqChanged...)
		if dirty && verr.Err() == nil {
			if err := s.sealRequest(&cfg, a.TenantID, id, sec); err != nil {
				return err
			}
		}
	}
	e.config = cfg
	return nil
}

// moveMonitor moves the monitor to the project target (which the actor must be able to edit in),
// drops the channels and status page components of the old project, and returns the audit
// metadata of the move and the new project.
func moveMonitor(ctx context.Context, tx pgx.Tx, q *monitorsdb.Queries, a Actor, id, from, to uuid.UUID, changed *[]string) (map[string]any, uuid.UUID, error) {
	target, err := projects.ForUse(ctx, tx, a.TenantID, &to)
	if errors.Is(err, projects.ErrNotFound) || (err == nil && !a.Access.Sees(target)) {
		return nil, from, projectFieldError()
	}
	if err != nil {
		return nil, from, err
	}
	if !a.Access.CanEditIn(target) {
		return nil, from, authz.ErrProjectForbidden
	}
	if err := q.SetMonitorProject(ctx, monitorsdb.SetMonitorProjectParams{TenantID: a.TenantID, ID: id, ProjectID: target}); err != nil {
		return nil, from, fmt.Errorf("move monitor: %w", err)
	}
	// Channels of the old project are not allowed in the new one.
	detached, err := notify.DetachForeignChannels(ctx, tx, a.TenantID, id, target)
	if err != nil {
		return nil, from, err
	}
	// Pages of the old project may show only its monitors.
	componentsRemoved, err := statuspages.DetachForeignComponents(ctx, tx, a.TenantID, id, target, time.Now())
	if err != nil {
		return nil, from, err
	}
	*changed = append(*changed, "projectId")
	return map[string]any{"from": from.String(), "to": target.String(), "channelsDetached": detached, "statusPageComponentsRemoved": componentsRemoved}, target, nil
}

// moveLocation points the monitor at Proreta's worker (specula nil) or at a Specula the project may
// use.
func (s *Service) moveLocation(ctx context.Context, tx pgx.Tx, a Actor, id, project uuid.UUID, specula *uuid.UUID) error {
	locID, err := s.sched.DefaultLocation(ctx, tx)
	if err != nil {
		return err
	}
	if specula != nil {
		if locID, err = s.speculaLocation(ctx, tx, a.TenantID, *specula, project); err != nil {
			return err
		}
	}
	var result string
	if err := tx.QueryRow(ctx, `SELECT monitor_set_location($1, $2)`, id, locID).Scan(&result); err != nil {
		return fmt.Errorf("set location: %w", err)
	}
	if result != "ok" {
		return speculaFieldError()
	}
	return nil
}

// saveUpdate writes the merged settings and the audit entry of an update that changed something.
func (s *Service) saveUpdate(ctx context.Context, tx pgx.Tx, q *monitorsdb.Queries, a Actor, id uuid.UUID, e *monitorEdit, moveMeta map[string]any) error {
	raw, err := json.Marshal(e.config)
	if err != nil {
		return fmt.Errorf("monitor config: %w", err)
	}
	if err := q.UpdateMonitor(ctx, monitorsdb.UpdateMonitorParams{
		TenantID: a.TenantID, ID: id, Name: e.name, Config: raw, IntervalSeconds: int32(e.interval), //nolint:gosec // validated
		FailureThreshold: int32(e.threshold), NotificationsEnabled: e.notificationsEnabled, //nolint:gosec // validated 1..3
	}); err != nil {
		return fmt.Errorf("update monitor: %w", err)
	}
	// Only field names: the URL may carry tokens in its query string.
	meta := map[string]any{"fields": e.changed}
	if moveMeta != nil {
		meta["move"] = moveMeta
	}
	return s.audit(ctx, tx, a, ActionUpdated, id, meta)
}

// Delete soft-deletes a monitor. It stops being scheduled (the claim skips deleted monitors)
// and no longer counts toward the quota; its history stays until retention.
func (s *Service) Delete(ctx context.Context, a Actor, id uuid.UUID) error {
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := monitorsdb.New(tx)
		cur, err := q.LockMonitor(ctx, monitorsdb.LockMonitorParams{TenantID: a.TenantID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock monitor: %w", err)
		}
		if err := a.checkAccess(cur.ProjectID); err != nil {
			return err
		}
		if cur.Type == TypeHeartbeat {
			if err := lockHeartbeat(ctx, q, a.TenantID, id); err != nil {
				return err
			}
		}
		if err := s.incidents.ResolveOpen(ctx, tx, a.TenantID, id, incidents.ReasonDeleted); err != nil {
			return err
		}
		if err := q.SoftDeleteMonitor(ctx, monitorsdb.SoftDeleteMonitorParams{TenantID: a.TenantID, ID: id}); err != nil {
			return fmt.Errorf("delete monitor: %w", err)
		}
		// Parks the schedule (next_run_at infinity) and drops a lease in flight.
		if err := s.sched.Restart(ctx, tx, id); err != nil {
			return err
		}
		// A published status page drops the component at once.
		if err := statuspages.RefreshForMonitor(ctx, tx, a.TenantID, id, time.Now()); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, ActionDeleted, id, nil)
	})
	return wrap("delete", err)
}

// Pause stops checking a monitor: an open incident is resolved without a notification, a check
// in flight is discarded and the status becomes unknown. Pausing a paused monitor changes nothing.
func (s *Service) Pause(ctx context.Context, a Actor, id uuid.UUID) (Monitor, error) {
	return s.setPaused(ctx, a, id, true)
}

// Resume starts checking a paused monitor again: counters reset, status unknown until the first
// check, which runs after a random jitter. Resuming a running monitor changes nothing.
func (s *Service) Resume(ctx context.Context, a Actor, id uuid.UUID) (Monitor, error) {
	return s.setPaused(ctx, a, id, false)
}

func (s *Service) setPaused(ctx context.Context, a Actor, id uuid.UUID, paused bool) (Monitor, error) {
	var m Monitor
	err := db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		q := monitorsdb.New(tx)
		cur, err := q.LockMonitor(ctx, monitorsdb.LockMonitorParams{TenantID: a.TenantID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock monitor: %w", err)
		}
		if err := a.checkAccess(cur.ProjectID); err != nil {
			return err
		}
		if cur.Paused != paused {
			if cur.Type == TypeHeartbeat {
				// Taken before the state rows, like the evaluation in jobs.
				if err := lockHeartbeat(ctx, q, a.TenantID, id); err != nil {
					return err
				}
				// Resume: the deadline counts from now; pings from before the pause are ignored.
				if !paused {
					if err := q.RestartHeartbeatWait(ctx, monitorsdb.RestartHeartbeatWaitParams{TenantID: a.TenantID, MonitorID: id}); err != nil {
						return fmt.Errorf("restart heartbeat wait: %w", err)
					}
				}
			}
			if err := q.SetMonitorPaused(ctx, monitorsdb.SetMonitorPausedParams{TenantID: a.TenantID, ID: id, Paused: paused}); err != nil {
				return fmt.Errorf("set paused: %w", err)
			}
			// Both directions restart the runtime state. On pause this drops the lease, which
			// fences off a check still in flight, and the status becomes unknown (a paused monitor
			// reports unknown); on resume the first check comes after a random jitter.
			if err := s.restart(ctx, tx, q, a.TenantID, id, false); err != nil {
				return err
			}
			action := ActionResumed
			if paused {
				action = ActionPaused
				if err := s.incidents.ResolveOpen(ctx, tx, a.TenantID, id, incidents.ReasonPaused); err != nil {
					return err
				}
			}
			if err := s.audit(ctx, tx, a, action, id, nil); err != nil {
				return err
			}
			// A published status page shows the paused component as "no data" at once.
			if err := statuspages.RefreshForMonitor(ctx, tx, a.TenantID, id, time.Now()); err != nil {
				return err
			}
		}
		m, err = get(ctx, q, a.TenantID, id)
		return err
	})
	if err != nil {
		return Monitor{}, wrap("set paused", err)
	}
	return m, nil
}

// restart resets the schedule (through the scheduler) and the aggregated state of a monitor;
// clearTLS also forgets the certificate seen so far (URL change).
func (s *Service) restart(ctx context.Context, tx pgx.Tx, q *monitorsdb.Queries, tenantID, id uuid.UUID, clearTLS bool) error {
	if err := s.sched.Restart(ctx, tx, id); err != nil {
		return err
	}
	if err := q.ResetMonitorState(ctx, monitorsdb.ResetMonitorStateParams{TenantID: tenantID, MonitorID: id, ClearTls: clearTLS}); err != nil {
		return fmt.Errorf("reset monitor state: %w", err)
	}
	return nil
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, action string, id uuid.UUID, meta map[string]any) error {
	actor := a.UserID
	return audit.Record(ctx, tx, audit.Entry{
		TenantID: a.TenantID, ActorUserID: &actor, Action: action,
		EntityType: "monitor", EntityID: id.String(), Metadata: meta, IP: a.IP,
	})
}

func get(ctx context.Context, q *monitorsdb.Queries, tenantID, id uuid.UUID) (Monitor, error) {
	row, err := q.GetMonitor(ctx, monitorsdb.GetMonitorParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Monitor{}, ErrNotFound
	}
	if err != nil {
		return Monitor{}, fmt.Errorf("get monitor: %w", err)
	}
	return fromRow(row)
}

func fromRow(r monitorsdb.GetMonitorRow) (Monitor, error) {
	// One struct per type: httpConfig and tcpConfig share the key "request".
	var h httpConfig
	var t tcpConfig
	var hb heartbeatConfig
	for _, dst := range []any{&h, &t, &hb} {
		if err := json.Unmarshal(r.Config, dst); err != nil {
			return Monitor{}, fmt.Errorf("monitor %s config: %w", r.ID, err)
		}
	}
	if r.Type == TypeHTTP && h.Method == "" {
		h.Method = monitorrequest.Methods[0]
	}
	return Monitor{
		ID: r.ID, ProjectID: r.ProjectID, Type: r.Type, Name: r.Name, URL: h.URL, ExpectedStatus: h.ExpectedStatus,
		Method: h.Method, HeaderNames: h.HeaderNames, BodySet: h.BodySet, Host: t.Host, Port: t.Port,
		IntervalSeconds: int(r.IntervalSeconds), TimeoutSeconds: int(r.TimeoutSeconds), GraceSeconds: hb.GraceSeconds,
		FailureThreshold: int(r.FailureThreshold), NotificationsEnabled: r.NotificationsEnabled, Paused: r.Paused,
		Status: r.Status, StatusSince: r.StatusChangedAt, LastErrorKind: r.LastErrorKind,
		LastCheckedAt: r.LastCheckedAt, TLSExpiresAt: r.TlsExpiresAt, CurrentIncidentID: r.CurrentIncidentID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// wrap adds context to unexpected errors and leaves domain errors recognisable.
func wrap(op string, err error) error {
	var verr *validation.Error
	var lerr *plans.LimitError
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotHeartbeat) || errors.Is(err, authz.ErrProjectForbidden) ||
		errors.As(err, &verr) || errors.As(err, &lerr) {
		return err
	}
	return fmt.Errorf("monitors: %s: %w", op, err)
}

func projectFieldError() error {
	var verr validation.Error
	verr.Add("projectId", "does not exist")
	return &verr
}

func checkFixed(verr *validation.Error, field string, got *int, want int) {
	if got != nil && *got != want {
		verr.Add(field, fmt.Sprintf("only %d is supported", want))
	}
}

func checkThreshold(verr *validation.Error, field string, n int) {
	if n < 1 || n > 3 {
		verr.Add(field, "must be between 1 and 3")
	}
}

func appendIf(list []string, item string, cond bool) []string {
	if cond {
		return append(list, item)
	}
	return list
}

func orDefault[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// validateTarget checks a monitor's URL with the rules of where it runs: public https for
// Proreta's own worker, the customer's network for a Specula.
func validateTarget(raw string, specula bool) (string, string) {
	if specula {
		return ValidatePrivateURL(raw)
	}
	return ValidateURL(raw)
}

// speculaLocation returns the location of a Specula that the project may use. Anything else (it
// does not exist, belongs to another organization, is revoked, or is not shared with the
// project) is the same field error.
func (s *Service) speculaLocation(ctx context.Context, tx pgx.Tx, tenantID, speculaID, project uuid.UUID) (uuid.UUID, error) {
	var loc uuid.UUID
	err := tx.QueryRow(ctx, `SELECT a.location_id FROM specula_agents AS a
		WHERE a.tenant_id = $1 AND a.id = $2 AND a.revoked_at IS NULL
		  AND (a.all_projects OR EXISTS (SELECT 1 FROM specula_projects AS sp WHERE sp.specula_id = a.id AND sp.project_id = $3))`,
		tenantID, speculaID, project).Scan(&loc)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, speculaFieldError()
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("specula location: %w", err)
	}
	return loc, nil
}

// currentSpecula returns the Specula a monitor runs at, nil for Proreta's own worker.
func currentSpecula(ctx context.Context, tx pgx.Tx, tenantID, monitorID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT a.id FROM monitor_location_state AS s
		JOIN specula_agents AS a ON a.location_id = s.location_id AND a.tenant_id = s.tenant_id
		WHERE s.tenant_id = $1 AND s.monitor_id = $2 LIMIT 1`, tenantID, monitorID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("current specula: %w", err)
	}
	return &id, nil
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func speculaFieldError() error {
	var verr validation.Error
	verr.Add(fieldSpeculaID, "does not exist or is not available to this project")
	return &verr
}
