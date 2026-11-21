package gateway

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	unauthorized prometheus.Counter
	limited      prometheus.Counter
	jobs         prometheus.Counter
	results      *prometheus.CounterVec
	tokenLimited *prometheus.CounterVec

	reports            *prometheus.CounterVec
	reportAge          prometheus.Histogram
	lateReports        prometheus.Counter
	scheduleRequests   *prometheus.CounterVec
	scheduleBuild      prometheus.Histogram
	scheduleUnreadable prometheus.Counter
	duplicateInstance  prometheus.Counter
	slotLosses         *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		unauthorized: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proreta_specula_unauthorized_total", Help: "Specula requests with a missing, unknown or revoked token.",
		}),
		limited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proreta_specula_rate_limited_total", Help: "Specula requests refused with 429 because the client address used up its token-check budget.",
		}),
		jobs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proreta_specula_jobs_claimed_total", Help: "Jobs handed to Specula executors.",
		}),
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proreta_specula_results_total", Help: "Results reported by Specula executors, by outcome.",
		}, []string{"outcome"}),
		tokenLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proreta_specula_token_limited_total", Help: "Specula requests refused with 429 because the token used up its budget for the endpoint.",
		}, []string{"endpoint"}),
		reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proreta_specula_reports_total", Help: "Slot-addressed results reported by Specula executors, by outcome.",
		}, []string{"outcome"}),
		reportAge: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "proreta_specula_report_age_seconds", Help: "Age of a recorded report when it arrived (received time minus slot); the depth of a replay.",
			Buckets: []float64{5, 15, 30, 60, 300, 900, 3600, 6 * 3600, 24 * 3600},
		}),
		lateReports: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proreta_specula_late_reports_total", Help: "Recorded reports older than the late-alert threshold (PRORETA_SPECULA_LATE_ALERT_AFTER) when they arrived; their alerts say they are late.",
		}),
		scheduleRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proreta_specula_schedule_requests_total", Help: "GET /schedule requests that passed the token budget, by result.",
		}, []string{"result"}),
		scheduleBuild: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "proreta_specula_schedule_build_seconds", Help: "Time to read and decrypt the schedule of a Specula (GET /schedule, and hello or reports when the version is not cached).",
			Buckets: []float64{.005, .02, .05, .1, .25, .5, 1, 2.5, 5},
		}),
		scheduleUnreadable: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proreta_specula_schedule_unreadable_monitors_total", Help: "Monitors left out of a built schedule because their sealed request cannot be opened; counted at every build, so any increase means a monitor is not being checked.",
		}),
		duplicateInstance: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proreta_specula_duplicate_instance_total", Help: "Hellos whose instance id differs from the stored one, sent less than 3 minutes after the stored one's hello: two executors share a token.",
		}),
		slotLosses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proreta_specula_slot_losses_total", Help: "Slots the executors reported as never checked (kind missed) or results they gave up on (kind dropped), by reason; the growth between two hellos of the same process.",
		}, []string{"kind", "reason"}),
	}
	if reg != nil {
		reg.MustRegister(m.unauthorized, m.limited, m.jobs, m.results, m.tokenLimited,
			m.reports, m.reportAge, m.lateReports, m.scheduleRequests, m.scheduleBuild, m.scheduleUnreadable, m.duplicateInstance, m.slotLosses)
	}
	return m
}

// RegisterMetrics registers the gateway's collectors without starting a gateway, so a test can
// check them against the documentation; New registers them itself.
func RegisterMetrics(reg prometheus.Registerer) { newMetrics(reg) }
