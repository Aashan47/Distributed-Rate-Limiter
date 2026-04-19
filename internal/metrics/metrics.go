// Package metrics exposes Prometheus instrumentation for the rate limiter.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	Registry        *prometheus.Registry
	Requests        *prometheus.CounterVec
	DecisionLatency *prometheus.HistogramVec
	RedisErrors     prometheus.Counter
	ActiveRules     prometheus.Gauge
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimiter_requests_total",
			Help: "Total rate-limit decisions, labelled by rule and outcome.",
		}, []string{"rule", "decision"}),
		DecisionLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ratelimiter_decision_latency_seconds",
			Help:    "End-to-end decision latency, including the Redis round trip.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 14),
		}, []string{"rule", "decision"}),
		RedisErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ratelimiter_redis_errors_total",
			Help: "Total errors returned from the Redis Lua script.",
		}),
		ActiveRules: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ratelimiter_active_rules",
			Help: "Number of active rate-limit rules loaded.",
		}),
	}
	reg.MustRegister(
		m.Requests, m.DecisionLatency, m.RedisErrors, m.ActiveRules,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

func DecisionLabel(allowed bool) string {
	if allowed {
		return "allowed"
	}
	return "denied"
}
