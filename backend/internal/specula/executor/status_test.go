package executor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestInstanceIDSurvivesARestartOnlyWithAStateDir(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(t0)
	h := newHarness(t, newGateway(clk, monitor(t0.Unix(), 60)), dir)
	h.waitRunning(1)
	first := h.e.instanceID
	h.restart(dir)
	h.waitRunning(1)
	if first == "" || h.e.instanceID != first {
		t.Fatalf("ids %q then %q", first, h.e.instanceID)
	}
	h.restart("")
	h.waitRunning(1)
	if h.e.instanceID == first {
		t.Fatal("without a state directory the id cannot survive")
	}
}

func TestStartupLogSaysScheduleAndBufferState(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	h := newHarness(t, gw, dir)
	h.waitRunning(1)
	clk.Advance(2*time.Minute + time.Second)
	gw.down.Store(true)
	var out bytes.Buffer
	h.log = slog.New(slog.NewTextHandler(&out, nil))
	h.restart(dir)
	h.waitRunning(1)
	_ = h.stop() // the executor logs from its own goroutines
	line := out.String()
	for _, want := range []string{"executor state at start", "instance_id=" + h.e.instanceID, "schedule_monitors=1", "schedule_issued_at", "buffer_degraded=false"} {
		if !strings.Contains(line, want) {
			t.Errorf("startup log lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, h.gw.token) {
		t.Error("the log holds the token")
	}
}

func TestMetricsShowBufferAndScheduleState(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	h := newHarness(t, gw, t.TempDir())
	h.waitRunning(1)
	gw.down.Store(true) // reports cannot leave: the results stay buffered
	clk.Advance(3*time.Minute + time.Second)
	reg := prometheus.NewPedanticRegistry()
	if err := h.e.Register(reg); err != nil {
		t.Fatal(err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	val := map[string]float64{}
	for _, mf := range mfs {
		if len(mf.Metric) == 1 && mf.Metric[0].Gauge != nil {
			val[mf.GetName()] = mf.Metric[0].Gauge.GetValue()
		}
	}
	if val["specula_buffer_results"] < 1 || val["specula_buffer_bytes"] <= 0 || val["specula_buffer_oldest_age_seconds"] <= 0 || val["specula_schedule_age_seconds"] <= 0 {
		t.Fatalf("gauges %v", val)
	}
	if val["specula_schedule_loaded"] != 1 {
		t.Fatalf("schedule_loaded %v", val["specula_schedule_loaded"])
	}
	if val["specula_buffer_degraded"] != 0 || val["specula_schedule_expired_monitors"] != 0 {
		t.Fatalf("gauges %v", val)
	}
	series := map[string]int{}
	for _, mf := range mfs {
		series[mf.GetName()] = len(mf.Metric)
	}
	if series["specula_slot_lateness_seconds"] != 1 || series["specula_results_dropped_total"] != len(dropReasons) || series["specula_missed_slots_total"] != 3 {
		t.Fatalf("series %v", series)
	}
}

func TestOldestAgeIsCachedButKeepsGrowing(t *testing.T) {
	clk := newFakeClock(t0)
	gw := newGateway(clk, monitor(t0.Unix(), 60))
	h := newHarness(t, gw, t.TempDir())
	h.waitRunning(1)
	gw.down.Store(true)
	clk.Advance(3*time.Minute + time.Second)
	c := &collector{e: h.e}
	depth := h.e.buf.Stats().Depth
	slot := c.oldest(clk.Now(), depth)
	if slot == 0 {
		t.Fatal("no oldest slot with results waiting")
	}
	clk.Advance(5 * time.Second)
	if c.oldestAt.Equal(clk.Now()) || c.oldest(clk.Now(), depth) != slot || !c.oldestAt.Before(clk.Now()) {
		t.Fatal("the slot was read again within the TTL")
	}
	if c.oldest(clk.Now(), 0) != 0 {
		t.Fatal("an empty buffer has no oldest slot")
	}
}
