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
	"github.com/proreta-io/proreta/backend/internal/monitors/monitorsdb"
	"github.com/proreta-io/proreta/backend/internal/notify"
	"github.com/proreta-io/proreta/backend/internal/plans"
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
	Heartbeat            *HeartbeatSettings
	// ProjectID is the monitor's project; nil means the Default project.
	ProjectID *uuid.UUID
	// ChannelIDs selects notification channels of the project or organization-wide ones (none:
	// the active channels of the project).
	ChannelIDs *[]uuid.UUID
}

// HTTPSettings are the settings of an http monitor; nil fields take their defaults on create
// and keep their value on update. URL is required on create.
type HTTPSettings struct {
	URL              *string
	ExpectedStatus   *string
	IntervalSeconds  *int
	TimeoutSeconds   *int
	FailureThreshold *int
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
	Heartbeat            *HeartbeatSettings
	// ProjectID moves the monitor; selected channels of the old project are dropped.
	ProjectID *uuid.UUID
	// ChannelIDs replaces the channel selection.
	ChannelIDs *[]uuid.UUID
}

// httpConfig is monitors.config for type http.
type httpConfig struct {
	URL            string `json:"url"`
	ExpectedStatus string `json:"expectedStatus"`
}

// heartbeatConfig is monitors.config for type heartbeat.
type heartbeatConfig struct {
	GraceSeconds int `json:"graceSeconds"`
}

// Field names of validation errors and audit records: the API's, with the sub-object prefix.
const (
	fieldHTTP                    = "http"
	fieldHeartbeat               = "heartbeat"
	fieldURL                     = "http.url"
	fieldExpectedStatus          = "http.expectedStatus"
	fieldIntervalSeconds         = "http.intervalSeconds"
	fieldTimeoutSeconds          = "http.timeoutSeconds"
	fieldFailureThreshold        = "http.failureThreshold"
	fieldExpectedIntervalSeconds = "heartbeat.expectedIntervalSeconds"
	fieldGraceSeconds            = "heartbeat.graceSeconds"

	msgHTTPOnly      = "is only for http monitors"
	msgHeartbeatOnly = "is only for heartbeat monitors"
)

// Service implements the monitor lifecycle.
type Service struct {
	pool      *db.Pool
	sched     *scheduler.Service
	incidents *incidents.Service
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
	var row monitorsdb.InsertMonitorParams
	var config any
	resource := plans.ResourceMonitor
	switch in.Type {
	case "", TypeHTTP:
		row.Type = TypeHTTP
		if in.Heartbeat != nil {
			verr.Add(fieldHeartbeat, msgHeartbeatOnly)
		}
		h := in.HTTP
		if h == nil {
			verr.Add(fieldHTTP, "is required for http monitors")
			h = &HTTPSettings{}
		}
		var target string
		if h.URL == nil {
			if in.HTTP != nil {
				verr.Add(fieldURL, "is required")
			}
		} else if target, msg = ValidateURL(*h.URL); msg != "" {
			verr.Add(fieldURL, msg)
		}
		checkFixed(&verr, fieldIntervalSeconds, h.IntervalSeconds, IntervalSeconds)
		checkFixed(&verr, fieldTimeoutSeconds, h.TimeoutSeconds, TimeoutSeconds)
		expected := orDefault(h.ExpectedStatus, DefaultExpectedStatus)
		if msg := ValidateExpectedStatus(expected); msg != "" {
			verr.Add(fieldExpectedStatus, msg)
		}
		threshold := orDefault(h.FailureThreshold, DefaultFailureThreshold)
		checkThreshold(&verr, threshold)
		config = httpConfig{URL: target, ExpectedStatus: expected}
		row.IntervalSeconds, row.FailureThreshold = IntervalSeconds, int32(threshold) //nolint:gosec // validated 1..3
	case TypeHeartbeat:
		row.Type, resource = TypeHeartbeat, plans.ResourceHeartbeat
		if in.HTTP != nil {
			verr.Add(fieldHTTP, msgHTTPOnly)
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
		config = heartbeatConfig{GraceSeconds: grace}
		// A heartbeat goes down at its first missed deadline: its failure threshold is always 1.
		row.IntervalSeconds, row.FailureThreshold = int32(interval), 1 //nolint:gosec // validated 60..86400
	default:
		verr.Add("type", `must be "http" or "heartbeat"`)
	}
	if err := verr.Err(); err != nil {
		return Monitor{}, err
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return Monitor{}, fmt.Errorf("monitors: config: %w", err)
	}
	var token string
	var hash []byte
	if row.Type == TypeHeartbeat {
		if token, hash, err = NewHeartbeatToken(); err != nil {
			return Monitor{}, err
		}
	}

	id := uuidv7.New()
	row.ID, row.TenantID, row.Name, row.Config = id, a.TenantID, name, raw
	row.TimeoutSeconds, row.NotificationsEnabled = TimeoutSeconds, orDefault(in.NotificationsEnabled, true)
	var m Monitor
	err = db.WithTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		// The tenant row lock taken here serialises concurrent creates, so the count stays true
		// until this transaction commits.
		if err := plans.CheckCreate(ctx, tx, a.TenantID, resource); err != nil {
			return err
		}
		q := monitorsdb.New(tx)
		project, err := projects.ForUse(ctx, tx, a.TenantID, in.ProjectID)
		if errors.Is(err, projects.ErrNotFound) || (err == nil && !a.Access.Sees(project)) {
			return projectFieldError()
		}
		if err != nil {
			return err
		}
		if !a.Access.CanEditIn(project) {
			return authz.ErrProjectForbidden
		}
		row.ProjectID = project
		location, err := s.sched.DefaultLocation(ctx, tx)
		if err != nil {
			return err
		}
		if err := q.InsertMonitor(ctx, row); err != nil {
			return fmt.Errorf("insert monitor: %w", err)
		}
		if err := q.InsertMonitorLocation(ctx, monitorsdb.InsertMonitorLocationParams{TenantID: a.TenantID, MonitorID: id, LocationID: location}); err != nil {
			return fmt.Errorf("insert monitor location: %w", err)
		}
		if row.Type == TypeHeartbeat {
			// The location state only carries the incident machine's state; nothing claims it.
			if err := s.sched.InitParkedMonitorLocation(ctx, tx, a.TenantID, id, location); err != nil {
				return err
			}
			if err := q.InsertHeartbeatToken(ctx, monitorsdb.InsertHeartbeatTokenParams{TenantID: a.TenantID, MonitorID: id, TokenHash: hash}); err != nil {
				return fmt.Errorf("insert heartbeat token: %w", err)
			}
		} else if err := s.sched.InitMonitorLocation(ctx, tx, a.TenantID, id, location); err != nil {
			return err
		}
		if err := q.InsertMonitorState(ctx, monitorsdb.InsertMonitorStateParams{TenantID: a.TenantID, MonitorID: id}); err != nil {
			return fmt.Errorf("insert monitor state: %w", err)
		}
		if in.ChannelIDs != nil {
			if _, err := notify.SetMonitorChannels(ctx, tx, a.TenantID, id, project, *in.ChannelIDs); err != nil {
				return err
			}
		}
		if err := s.audit(ctx, tx, a, ActionCreated, id, map[string]any{"type": row.Type, "projectId": project.String()}); err != nil {
			return err
		}
		m, err = get(ctx, q, a.TenantID, id)
		return err
	})
	if err != nil {
		return Monitor{}, wrap("create", err)
	}
	m.HeartbeatToken = token
	return m, nil
}

// Update changes the given fields, also inside the type's settings. The interval and timeout of
// an http monitor are fixed: they may be sent only with their current value. A heartbeat's
// interval and grace may change; its deadline then follows the new values.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, in UpdateInput) (Monitor, error) {
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
		name, interval, threshold, notificationsEnabled := cur.Name, int(cur.IntervalSeconds), int(cur.FailureThreshold), cur.NotificationsEnabled

		var verr validation.Error
		var changed []string
		if in.Name != nil {
			v, msg := ValidateName(*in.Name)
			if msg != "" {
				verr.Add("name", msg)
			}
			changed, name = appendIf(changed, "name", v != name), v
		}
		if in.NotificationsEnabled != nil {
			changed, notificationsEnabled = appendIf(changed, "notificationsEnabled", *in.NotificationsEnabled != notificationsEnabled), *in.NotificationsEnabled
		}

		var config any
		if cur.Type == TypeHeartbeat {
			var cfg heartbeatConfig
			if err := json.Unmarshal(cur.Config, &cfg); err != nil {
				return fmt.Errorf("monitor config: %w", err)
			}
			if in.HTTP != nil {
				verr.Add(fieldHTTP, msgHTTPOnly)
			}
			if hb := in.Heartbeat; hb != nil {
				if hb.ExpectedIntervalSeconds != nil {
					if msg := ValidateExpectedInterval(*hb.ExpectedIntervalSeconds); msg != "" {
						verr.Add(fieldExpectedIntervalSeconds, msg)
					}
					changed, interval = appendIf(changed, fieldExpectedIntervalSeconds, *hb.ExpectedIntervalSeconds != interval), *hb.ExpectedIntervalSeconds
				}
				if hb.GraceSeconds != nil {
					if msg := ValidateGrace(*hb.GraceSeconds); msg != "" {
						verr.Add(fieldGraceSeconds, msg)
					}
					changed, cfg.GraceSeconds = appendIf(changed, fieldGraceSeconds, *hb.GraceSeconds != cfg.GraceSeconds), *hb.GraceSeconds
				}
			}
			config = cfg
		} else {
			var cfg httpConfig
			if err := json.Unmarshal(cur.Config, &cfg); err != nil {
				return fmt.Errorf("monitor config: %w", err)
			}
			if in.Heartbeat != nil {
				verr.Add(fieldHeartbeat, msgHeartbeatOnly)
			}
			if h := in.HTTP; h != nil {
				checkFixed(&verr, fieldIntervalSeconds, h.IntervalSeconds, int(cur.IntervalSeconds))
				checkFixed(&verr, fieldTimeoutSeconds, h.TimeoutSeconds, int(cur.TimeoutSeconds))
				if h.URL != nil {
					v, msg := ValidateURL(*h.URL)
					if msg != "" {
						verr.Add(fieldURL, msg)
					}
					changed, cfg.URL = appendIf(changed, fieldURL, v != cfg.URL), v
				}
				if h.ExpectedStatus != nil {
					if msg := ValidateExpectedStatus(*h.ExpectedStatus); msg != "" {
						verr.Add(fieldExpectedStatus, msg)
					}
					changed, cfg.ExpectedStatus = appendIf(changed, fieldExpectedStatus, *h.ExpectedStatus != cfg.ExpectedStatus), *h.ExpectedStatus
				}
				if h.FailureThreshold != nil {
					checkThreshold(&verr, *h.FailureThreshold)
					changed, threshold = appendIf(changed, fieldFailureThreshold, *h.FailureThreshold != threshold), *h.FailureThreshold
				}
			}
			config = cfg
		}
		if err := verr.Err(); err != nil {
			return err
		}
		project := cur.ProjectID
		var moveMeta map[string]any
		if in.ProjectID != nil && *in.ProjectID != project {
			target, err := projects.ForUse(ctx, tx, a.TenantID, in.ProjectID)
			if errors.Is(err, projects.ErrNotFound) || (err == nil && !a.Access.Sees(target)) {
				return projectFieldError()
			}
			if err != nil {
				return err
			}
			if !a.Access.CanEditIn(target) {
				return authz.ErrProjectForbidden
			}
			if err := q.SetMonitorProject(ctx, monitorsdb.SetMonitorProjectParams{TenantID: a.TenantID, ID: id, ProjectID: target}); err != nil {
				return fmt.Errorf("move monitor: %w", err)
			}
			// Channels of the old project are not allowed in the new one.
			detached, err := notify.DetachForeignChannels(ctx, tx, a.TenantID, id, target)
			if err != nil {
				return err
			}
			// Pages of the old project may show only its monitors.
			componentsRemoved, err := statuspages.DetachForeignComponents(ctx, tx, a.TenantID, id, target, time.Now())
			if err != nil {
				return err
			}
			moveMeta = map[string]any{"from": project.String(), "to": target.String(), "channelsDetached": detached, "statusPageComponentsRemoved": componentsRemoved}
			changed, project = append(changed, "projectId"), target
		}
		if in.ChannelIDs != nil {
			selected, err := notify.SetMonitorChannels(ctx, tx, a.TenantID, id, project, *in.ChannelIDs)
			if err != nil {
				return err
			}
			changed = appendIf(changed, "channelIds", selected)
		}
		if slices.Contains(changed, fieldURL) {
			// A new URL is a new target: its history says nothing about it. Restart like resume
			// and end an open incident without a notification.
			if err := s.restart(ctx, tx, q, a.TenantID, id, true); err != nil {
				return err
			}
			if err := s.incidents.ResolveOpen(ctx, tx, a.TenantID, id, incidents.ReasonURLChanged); err != nil {
				return err
			}
		}
		if len(changed) > 0 {
			raw, err := json.Marshal(config)
			if err != nil {
				return fmt.Errorf("monitor config: %w", err)
			}
			if err := q.UpdateMonitor(ctx, monitorsdb.UpdateMonitorParams{
				TenantID: a.TenantID, ID: id, Name: name, Config: raw, IntervalSeconds: int32(interval), //nolint:gosec // validated
				FailureThreshold: int32(threshold), NotificationsEnabled: notificationsEnabled, //nolint:gosec // validated 1..3
			}); err != nil {
				return fmt.Errorf("update monitor: %w", err)
			}
			// Only field names: the URL may carry tokens in its query string.
			meta := map[string]any{"fields": changed}
			if moveMeta != nil {
				meta["move"] = moveMeta
			}
			if err := s.audit(ctx, tx, a, ActionUpdated, id, meta); err != nil {
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
	var cfg struct {
		httpConfig
		heartbeatConfig
	}
	if err := json.Unmarshal(r.Config, &cfg); err != nil {
		return Monitor{}, fmt.Errorf("monitor %s config: %w", r.ID, err)
	}
	return Monitor{
		ID: r.ID, ProjectID: r.ProjectID, Type: r.Type, Name: r.Name, URL: cfg.URL, ExpectedStatus: cfg.ExpectedStatus,
		IntervalSeconds: int(r.IntervalSeconds), TimeoutSeconds: int(r.TimeoutSeconds), GraceSeconds: cfg.GraceSeconds,
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

func checkThreshold(verr *validation.Error, n int) {
	if n < 1 || n > 3 {
		verr.Add(fieldFailureThreshold, "must be between 1 and 3")
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
