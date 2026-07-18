// Package metrics provides a per-service Prometheus registry and HTTP
// instrumentation.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns a private registry so tests and services never collide
// on the global default registry.
type Metrics struct {
	registry *prometheus.Registry
	reqTotal *prometheus.CounterVec
	reqDur   *prometheus.HistogramVec
}

// New builds a registry with Go/process collectors and HTTP request
// metrics. The service name arrives as a label from scrape config, not
// the metric name, so dashboards can query one metric across services.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: reg,
		reqTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "timothy",
			Name:      "http_requests_total",
			Help:      "HTTP requests by route and status.",
		}, []string{"method", "route", "status"}),
		reqDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "timothy",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration by route.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.reqTotal, m.reqDur)
	return m
}

// NewCounter registers a service-specific counter on this registry.
func (m *Metrics) NewCounter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Namespace: "timothy", Name: name, Help: help})
	m.registry.MustRegister(c)
	return c
}

// NewGaugeVec registers a service-specific labeled gauge on this
// registry.
func (m *Metrics) NewGaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "timothy", Name: name, Help: help}, labels)
	m.registry.MustRegister(g)
	return g
}

// Handler serves the registry in Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// statusWriter records the response status and keeps http.Flusher
// visible for streaming handlers.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Instrument wraps next, counting requests and observing duration
// under the given route label. Routes are registration patterns, never
// raw URL paths, to keep label cardinality bounded.
func (m *Metrics) Instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		m.reqTotal.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		m.reqDur.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
