// Package metrics defines CloudDeploy's Prometheus metrics.
//
// Metrics live on an explicit Registry (no global state) and every label
// has a small, bounded set of values: route PATTERNS rather than raw paths,
// environment names, statuses. An unbounded label (a deployment ID, a raw
// URL with IDs in it) creates a new time series per value and eventually
// takes Prometheus down: the classic label-cardinality mistake.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTP holds the API's request metrics.
type HTTP struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// Deployments holds deployment lifecycle metrics (recorded by the worker).
type Deployments struct {
	total     *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	failures  *prometheus.CounterVec
	stepTime  *prometheus.HistogramVec
	inFlight  prometheus.Gauge
	rollbacks *prometheus.CounterVec
}

// RateLimit holds rate limiter decision metrics.
type RateLimit struct {
	decisions *prometheus.CounterVec
}

// NewRegistry returns a registry with Go runtime and process collectors.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Handler serves the registry in the Prometheus exposition format.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg})
}

// Latency buckets (seconds) for API requests: mostly sub-100ms, with room
// for slow database calls and SSE handshakes.
var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// NewHTTP registers HTTP metrics on reg.
func NewHTTP(reg prometheus.Registerer) *HTTP {
	m := &HTTP{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests by method, route pattern and status code.",
		}, []string{"method", "route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency by method and route pattern.", Buckets: httpBuckets,
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight", Help: "HTTP requests currently being served.",
		}),
	}
	reg.MustRegister(m.requests, m.duration, m.inFlight)
	return m
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware records request metrics. It must wrap the ServeMux DIRECTLY:
// the mux sets r.Pattern (e.g. "GET /api/v1/deployments/{id}") on the
// request it receives, and that pattern, not r.URL.Path, is the route label.
// Long-lived streams (SSE) are counted but excluded from the latency
// histogram, where minutes-long "requests" would distort every percentile.
func (m *HTTP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.inFlight.Inc()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			m.inFlight.Dec()
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			if sw.code == 0 {
				sw.code = http.StatusOK
			}
			m.requests.WithLabelValues(r.Method, route, strconv.Itoa(sw.code)).Inc()
			if r.URL.Query().Get("follow") != "true" {
				m.duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// Deployment durations range from sub-second (image reused) to many minutes.
var deployBuckets = []float64{1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200}

// NewDeployments registers deployment metrics on reg.
func NewDeployments(reg prometheus.Registerer) *Deployments {
	m := &Deployments{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "deployment_total", Help: "Finished deployments by environment and outcome (succeeded, failed, cancelled).",
		}, []string{"environment", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "deployment_duration_seconds", Help: "Time from start of build to the final state.", Buckets: deployBuckets,
		}, []string{"environment", "outcome"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "deployment_failures_total", Help: "Failed deployments by environment and the stage that failed (build, deploy, lease_lost, interrupted).",
		}, []string{"environment", "stage"}),
		stepTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "deployment_step_duration_seconds", Help: "Duration of each pipeline step.", Buckets: deployBuckets,
		}, []string{"step", "result"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "worker_deployments_in_flight", Help: "Deployments this worker is processing now.",
		}),
		rollbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "deployment_auto_rollbacks_total", Help: "Auto-rollback decisions after failed rollouts (queued or skipped).",
		}, []string{"environment", "result"}),
	}
	reg.MustRegister(m.total, m.duration, m.failures, m.stepTime, m.inFlight, m.rollbacks)
	// Initialize every known series at 0. A counter that first appears at 1
	// has no earlier sample, so increase()/rate() in dashboards and alerts
	// miss that first event (found when a fresh dashboard showed 0% success
	// after a successful deployment).
	for _, env := range []string{"development", "staging", "production"} {
		for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
			m.total.WithLabelValues(env, outcome)
		}
		for _, stage := range []string{"build", "deploy", "panic", "lease_lost", "interrupted"} {
			m.failures.WithLabelValues(env, stage)
		}
		for _, result := range []string{"queued", "skipped"} {
			m.rollbacks.WithLabelValues(env, result)
		}
	}
	return m
}

// TotalCollector exposes deployment_total (for tests).
func (m *Deployments) TotalCollector() prometheus.Collector { return m.total }

// Finished records a deployment reaching a terminal state.
func (m *Deployments) Finished(env, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.total.WithLabelValues(env, outcome).Inc()
	m.duration.WithLabelValues(env, outcome).Observe(d.Seconds())
}

// Failed records a failure and the stage it happened in.
func (m *Deployments) Failed(env, stage string) {
	if m == nil {
		return
	}
	m.failures.WithLabelValues(env, stage).Inc()
}

// Step records one pipeline step (build, deploy).
func (m *Deployments) Step(step string, err error, d time.Duration) {
	if m == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.stepTime.WithLabelValues(step, result).Observe(d.Seconds())
}

// Started / Done track in-flight work.
func (m *Deployments) Started() {
	if m != nil {
		m.inFlight.Inc()
	}
}

func (m *Deployments) Done() {
	if m != nil {
		m.inFlight.Dec()
	}
}

// AutoRollback records an auto-rollback decision ("queued" or "skipped").
func (m *Deployments) AutoRollback(env, result string) {
	if m != nil {
		m.rollbacks.WithLabelValues(env, result).Inc()
	}
}

// NewRateLimit registers rate limiter metrics on reg.
func NewRateLimit(reg prometheus.Registerer) *RateLimit {
	m := &RateLimit{decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_decisions_total",
		Help: "Rate limit checks by rule and decision (allowed, denied, fail_open). fail_open means Redis was unavailable and the request was allowed unchecked.",
	}, []string{"rule", "decision"})}
	reg.MustRegister(m.decisions)
	return m
}

// Decision records one rate limit check.
func (m *RateLimit) Decision(rule, decision string) {
	if m != nil {
		m.decisions.WithLabelValues(rule, decision).Inc()
	}
}

// QueueQuerier returns current queue state at scrape time.
type QueueQuerier func() (queued, active int, oldestQueuedAge time.Duration, err error)

// queueCollector computes queue gauges when Prometheus scrapes, with one
// query, instead of a background loop polling the database.
type queueCollector struct {
	query  QueueQuerier
	queued *prometheus.Desc
	active *prometheus.Desc
	oldest *prometheus.Desc
	up     *prometheus.Desc
}

// NewQueueCollector registers scrape-time queue metrics.
func NewQueueCollector(reg prometheus.Registerer, q QueueQuerier) {
	reg.MustRegister(&queueCollector{
		query:  q,
		queued: prometheus.NewDesc("deployment_queue_depth", "Deployments waiting in QUEUED.", nil, nil),
		active: prometheus.NewDesc("deployments_active", "Deployments in BUILDING or DEPLOYING.", nil, nil),
		oldest: prometheus.NewDesc("deployment_queue_oldest_age_seconds", "Age of the oldest QUEUED deployment (0 if none): grows when workers are down or saturated.", nil, nil),
		up:     prometheus.NewDesc("deployment_queue_scrape_ok", "1 if the queue query succeeded during this scrape.", nil, nil),
	})
}

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queued
	ch <- c.active
	ch <- c.oldest
	ch <- c.up
}

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	queued, active, oldest, err := c.query()
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.queued, prometheus.GaugeValue, float64(queued))
	ch <- prometheus.MustNewConstMetric(c.active, prometheus.GaugeValue, float64(active))
	ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, oldest.Seconds())
}
