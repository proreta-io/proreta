package e2e_test

import (
	"testing"
	"time"

	"github.com/proreta-io/proreta/backend/internal/specula/wire"
)

// A short schedule validity and a cut link make the executor miss slots as "scheduleExpired", a loss
// it counts itself and no row or report can show.
const shortTTL = 3 * time.Minute

func (e *env) lossRows() (n int64) {
	e.t.Helper()
	if err := e.admin.QueryRow(e.ctx, `SELECT coalesce(sum(count), 0) FROM specula_slot_losses WHERE reason = 'scheduleExpired'`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) countersStored() bool {
	e.t.Helper()
	var ok bool
	if err := e.admin.QueryRow(e.ctx, `SELECT slot_counters IS NOT NULL FROM specula_agents`).Scan(&ok); err != nil {
		e.t.Fatal(err)
	}
	return ok
}

// outage runs the executor, cuts the link for longer than the schedule's validity and restores it.
func (e *env) outage() {
	e.t.Helper()
	e.start(e.clk, e.t.TempDir())
	e.ready()
	e.run(4*time.Minute, time.Minute)
	e.until("the first slots are stored", 2*time.Minute, 5*time.Second, func() bool { return e.rowCount() > 0 })
	e.link.cut.Store(true)
	e.run(10*time.Minute, time.Minute)
	e.link.cut.Store(false)
	e.run(5*time.Minute, time.Minute)
}

// New executor, new gateway: what the executor missed arrives in the table and in the metric.
func TestSlotLossesReachTheGateway(t *testing.T) {
	e := newEnvTTL(t, "e2esloss", 6*time.Hour, twoMonitors, shortTTL)
	e.outage()
	e.until("the counters arrive", 10*time.Minute, 5*time.Second, func() bool { return e.lossRows() > 0 })
	if !e.countersStored() {
		t.Fatal("the hello carried no counters")
	}
	if got := e.lossRows(); got < 2*3 { // two monitors, at least a few minutes beyond the validity
		t.Fatalf("scheduleExpired = %d, want at least 6", got)
	}
}

// New executor, old gateway (strict hello, schedule/1 only): the executor keeps its schedule mode,
// never sends counters, and does not fall to pull mode.
func TestNewExecutorWithAnOldGatewayStaysInScheduleMode(t *testing.T) {
	e := newEnvTTL(t, "e2esloldgw", 6*time.Hour, twoMonitors, shortTTL)
	e.link.previous.Store(true)
	e.outage()
	e.until("every slot after the outage is stored", 10*time.Minute, 5*time.Second, func() bool { return e.rowCount() >= 12 })
	if n := e.lossRows(); n != 0 || e.countersStored() {
		t.Fatalf("an old gateway recorded %d losses (counters stored: %t)", n, e.countersStored())
	}
	if n := e.link.hit(wire.PathClaim); n != 0 {
		t.Fatalf("the executor fell back to pull mode: %d claims", n)
	}
}

// A gateway rolled back while the executor is sending counters answers 400 to them: the executor
// drops the counters, keeps schedule mode, and its reports are still accepted.
func TestGatewayRolledBackMidRun(t *testing.T) {
	e := newEnvTTL(t, "e2eslrollback", 6*time.Hour, twoMonitors, 0)
	e.start(e.clk, t.TempDir())
	e.ready()
	e.run(5*time.Minute, time.Minute)
	e.until("counters flow", 5*time.Minute, 5*time.Second, func() bool { return e.countersStored() })
	e.link.previous.Store(true)
	before := e.rowCount()
	e.run(6*time.Minute, time.Minute)
	e.until("rows keep arriving", 5*time.Minute, 5*time.Second, func() bool { return e.rowCount() >= before+8 })
	if n := e.link.hit(wire.PathClaim); n != 0 {
		t.Fatalf("the executor fell back to pull mode: %d claims", n)
	}
}
