package e2e_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/monitors"
	"github.com/proreta-io/proreta/backend/internal/specula/wire"
	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// report is what an executor would send for a slot of m.
func report(m mon, epoch int, slot int64, ok bool) wire.Report {
	r := wire.Report{MonitorID: m.ID, Epoch: epoch, Slot: slot, OK: ok, LatencyMs: 5}
	if ok {
		r.StatusCode = 200
	} else {
		r.ErrorKind = "timeout"
	}
	return r
}

func mustExec(t *testing.T, e *env, q string) {
	t.Helper()
	pgtest.MustExec(t, e.admin, q)
}

func same(n int, outcome string) []string { return slices.Repeat([]string{outcome}, n) }

// Whatever the executor sends, history gets one row per monitor and slot, the interval stays the
// server's, and a replay of old batches or of an epoch that was replaced changes nothing.
func TestReportsCannotChangeHistory(t *testing.T) {
	e := newEnv(t, "e2eintegrity", 4*time.Hour, []monSpec{
		{name: "api", interval: 60, threshold: 1},
		{name: "db", interval: 60, threshold: 1, tcp: true},
	})
	api, db := e.monitor("api"), e.monitor("db")
	// Two executors with the same token (a pod that was not replaced in time, a second replica):
	// both check every slot and both report it.
	e.start(e.clk, t.TempDir())
	e.start(skewed{e.clk, 3 * time.Second}, t.TempDir())
	e.ready()
	e.run(15*time.Minute, time.Minute)
	e.until("every slot is stored", 10*time.Minute, 5*time.Second, func() bool { return e.allStored(e.t0) })
	e.run(2*time.Minute, time.Minute)
	e.until("every slot is stored", 10*time.Minute, 5*time.Second, func() bool { return e.allStored(e.t0) })
	e.wantCovered(e.t0, e.clk.Now())
	if n := e.count(`SELECT count(*) FROM (SELECT 1 FROM check_results GROUP BY monitor_id, checked_at, location_id HAVING count(*) > 1) d`); n != 0 {
		t.Fatalf("%d slots with several rows", n)
	}

	before := e.dump()
	api0, db0 := api.slots(e.t0, e.clk.Now()), db.slots(e.t0, e.clk.Now())
	t.Run("the same slots again, claiming something else", func(t *testing.T) {
		var batch []wire.Report
		for _, s := range api0[:10] {
			batch = append(batch, report(api, 1, s, false)) // stored as ok
		}
		if got := e.post(batch...); !slices.Equal(got, same(10, "duplicate")) {
			t.Errorf("outcomes %v, want all duplicate", got)
		}
		if got := e.post(report(db, 1, db0[0], false), report(db, 1, db0[len(db0)-1], false)); !slices.Equal(got, same(2, "duplicate")) {
			t.Errorf("outcomes %v, want duplicates", got)
		}
	})
	t.Run("slots off the server's grid are refused: the interval is not the executor's", func(t *testing.T) {
		now := api0[len(api0)-1]
		got := e.post(
			report(api, 1, now+30, true), // half an interval later
			report(api, 1, now+1, true),
			report(api, 1, now-1, true),
			report(api, 1, now+120, true), // on the grid, in the future
			report(api, 1, now+3600, true),
		)
		if !slices.Equal(got, same(5, "rejected")) {
			t.Errorf("outcomes %v, want all rejected", got)
		}
		// Before the epoch began: not this epoch's slot.
		if got := e.post(report(api, 1, api.Anchor-60, true)); got[0] != "superseded" {
			t.Errorf("a slot before the epoch: %v", got)
		}
	})
	t.Run("an old epoch or an epoch that does not exist", func(t *testing.T) {
		if got := e.post(report(api, 2, api0[3], true), report(api, 99, api0[3], true), report(api, 0, api0[3], true)); !slices.Equal(got, []string{"superseded", "superseded", "rejected"}) {
			t.Errorf("outcomes %v", got)
		}
	})
	if after := e.dump(); after != before {
		t.Fatalf("a refused or repeated report changed the history or the schedule:\n--- before\n%s\n--- after\n%s", before, after)
	}

	t.Run("a batch of an epoch that was replaced", func(t *testing.T) {
		// The target of api changes at 00:12: epoch 1 ends there and epoch 2 begins, on its own grid.
		edit := e.t0.Add(12 * time.Minute)
		url := "https://moved.internal.example.com/"
		if _, err := e.mon.Update(e.ctx, e.actor, api.ID, monitors.UpdateInput{HTTP: &monitors.HTTPSettings{URL: &url}}); err != nil {
			t.Fatal(err)
		}
		mustExec(t, e, fmt.Sprintf(`UPDATE monitor_schedule_epochs SET active_until = to_timestamp(%[1]d) WHERE monitor_id = '%[2]s' AND epoch = 1`, edit.Unix(), api.ID))
		mustExec(t, e, fmt.Sprintf(`UPDATE monitor_schedule_epochs SET active_from = to_timestamp(%[1]d), anchor = %[1]d + 11 WHERE monitor_id = '%[2]s' AND epoch = 2`, edit.Unix(), api.ID))
		oldGrid := func() int {
			return e.count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND checked_at >= to_timestamp($2) AND (extract(epoch FROM checked_at)::bigint - $3) % 60 = 0`, api.ID, edit.Unix(), api.Anchor)
		}
		storedBefore := oldGrid() // epoch 1 slots stored before it ended: they stay
		var late, early []wire.Report
		for _, s := range api0 {
			if s >= edit.Unix() {
				late = append(late, report(api, 1, s, true))
			} else {
				early = append(early, report(api, 1, s, true))
			}
		}
		if got := e.post(late...); !slices.Equal(got, same(len(late), "superseded")) {
			t.Errorf("slots of epoch 1 after it ended: %v", got)
		}
		if got := e.post(early...); !slices.Equal(got, same(len(early), "duplicate")) {
			t.Errorf("slots of epoch 1 while it was valid: %v", got)
		}
		if got := e.post(report(api, 2, api0[2], true)); got[0] != "superseded" {
			t.Errorf("a slot of epoch 2 before it began: %v", got)
		}
		// The executor learns of the new schedule and runs epoch 2 on its grid; the old grid stops.
		e.until("the executor runs the new epoch", 10*time.Minute, 5*time.Second, func() bool {
			return e.count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND checked_at >= to_timestamp($2) AND (extract(epoch FROM checked_at)::bigint - $3) % 60 = 0`, api.ID, edit.Unix(), edit.Unix()+11) >= 3
		})
		if n := oldGrid(); n != storedBefore {
			t.Errorf("%d rows of the old grid after the epoch ended, %d before: the old epoch took slots after its end", n, storedBefore)
		}
		if n := e.count(`SELECT count(*) FROM check_results WHERE monitor_id = $1 AND checked_at < to_timestamp($2)`, api.ID, edit.Unix()); n != len(early) {
			t.Errorf("%d rows before the change, want %d", n, len(early))
		}
	})
}
