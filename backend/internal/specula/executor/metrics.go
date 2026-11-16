package executor

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics holds the one metric that is observed as it happens; the rest is read at scrape time.
type metrics struct {
	lateness prometheus.Histogram
}

func newMetrics() metrics {
	return metrics{lateness: prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "specula_slot_lateness_seconds",
		Help:    "How late a check started after its slot, on the corrected clock.",
		Buckets: []float64{0.01, 0.05, 0.25, 1, 5, 15, 30, 60},
	})}
}

// Register exposes the executor's metrics on reg (its admin port). They carry no per-monitor or
// per-token label.
func (e *Executor) Register(reg prometheus.Registerer) error {
	return reg.Register(&collector{e: e})
}

var (
	descResults   = prometheus.NewDesc("specula_buffer_results", "Results waiting to be reported.", nil, nil)
	descBytes     = prometheus.NewDesc("specula_buffer_bytes", "Bytes the buffer holds on disk.", nil, nil)
	descOldest    = prometheus.NewDesc("specula_buffer_oldest_age_seconds", "Age of the oldest result waiting to be reported; 0 when none waits.", nil, nil)
	descDegraded  = prometheus.NewDesc("specula_buffer_degraded", "1 when results are buffered in memory only.", nil, nil)
	descDropped   = prometheus.NewDesc("specula_results_dropped_total", "Results given up on: buffer limits (size, age, memory), an unreadable segment, or refused by the gateway.", []string{"reason"}, nil)
	descCorrupt   = prometheus.NewDesc("specula_buffer_corrupt_records_total", "Damaged buffer records skipped or segments set aside.", nil, nil)
	descSchedAge  = prometheus.NewDesc("specula_schedule_age_seconds", "Time since the schedule in use was issued; absent while there is none.", nil, nil)
	descExpired   = prometheus.NewDesc("specula_schedule_expired_monitors", "Monitors that are not checked because the schedule expired.", nil, nil)
	descMissed    = prometheus.NewDesc("specula_missed_slots_total", "Slots without a check.", []string{"reason"}, nil)
	descInflight  = prometheus.NewDesc("specula_replay_inflight", "Results in the batch being reported now; 0 when idle.", nil, nil)
	descClockOffs = prometheus.NewDesc("specula_clock_offset_seconds", "Proreta's time minus the local time at the last hello.", nil, nil)
)

// dropReasons are always exported, so rate() works before the first drop.
var dropReasons = []string{dropSize, dropAge, dropMemory, dropBad, dropRefused}

type collector struct{ e *Executor }

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descResults, descBytes, descOldest, descDegraded, descDropped, descCorrupt, descSchedAge, descExpired, descMissed, descInflight, descClockOffs} {
		ch <- d
	}
	c.e.met.lateness.Describe(ch)
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	e := c.e
	now := e.now()
	g := func(d *prometheus.Desc, v float64, lv ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, lv...)
	}
	ctr := func(d *prometheus.Desc, v int64, lv ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, float64(v), lv...)
	}
	var st BufferStats
	if e.buf != nil { // before Run opens the buffer
		st = e.buf.Stats()
		age := 0.0
		if slot := e.buf.OldestSlot(); slot > 0 {
			age = max(now.Sub(time.Unix(slot, 0)).Seconds(), 0)
		}
		g(descOldest, age)
	}
	g(descResults, float64(st.Depth))
	g(descBytes, float64(st.Bytes))
	g(descDegraded, b2f(st.Degraded))
	rs := e.rep.Stats()
	for _, r := range dropReasons {
		n := st.Drops[r]
		if r == dropRefused {
			n = rs.Dropped
		}
		ctr(descDropped, n, r)
	}
	ctr(descCorrupt, st.Corrupt+st.Quarantined)
	if issued := e.sched.issuedAt(); issued != nil {
		g(descSchedAge, max(now.Sub(*issued).Seconds(), 0))
		expired := 0
		if v := e.sched.validity(); !now.Before(v) {
			expired = e.sched.running()
		}
		g(descExpired, float64(expired))
	}
	ctr(descMissed, e.sched.n.missedBusy.Load(), "busy")
	ctr(descMissed, e.sched.n.missedLate.Load(), "late")
	ctr(descMissed, e.sched.n.missedExpired.Load(), "expired")
	g(descInflight, float64(e.rep.inflight.Load()))
	g(descClockOffs, e.clk.Offset().Seconds())
	e.met.lateness.Collect(ch)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
