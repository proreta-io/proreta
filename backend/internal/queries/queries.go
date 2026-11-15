// Package queries holds the read models of the dashboard: monitor list and detail, statistics,
// recent checks and incidents. They read across tables owned by monitors, scheduler, results,
// incidents and notify (a monitor's channel selection) but never write (each table has one
// owning module, see CONTRIBUTING.md).
// Every query runs in a tenant transaction (db.WithTenantTx) and reads raw results only through
// the (monitor_id, checked_at) key, for bounded windows; longer ranges come from the hourly
// rollups.
package queries

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/authz"
	"github.com/proreta-io/proreta/backend/internal/platform/clock"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/platform/pagecursor"
)

// ErrNotFound means no live monitor with that id exists in the tenant.
var ErrNotFound = errors.New("queries: monitor not found")

// Monitor statuses as reported to clients.
const (
	StatusUnknown = "unknown"
	StatusUp      = "up"
	StatusDown    = "down"
)

// Limits of the list endpoints.
const (
	DefaultCheckLimit = 50
	MaxCheckLimit     = 200
	MaxIncidents      = 100
)

// unknownReason stands in for an incident without a recorded error kind.
const unknownReason = "unknown"

// Service answers the dashboard's read requests.
type Service struct {
	pool  *db.Pool
	clock clock.Clock
}

// New returns a Service; c defaults to the system clock.
func New(pool *db.Pool, c clock.Clock) *Service {
	if c == nil {
		c = clock.System{}
	}
	return &Service{pool: pool, clock: c}
}

// Incident is one incident of a monitor; ResolvedAt is nil while it is ongoing.
type Incident struct {
	ID         uuid.UUID
	StartedAt  time.Time
	ResolvedAt *time.Time
	Reason     string
}

// Monitor is a monitor with its current state, as the list shows it.
type Monitor struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	// Type is "http", "tcp" or "heartbeat".
	Type           string
	Name           string
	URL            string
	ExpectedStatus string
	// Host and Port are the target of a tcp monitor.
	Host string
	Port int
	// Method, HeaderNames and BodySet describe the request of an http monitor; values are
	// sealed and never read here.
	Method      string
	HeaderNames []string
	BodySet     bool
	// SpeculaID and SpeculaName are set when an http monitor runs at a Specula.
	SpeculaID            *uuid.UUID
	SpeculaName          string
	IntervalSeconds      int
	TimeoutSeconds       int
	FailureThreshold     int
	NotificationsEnabled bool
	Paused               bool
	// Status is "unknown" for a paused monitor (it is not being checked).
	Status        string
	StatusSince   time.Time
	LastErrorKind *string
	LastCheckedAt *time.Time
	TLSExpiresAt  *time.Time
	// LastPingAt is a heartbeat's newest accepted ping; GraceSeconds its grace. Both are nil for
	// http monitors (and LastPingAt before the first ping).
	LastPingAt   *time.Time
	GraceSeconds *int
	// Ongoing is the open incident, nil when there is none.
	Ongoing *Incident
	// Uptime24h is the percentage of successful checks in the last 24 h; nil when none ran.
	Uptime24h *float64
	// LastResponseMs is the latency of the newest check; nil when none ran or it got no response.
	LastResponseMs *int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Detail is one monitor with uptime over 24 h, 7 d and 30 d.
type Detail struct {
	Monitor
	Uptime7d  *float64
	Uptime30d *float64
	// ChannelIDs is the monitor's channel selection (monitor_channels); empty means every active
	// channel of the tenant.
	ChannelIDs []uuid.UUID
}

// Summary counts monitors by display state; a paused monitor counts only as paused.
type Summary struct {
	Up, Down, Paused, Unknown int
}

const monitorColumns = `
SELECT m.id, m.project_id, m.type, m.name, COALESCE(m.config ->> 'url', ''), COALESCE(m.config ->> 'expectedStatus', '2xx'),
       COALESCE(m.config ->> 'method', 'GET'), COALESCE(m.config -> 'headerNames', '[]'::jsonb), COALESCE((m.config ->> 'bodySet')::boolean, false),
       m.interval_seconds, m.timeout_seconds, m.failure_threshold, m.notifications_enabled, m.paused,
       m.created_at, m.updated_at,
       s.status, s.status_changed_at, s.last_error_kind, s.last_checked_at, s.tls_expires_at,
       i.id, i.started_at, COALESCE(i.reason, '` + unknownReason + `'),
       lr.latency_ms, h.last_ping_at,
       CASE WHEN m.type = 'heartbeat' THEN (m.config ->> 'graceSeconds')::numeric::integer END,
       sp.id, sp.name,
       COALESCE(m.config ->> 'host', ''), COALESCE((m.config ->> 'port')::integer, 0)
FROM monitors AS m
JOIN monitor_state AS s ON s.tenant_id = m.tenant_id AND s.monitor_id = m.id
LEFT JOIN heartbeat_tokens AS h ON h.tenant_id = m.tenant_id AND h.monitor_id = m.id
-- The Specula an http monitor runs at: the one whose location holds its schedule.
LEFT JOIN LATERAL (
    SELECT a.id, a.name
    FROM monitor_location_state AS mls
    JOIN specula_agents AS a ON a.tenant_id = mls.tenant_id AND a.location_id = mls.location_id
    WHERE mls.tenant_id = m.tenant_id AND mls.monitor_id = m.id
    LIMIT 1
) AS sp ON true
LEFT JOIN incidents AS i ON i.tenant_id = s.tenant_id AND i.id = s.current_incident_id AND i.resolved_at IS NULL
-- The newest result through the primary key (monitor_id, checked_at); the lower bound lets the
-- planner prune old monthly partitions.
LEFT JOIN LATERAL (
    SELECT c.latency_ms
    FROM check_results AS c
    WHERE c.monitor_id = m.id AND s.last_checked_at IS NOT NULL
      AND c.checked_at >= s.last_checked_at - interval '1 hour'
    ORDER BY c.checked_at DESC
    LIMIT 1
) AS lr ON true
WHERE m.tenant_id = $1 AND m.deleted_at IS NULL`

func scanMonitor(row pgx.Row) (Monitor, error) {
	var m Monitor
	var interval, timeout, threshold int32
	var incID *uuid.UUID
	var incStarted *time.Time
	var incReason string
	var latency, grace *int32
	var speculaName *string
	err := row.Scan(&m.ID, &m.ProjectID, &m.Type, &m.Name, &m.URL, &m.ExpectedStatus, &m.Method, &m.HeaderNames, &m.BodySet, &interval, &timeout, &threshold, &m.NotificationsEnabled, &m.Paused,
		&m.CreatedAt, &m.UpdatedAt, &m.Status, &m.StatusSince, &m.LastErrorKind, &m.LastCheckedAt, &m.TLSExpiresAt,
		&incID, &incStarted, &incReason, &latency, &m.LastPingAt, &grace, &m.SpeculaID, &speculaName, &m.Host, &m.Port)
	if err != nil {
		return Monitor{}, err
	}
	m.IntervalSeconds, m.TimeoutSeconds, m.FailureThreshold = int(interval), int(timeout), int(threshold)
	if speculaName != nil {
		m.SpeculaName = *speculaName
	}
	if m.Paused {
		m.Status = StatusUnknown
	}
	if incID != nil && incStarted != nil {
		m.Ongoing = &Incident{ID: *incID, StartedAt: *incStarted, Reason: incReason}
	}
	if latency != nil {
		v := int(*latency)
		m.LastResponseMs = &v
	}
	if grace != nil {
		v := int(*grace)
		m.GraceSeconds = &v
	}
	return m, nil
}

// scopeSQL limits monitors (alias m) to the projects of a scope: every project
// when $all is true, otherwise those in $ids. The placeholders are numbered from n.
func scopeSQL(n int) string {
	return fmt.Sprintf(` AND ($%d::boolean OR m.project_id = ANY($%d::uuid[]))`, n, n+1)
}

func scopeArgs(sc authz.Scope) []any {
	ids := sc.IDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return []any{sc.All, ids}
}

// MaxSearchRunes bounds a monitor search text.
const MaxSearchRunes = 100

// MonitorFilter narrows the list; the zero value keeps every monitor. Status is "up", "down",
// "paused" or "unknown" (the summary's own classes, see Summary); Search is a case-insensitive
// substring of the name, URL or host:port, taken literally (no pattern characters).
type MonitorFilter struct {
	Search string
	Status string
}

// monitorRank is the leading sort key: down monitors first, then the active ones, paused last.
const monitorRank = `(CASE WHEN m.paused THEN 2 WHEN s.status = '` + StatusDown + `' THEN 0 ELSE 1 END)`

// statusSQL is the condition of each status filter, the same classes as summarySQL.
var statusSQL = map[string]string{
	StatusUp:      `NOT m.paused AND s.status = '` + StatusUp + `'`,
	StatusDown:    `NOT m.paused AND s.status = '` + StatusDown + `'`,
	"paused":      `m.paused`,
	StatusUnknown: `NOT m.paused AND s.status NOT IN ('` + StatusUp + `', '` + StatusDown + `')`,
}

// ValidStatus reports whether s is a status filter value.
func ValidStatus(s string) bool { _, ok := statusSQL[s]; return ok }

// ListMonitors returns one page of the live monitors of the tenant in the scope's projects that
// match the filter. The order is explicit: down monitors, then the other active ones, then paused
// ones, each oldest first (rank, created_at, id: a total order, so pages neither skip nor repeat
// a monitor while rows are added or removed; a monitor whose state changes between two requests
// may move to another class and be seen twice or missed, as with any keyset on a mutable key).
// The cursor of the next page is "" on the last. The summary counts every monitor of the scope,
// never the filtered rows. Uptime comes from the hourly rollups plus the raw rows of the hours
// not rolled up yet, for the page only.
func (s *Service) ListMonitors(ctx context.Context, tenantID uuid.UUID, sc authz.Scope, f MonitorFilter, page pagecursor.Page) ([]Monitor, Summary, string, error) {
	now := s.clock.Now()
	var out []Monitor
	var sum Summary
	var next string
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		args := append([]any{tenantID}, scopeArgs(sc)...)
		if err := tx.QueryRow(ctx, summarySQL+scopeSQL(2), args...).Scan(&sum.Up, &sum.Down, &sum.Paused, &sum.Unknown); err != nil {
			return fmt.Errorf("summary: %w", err)
		}
		q := monitorColumns + scopeSQL(2)
		if c, ok := statusSQL[f.Status]; ok {
			q += ` AND (` + c + `)`
		}
		if f.Search != "" {
			// position() on lower-cased text, not LIKE: the text has no wildcards to escape.
			args = append(args, f.Search)
			n := len(args)
			q += fmt.Sprintf(` AND (position(lower($%[1]d) in lower(m.name)) > 0
				OR position(lower($%[1]d) in lower(COALESCE(m.config ->> 'url', ''))) > 0
				OR (m.type = 'tcp' AND position(lower($%[1]d) in lower((m.config ->> 'host') || ':' || (m.config ->> 'port'))) > 0))`, n)
		}
		if page.After != nil {
			args = append(args, page.After.Rank, page.After.At, page.After.ID)
			n := len(args)
			q += fmt.Sprintf(` AND (%s, m.created_at, m.id) > ($%d, $%d, $%d)`, monitorRank, n-2, n-1, n)
		}
		q += ` ORDER BY ` + monitorRank + `, m.created_at, m.id`
		if n := page.Size(); n > 0 {
			args = append(args, n)
			q += fmt.Sprintf(` LIMIT $%d`, len(args))
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Monitor, error) { return scanMonitor(r) })
		if err != nil {
			return err
		}
		out, next = pagecursor.Trim(page, out, func(m Monitor) pagecursor.Position {
			rank := 1
			switch {
			case m.Paused:
				rank = 2
			case m.Status == StatusDown:
				rank = 0
			}
			return pagecursor.Position{Rank: rank, At: m.CreatedAt, ID: m.ID}
		})
		ids := make([]uuid.UUID, len(out))
		for i, m := range out {
			ids[i] = m.ID
		}
		up, err := uptime(ctx, tx, ids, now.Add(-24*time.Hour), now)
		if err != nil {
			return err
		}
		for i := range out {
			out[i].Uptime24h = up[out[i].ID]
		}
		return nil
	})
	if err != nil {
		return nil, Summary{}, "", fmt.Errorf("queries: list monitors: %w", err)
	}
	if out == nil {
		out = []Monitor{}
	}
	return out, sum, next, nil
}

// summarySQL counts the live monitors of the scope by what the list shows: a paused monitor only
// as paused, then up, down, and everything else as unknown. Shares its WHERE with monitorColumns.
const summarySQL = `
SELECT count(*) FILTER (WHERE NOT m.paused AND s.status = '` + StatusUp + `'),
       count(*) FILTER (WHERE NOT m.paused AND s.status = '` + StatusDown + `'),
       count(*) FILTER (WHERE m.paused),
       count(*) FILTER (WHERE NOT m.paused AND s.status NOT IN ('` + StatusUp + `', '` + StatusDown + `'))
FROM monitors AS m
JOIN monitor_state AS s ON s.tenant_id = m.tenant_id AND s.monitor_id = m.id
WHERE m.tenant_id = $1 AND m.deleted_at IS NULL`

// Monitor returns one live monitor of the scope's projects with uptime over 24 h, 7 d and 30 d;
// ErrNotFound for a monitor of another project.
func (s *Service) Monitor(ctx context.Context, tenantID uuid.UUID, sc authz.Scope, id uuid.UUID) (Detail, error) {
	now := s.clock.Now()
	var d Detail
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		m, err := scanMonitor(tx.QueryRow(ctx, monitorColumns+` AND m.id = $2`+scopeSQL(3), append([]any{tenantID, id}, scopeArgs(sc)...)...))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		d.Monitor = m
		ids := []uuid.UUID{id}
		for _, w := range []struct {
			span time.Duration
			dst  **float64
		}{{24 * time.Hour, &d.Uptime24h}, {7 * 24 * time.Hour, &d.Uptime7d}, {30 * 24 * time.Hour, &d.Uptime30d}} {
			up, err := uptime(ctx, tx, ids, now.Add(-w.span), now)
			if err != nil {
				return err
			}
			*w.dst = up[id]
		}
		// Only links routing honours: organization-wide channels and those of the monitor's project.
		// A link to another project's channel cannot be created (moves drop them); the filter stays as a guard.
		rows, err := tx.Query(ctx, `
			SELECT mc.channel_id
			FROM monitor_channels AS mc
			JOIN notification_channels AS c ON c.tenant_id = mc.tenant_id AND c.id = mc.channel_id
			WHERE mc.tenant_id = $1 AND mc.monitor_id = $2
			  AND (c.project_id IS NULL OR c.project_id IS NOT DISTINCT FROM $3::uuid)
			ORDER BY mc.created_at, mc.channel_id`, tenantID, id, m.ProjectID)
		if err != nil {
			return fmt.Errorf("channels: %w", err)
		}
		d.ChannelIDs, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return fmt.Errorf("channels: %w", err)
		}
		if d.ChannelIDs == nil {
			d.ChannelIDs = []uuid.UUID{}
		}
		return nil
	})
	if err != nil {
		return Detail{}, wrap("monitor", err)
	}
	return d, nil
}

// uptimeSQL counts checks of each monitor in [$2, $5): rollups for the whole hours
// [$3, $4) that are rolled up, raw rows for the partial first hour [$2, $3) and for the hours
// after the newest rollup (the current hour and any hour jobs has not rolled up yet).
const uptimeSQL = `
SELECT m.id,
       COALESCE(r.total, 0) + head.total + tail.total,
       COALESCE(r.ok, 0) + head.ok + tail.ok
FROM unnest($1::uuid[]) AS m(id)
CROSS JOIN LATERAL (
    SELECT COALESCE(max(h.hour) + interval '1 hour', $3::timestamptz) AS raw_from,
           sum(h.total) AS total, sum(h.ok_count) AS ok
    FROM result_rollups_hourly AS h
    WHERE h.monitor_id = m.id AND h.hour >= $3 AND h.hour < $4
) AS r
CROSS JOIN LATERAL (
    SELECT count(*) AS total, count(*) FILTER (WHERE c.ok) AS ok
    FROM check_results AS c
    WHERE c.monitor_id = m.id AND c.checked_at >= $2 AND c.checked_at < $3
) AS head
CROSS JOIN LATERAL (
    SELECT count(*) AS total, count(*) FILTER (WHERE c.ok) AS ok
    FROM check_results AS c
    WHERE c.monitor_id = m.id AND c.checked_at >= r.raw_from AND c.checked_at < $5
) AS tail`

// uptime returns, per monitor, the percentage of successful checks in [from, to), or nil when
// no check ran. Checks that ran are the only denominator, so paused time does not count.
func uptime(ctx context.Context, tx pgx.Tx, ids []uuid.UUID, from, to time.Time) (map[uuid.UUID]*float64, error) {
	out := make(map[uuid.UUID]*float64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	from, to = from.UTC(), to.UTC()
	firstHour := from.Truncate(time.Hour)
	if firstHour.Before(from) {
		firstHour = firstHour.Add(time.Hour)
	}
	rows, err := tx.Query(ctx, uptimeSQL, ids, from, firstHour, to.Truncate(time.Hour), to)
	if err != nil {
		return nil, fmt.Errorf("uptime: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var total, ok int64
		if err := rows.Scan(&id, &total, &ok); err != nil {
			return nil, fmt.Errorf("uptime: %w", err)
		}
		out[id] = ratio(ok, total, 100)
	}
	return out, rows.Err()
}

func ratio(ok, total int64, scale float64) *float64 {
	if total <= 0 {
		return nil
	}
	v := float64(ok) / float64(total) * scale
	return &v
}

// Check is one raw check result.
type Check struct {
	CheckedAt  time.Time
	Location   string
	OK         bool
	HTTPStatus *int
	ResponseMs *int
	ErrorKind  *string
}

// Checks returns the newest checks of a monitor, at most limit (1..MaxCheckLimit).
func (s *Service) Checks(ctx context.Context, tenantID uuid.UUID, sc authz.Scope, id uuid.UUID, limit int) ([]Check, error) {
	limit = min(max(limit, 1), MaxCheckLimit)
	var out []Check
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := exists(ctx, tx, tenantID, sc, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT c.checked_at, l.code, c.ok, c.status_code, c.latency_ms, c.error_kind
			FROM check_results AS c
			JOIN probe_locations AS l ON l.id = c.location_id
			WHERE c.monitor_id = $1
			ORDER BY c.checked_at DESC, l.code
			LIMIT $2`, id, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Check, error) {
			var c Check
			var status *int16
			var latency *int32
			if err := r.Scan(&c.CheckedAt, &c.Location, &c.OK, &status, &latency, &c.ErrorKind); err != nil {
				return c, err
			}
			if status != nil {
				v := int(*status)
				c.HTTPStatus = &v
			}
			if latency != nil {
				v := int(*latency)
				c.ResponseMs = &v
			}
			return c, nil
		})
		return err
	})
	if err != nil {
		return nil, wrap("checks", err)
	}
	if out == nil {
		out = []Check{}
	}
	return out, nil
}

// Incidents returns the incidents of a monitor, newest first, at most MaxIncidents.
func (s *Service) Incidents(ctx context.Context, tenantID uuid.UUID, sc authz.Scope, id uuid.UUID) ([]Incident, error) {
	var out []Incident
	err := db.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := exists(ctx, tx, tenantID, sc, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT i.id, i.started_at, i.resolved_at, COALESCE(i.reason, '`+unknownReason+`')
			FROM incident_monitors AS im
			JOIN incidents AS i ON i.tenant_id = im.tenant_id AND i.id = im.incident_id
			WHERE im.tenant_id = $1 AND im.monitor_id = $2
			ORDER BY i.started_at DESC, i.id DESC
			LIMIT $3`, tenantID, id, MaxIncidents)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Incident, error) {
			var i Incident
			err := r.Scan(&i.ID, &i.StartedAt, &i.ResolvedAt, &i.Reason)
			return i, err
		})
		return err
	})
	if err != nil {
		return nil, wrap("incidents", err)
	}
	if out == nil {
		out = []Incident{}
	}
	return out, nil
}

// exists returns ErrNotFound unless id is a live monitor of the scope's projects.
func exists(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sc authz.Scope, id uuid.UUID) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM monitors AS m WHERE m.tenant_id = $1 AND m.id = $2 AND m.deleted_at IS NULL`+scopeSQL(3),
		append([]any{tenantID, id}, scopeArgs(sc)...)...).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func wrap(op string, err error) error {
	if errors.Is(err, ErrNotFound) {
		return err
	}
	return fmt.Errorf("queries: %s: %w", op, err)
}
