package gateway

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	unauthorized prometheus.Counter
	limited      prometheus.Counter
	jobs         prometheus.Counter
	results      *prometheus.CounterVec
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
	}
	if reg != nil {
		reg.MustRegister(m.unauthorized, m.limited, m.jobs, m.results)
	}
	return m
}
