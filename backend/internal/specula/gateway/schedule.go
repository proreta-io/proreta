package gateway

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/monitorrequest"
	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/specula"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

const (
	defaultScheduleTTL = 24 * time.Hour
	// versionCacheTTL is how long hello and reports reuse a Specula's schedule version instead of
	// decrypting the schedule again. A pause, a delete, an edit or a revocation shows in the answer
	// at most this long later, like the token cache; GET /schedule is never cached.
	versionCacheTTL = 5 * time.Second
	refreshAfter    = 300 // seconds
)

var errScheduleTooLarge = errors.New("specula: schedule over the cap")

// buildSchedule returns the monitors of the Specula's location in canonical form (sorted by
// monitor, headers sorted) and their version: a hash of exactly what is served, so the same
// content always has the same version. The monitors come from open epochs of this Specula's own
// tenant and location only (the SQL function takes both from the token's identity). A monitor
// whose sealed request cannot be opened is left out and logged without values.
func (g *Handler) buildSchedule(ctx context.Context, id specula.Identity) ([]wire.ScheduledMonitor, string, error) {
	started := time.Now()
	defer func() { g.metrics.scheduleBuild.Observe(time.Since(started).Seconds()) }()
	out := []wire.ScheduledMonitor{}
	err := db.WithTx(ctx, g.d.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT monitor_id, epoch, anchor, interval_seconds, timeout_seconds, monitor_type, url,
			expected_status, method, request, host, port FROM specula_schedule($1, $2)`, id.TenantID, id.SpeculaID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m wire.ScheduledMonitor
			var typ, request string
			if err := rows.Scan(&m.MonitorID, &m.Epoch, &m.Anchor, &m.IntervalSeconds, &m.TimeoutSeconds, &typ, &m.URL,
				&m.ExpectedStatus, &m.Method, &request, &m.Host, &m.Port); err != nil {
				return err
			}
			m.Active = true
			if typ == "tcp" {
				m.Type, m.URL, m.Method, m.ExpectedStatus = wire.TypeTCP, "", "", ""
			} else {
				m.Host, m.Port = "", 0
			}
			if request != "" {
				sec, err := monitorrequest.Open(g.d.RequestKeys, id.TenantID, m.MonitorID, request)
				if err != nil {
					g.d.Log.ErrorContext(ctx, "cannot open the sealed request of a monitor, left out of the schedule",
						"tenant_id", id.TenantID, "monitor_id", m.MonitorID, "error", err)
					g.metrics.scheduleUnreadable.Inc()
					continue
				}
				for _, h := range sec.Headers {
					m.Headers = append(m.Headers, wire.Header{Name: h.Name, Value: h.Value})
				}
				slices.SortStableFunc(m.Headers, func(a, b wire.Header) int {
					return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Value, b.Value))
				})
				m.Body = sec.Body
				m.Probe = probeOf(sec.Probe, typ == "tcp")
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	// The function returns one row more than the cap, so an oversized schedule is seen without reading it all.
	if len(out) > wire.MaxSchedule {
		return nil, "", errScheduleTooLarge
	}
	slices.SortFunc(out, func(a, b wire.ScheduledMonitor) int {
		return strings.Compare(a.MonitorID.String(), b.MonitorID.String())
	})
	raw, err := json.Marshal(out) // struct field order is fixed, so the bytes are deterministic
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return out, hex.EncodeToString(sum[:]), nil
}

func (g *Handler) scheduleTTL() time.Duration {
	if g.d.ScheduleTTL > 0 {
		return g.d.ScheduleTTL
	}
	return defaultScheduleTTL
}

// scheduleVersion is the version of the Specula's schedule, from a few seconds old cache. A schedule
// over the cap has no version (""), which is cached too: it is the expensive case.
func (g *Handler) scheduleVersion(ctx context.Context, id specula.Identity) (string, error) {
	key, now := id.SpeculaID.String(), g.d.Clock.Now()
	if v, ok := g.versions.get(key, now); ok {
		return v, nil
	}
	_, v, err := g.buildSchedule(ctx, id)
	if err != nil && !errors.Is(err, errScheduleTooLarge) {
		return "", err
	}
	g.versions.put(key, v, now)
	return v, nil
}

func (g *Handler) schedule(w http.ResponseWriter, r *http.Request) {
	id := identity(r)
	monitors, version, err := g.buildSchedule(r.Context(), id)
	if errors.Is(err, errScheduleTooLarge) {
		g.metrics.scheduleRequests.WithLabelValues("413").Inc()
		problem(w, r, http.StatusRequestEntityTooLarge, "schedule-too-large", "Too many monitors at this location")
		return
	}
	if err != nil {
		g.metrics.scheduleRequests.WithLabelValues("error").Inc()
		g.fail(w, r, "schedule", err)
		return
	}
	now := g.d.Clock.Now().UTC()
	g.versions.put(id.SpeculaID.String(), version, now) // free: the executor's next hello or report reuses it
	until := now.Add(g.scheduleTTL())
	h := w.Header()
	h.Set("ETag", `"`+version+`"`)
	h.Set("Cache-Control", "no-store") // the answer carries secrets
	if etagMatches(r.Header.Get("If-None-Match"), version) {
		h.Set(wire.HeaderScheduleValidUntil, until.Format(time.RFC3339))
		w.WriteHeader(http.StatusNotModified)
		g.metrics.scheduleRequests.WithLabelValues("304").Inc()
		return
	}
	g.metrics.scheduleRequests.WithLabelValues("200").Inc()
	write(w, http.StatusOK, wire.Schedule{
		Version: version, ServerTime: now, IssuedAt: now, ValidUntil: until, RefreshAfterSeconds: refreshAfter, Monitors: monitors,
	})
}

// etagMatches reports whether an If-None-Match header names version (weak or strong, or a list).
func etagMatches(header, version string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.Trim(strings.TrimPrefix(strings.TrimSpace(t), "W/"), `"`)
		if t == version {
			return true
		}
	}
	return false
}
