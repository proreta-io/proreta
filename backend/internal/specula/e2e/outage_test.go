package e2e_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/db"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/statuspages"
	"github.com/proreta-io/proreta/backend/internal/statuspages/statuspagesdb"
)

var healthy = []monSpec{
	{name: "api", interval: 60, threshold: 1},
	{name: "web", interval: 60, threshold: 1},
	{name: "db", interval: 60, threshold: 1, tcp: true},
}

// ready waits until the executor has fetched its schedule and armed the timers of its checks, so
// that the first slot of the simulation is run.
func (e *env) ready() {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for e.link.hit(wire.PathSchedule) == 0 {
		if time.Now().After(deadline) {
			e.t.Fatalf("the executor never fetched its schedule:\n%s", e.logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.clk.quiet(100 * time.Millisecond)
}

// allStored reports whether every slot up to now has a row.
func (e *env) allStored(from time.Time) bool {
	got := e.stored()
	for _, m := range e.mons {
		if len(got[m.ID]) != len(m.slots(from, e.clk.Now())) {
			return false
		}
	}
	return true
}

// A link outage leaves no gap: after it every slot of every monitor has one row at the slot, in the
// raw rows, in the rollups (after the dirty hours are drained) and on the status page day.
// The outages are 8 minutes and 2 hours of simulated time; the 2 hour one takes about 25 s of wall time.
func TestOutageLeavesNoGap(t *testing.T) {
	for _, c := range []struct {
		name                string
		before, down, after time.Duration
		back                time.Duration
	}{
		// The first starts at a quarter to, so that its first hour closes soon.
		{"8 minutes", 5 * time.Minute, 8 * time.Minute, 20 * time.Minute, 6*time.Hour + 15*time.Minute},
		{"2 hours", 30 * time.Minute, 2 * time.Hour, 30 * time.Minute, 8 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			if testing.Short() && c.down > time.Hour {
				t.Skip("simulated hours")
			}
			e := newEnv(t, "e2eoutage", c.back, healthy)
			e.start(e.clk, t.TempDir())
			e.ready()

			e.run(c.before, time.Minute)
			e.until("the first slots are stored", 2*time.Minute, 5*time.Second, func() bool { return e.rowCount() > 0 })

			e.link.cut.Store(true)
			e.run(c.down, time.Minute) // the regular rollup keeps running while the link is down
			before := e.rowCount()
			e.link.cut.Store(false)
			e.run(c.after, time.Minute)
			e.until("every slot is stored", time.Hour, 5*time.Second, func() bool { return e.allStored(e.t0) })
			end := e.clk.Now()
			if end.After(e.pgNow().Truncate(time.Hour)) {
				t.Fatalf("the simulation ended at %v, in the database's open hour", end)
			}
			t.Logf("rows before the link came back: %d, after: %d", before, e.rowCount())
			e.wantCovered(e.t0, end)

			// The regular run has seen the late rows (they are within its lookback); the drain has
			// nothing to do, and the rollups count every slot.
			e.catchUp(end)
			e.drain()
			if bad := e.rolledUpMismatches(e.t0, end); bad != 0 {
				t.Errorf("%d hourly rollups do not count their slots", bad)
			}
			e.wantStatusPageDays(end)
		})
	}
}

// wantStatusPageDays reads the daily bars the status page builds from the rollups: each day counts
// the slots of its closed hours and shows 100 % for monitors whose target never failed.
func (e *env) wantStatusPageDays(end time.Time) {
	e.t.Helper()
	ids := make([]uuid.UUID, len(e.mons))
	for i, m := range e.mons {
		ids[i] = m.ID
	}
	var rows []statuspagesdb.DailyCountsRow
	err := db.WithTenantTx(e.ctx, e.pool, e.actor.TenantID, func(tx pgx.Tx) error {
		var err error
		rows, err = statuspagesdb.New(tx).DailyCounts(e.ctx, statuspagesdb.DailyCountsParams{TenantID: e.actor.TenantID, MonitorIds: ids, Since: e.t0.Truncate(24 * time.Hour)})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	counts := map[uuid.UUID]map[string]statuspages.Counts{}
	for _, r := range rows {
		if counts[r.MonitorID] == nil {
			counts[r.MonitorID] = map[string]statuspages.Counts{}
		}
		counts[r.MonitorID][r.Day] = statuspages.Counts{Total: r.Total, OK: r.Ok}
	}
	for _, m := range e.mons {
		want := map[string]int64{}
		for _, s := range m.slots(e.t0, end.Truncate(time.Hour).Add(-time.Second)) {
			want[time.Unix(s, 0).UTC().Format(time.DateOnly)]++
		}
		for day, n := range want {
			if got := counts[m.ID][day]; got.Total != n || got.OK != n {
				e.t.Errorf("%s on %s: the status page counts %d of %d checks, want %d, all ok", m.Name, day, got.OK, got.Total, n)
			}
		}
		bars, uptime := statuspages.CheckDays(statuspages.HistoryWindow(time.Now().UTC()), counts[m.ID])
		if uptime == nil || *uptime != 100 {
			e.t.Errorf("%s: uptime %v, want 100", m.Name, uptime)
		}
		for _, b := range bars {
			if _, ok := want[b.Date]; ok && b.Status != statuspages.Operational {
				e.t.Errorf("%s on %s: the bar is %q", m.Name, b.Date, b.Status)
			}
		}
	}
}

// A replay that arrives just before a full hour carries rows of the oldest hour the regular rollup
// still revisits; the run right after the hour looks one hour less far back, so that hour must have
// been marked for the drain. The month then passes the retention completeness check only after the
// drain.
// Slow: about 30 s of wall time (6 simulated hours).
func TestLateRowsJustBeforeTheHourChangeAndRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("six simulated hours")
	}
	e := newEnv(t, "e2ehourchange", 8*time.Hour, []monSpec{{name: "api", interval: 60, threshold: 1}})
	e.start(e.clk, t.TempDir())
	e.ready()
	e.run(10*time.Minute, time.Minute)
	e.link.cut.Store(true)
	e.run(3*time.Hour+48*time.Minute, time.Minute) // the link is back at 03:58
	e.link.cut.Store(false)
	e.run(5*time.Minute, time.Minute)
	e.until("every slot is stored", time.Hour, 5*time.Second, func() bool { return e.allStored(e.t0) })
	e.run(2*time.Hour, 5*time.Minute)
	end := e.clk.Now()
	e.wantCovered(e.t0, end)

	// The hour the simulation ended in is rolled up by the next regular run; it holds on-time rows only.
	if _, err := e.worker.Exec(e.ctx, `SELECT compute_rollups($1, $2)`, end.Truncate(time.Hour), end.Truncate(time.Hour).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM rollup_dirty_hours`); n == 0 {
		t.Fatal("no hour was marked for the drain")
	}
	if bad := e.rolledUpMismatches(e.t0, end); bad == 0 {
		t.Fatal("the late rows changed no rollup before the drain; the test proves nothing")
	}
	month := e.moveToOldMonth()
	if e.detach(month) {
		t.Fatal("the month was detached with hours waiting for the drain")
	}
	e.drain()
	if bad := e.rolledUpMismatches(e.t0, end); bad != 0 {
		t.Errorf("%d hourly rollups do not count their slots after the drain", bad)
	}
	if !e.detach(month) {
		t.Error("the month still fails the completeness check after the drain")
	}
}
