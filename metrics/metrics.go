// Package metrics is the Prometheus instrumentation shared by pdbq and
// pdbr: HTTP request counters, durations and in-flight requests by route
// class, database operation durations and errors by SQLSTATE class (as a
// [pgexec.Observer]), pgxpool statistics, schema rebuilds, statement
// compile time, and counters products register for their own events.
//
// Every [Metrics] registers on its own registry unless the caller passes
// one, so several products embedded in one process never collide on metric
// names. A nil *Metrics is valid and records nothing, so products can keep
// one code path whether metrics are enabled or not.
package metrics

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Options configures [New].
type Options struct {
	// Namespace prefixes every metric name, e.g. "pdbq" gives
	// pdbq_http_requests_total. Required.
	Namespace string
	// Registerer receives every metric. nil creates a private registry
	// that also carries the Go runtime and process collectors.
	Registerer prometheus.Registerer
	// Gatherer is what [Metrics.Handler] serves. It defaults to Registerer
	// when that is also a Gatherer (as *prometheus.Registry is), and to
	// the private registry when Registerer is nil.
	Gatherer prometheus.Gatherer
	// ConstLabels are added to every metric registered through this
	// Metrics, for telling apart several instances that share a
	// Registerer. Prometheus requires every instance on one Registerer to
	// use the same label names, with different values.
	ConstLabels prometheus.Labels
	// DurationBuckets are the histogram buckets, in seconds, of every
	// duration metric. nil uses prometheus.DefBuckets.
	DurationBuckets []float64
}

// Metrics holds the instruments of one application instance. Create it
// with [New]; the zero value is not usable, but a nil *Metrics is: every
// method on it is a no-op and [Metrics.Middleware] returns its handler
// unchanged.
type Metrics struct {
	namespace string
	reg       prometheus.Registerer
	gatherer  prometheus.Gatherer
	buckets   []float64

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight prometheus.Gauge

	dbDuration *prometheus.HistogramVec
	dbErrors   *prometheus.CounterVec
	dbRetries  *prometheus.CounterVec

	schemaRebuilds    *prometheus.CounterVec
	schemaLastRebuild prometheus.Gauge
	compileDuration   prometheus.Histogram
}

// New registers the built-in instruments and returns the Metrics. It fails
// when Namespace is empty, when a metric is already registered on
// o.Registerer (for example by a second Metrics with the same Namespace and
// ConstLabels), or when no Gatherer can be derived for a custom Registerer.
func New(o Options) (*Metrics, error) {
	if o.Namespace == "" {
		return nil, errors.New("metrics: Namespace is required")
	}
	reg, gatherer := o.Registerer, o.Gatherer
	if reg == nil {
		r := prometheus.NewRegistry()
		if err := r.Register(collectors.NewGoCollector()); err != nil {
			return nil, fmt.Errorf("metrics: %w", err)
		}
		if err := r.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
			return nil, fmt.Errorf("metrics: %w", err)
		}
		reg = r
		if gatherer == nil {
			gatherer = r
		}
	}
	if gatherer == nil {
		g, ok := reg.(prometheus.Gatherer)
		if !ok {
			return nil, errors.New("metrics: Gatherer is required when Registerer is not a prometheus.Gatherer")
		}
		gatherer = g
	}
	if len(o.ConstLabels) > 0 {
		reg = prometheus.WrapRegistererWith(o.ConstLabels, reg)
	}
	buckets := o.DurationBuckets
	if buckets == nil {
		buckets = prometheus.DefBuckets
	}
	m := &Metrics{namespace: o.Namespace, reg: reg, gatherer: gatherer, buckets: buckets}
	httpLabels := []string{"route", "method", "status_class"}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: m.namespace, Subsystem: "http", Name: "requests_total",
		Help: "HTTP requests served, by route class, method and status class (2xx, 4xx, ...).",
	}, httpLabels)
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: m.namespace, Subsystem: "http", Name: "request_duration_seconds",
		Help:    "HTTP request duration until the handler returned, by route class, method and status class.",
		Buckets: buckets,
	}, httpLabels)
	m.httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: m.namespace, Subsystem: "http", Name: "requests_in_flight",
		Help: "HTTP requests currently being served.",
	})
	m.dbDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: m.namespace, Subsystem: "db", Name: "duration_seconds",
		Help:    "Database operation duration by operation: run_tx and query_tx are single transaction attempts, other operations single statements.",
		Buckets: buckets,
	}, []string{"op"})
	m.dbErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: m.namespace, Subsystem: "db", Name: "errors_total",
		Help: "Failed database operations by operation and SQLSTATE class (two characters, e.g. 23), or canceled, timeout, other for errors without a SQLSTATE.",
	}, []string{"op", "sqlstate_class"})
	m.dbRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: m.namespace, Subsystem: "db", Name: "retries_total",
		Help: "Transaction attempts re-run after a serialization failure or deadlock, by operation.",
	}, []string{"op"})
	m.schemaRebuilds = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: m.namespace, Subsystem: "schema", Name: "rebuilds_total",
		Help: "API rebuilds after a schema change (watch mode), by result (success, failure).",
	}, []string{"result"})
	m.schemaLastRebuild = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: m.namespace, Subsystem: "schema", Name: "last_rebuild_timestamp_seconds",
		Help: "Unix time of the last successful API rebuild after a schema change; 0 before the first.",
	})
	m.compileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: m.namespace, Name: "compile_duration_seconds",
		Help:    "Time spent compiling a request into SQL.",
		Buckets: buckets,
	})
	for _, c := range []prometheus.Collector{
		m.httpRequests, m.httpDuration, m.httpInFlight,
		m.dbDuration, m.dbErrors, m.dbRetries,
		m.schemaRebuilds, m.schemaLastRebuild, m.compileDuration,
	} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("metrics: %w", err)
		}
	}
	m.schemaRebuilds.WithLabelValues("success")
	m.schemaRebuilds.WithLabelValues("failure")
	return m, nil
}

// Handler serves the gathered metrics in the Prometheus exposition format.
// On a nil Metrics it answers 404.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}

// Registerer returns the registerer the built-in instruments were added to
// (wrapped with Options.ConstLabels), for collectors this package does not
// provide. nil on a nil Metrics.
func (m *Metrics) Registerer() prometheus.Registerer {
	if m == nil {
		return nil
	}
	return m.reg
}

// SchemaRebuilt records an API rebuild after a schema change: err == nil
// counts a success and sets the last-rebuild timestamp, anything else
// counts a failure.
func (m *Metrics) SchemaRebuilt(err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.schemaRebuilds.WithLabelValues("failure").Inc()
		return
	}
	m.schemaRebuilds.WithLabelValues("success").Inc()
	m.schemaLastRebuild.SetToCurrentTime()
}

// ObserveCompile records the time spent compiling one request into SQL.
func (m *Metrics) ObserveCompile(d time.Duration) {
	if m == nil {
		return
	}
	m.compileDuration.Observe(d.Seconds())
}

// Counter is a product-defined counter with optional labels, created with
// [Metrics.NewCounter]. A nil *Counter records nothing.
type Counter struct {
	vec *prometheus.CounterVec
}

// NewCounter registers the counter Namespace_name with the given help text
// and label names. By convention name ends in _total. A counter without
// labels is exported at zero from the start; labelled series appear on
// first use or [Counter.Init]. On a nil Metrics it returns a nil *Counter
// and no error.
func (m *Metrics) NewCounter(name, help string, labels ...string) (*Counter, error) {
	if m == nil {
		return nil, nil
	}
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: m.namespace, Name: name, Help: help}, labels)
	if err := m.reg.Register(vec); err != nil {
		return nil, fmt.Errorf("metrics: %s: %w", name, err)
	}
	if len(labels) == 0 {
		vec.WithLabelValues()
	}
	return &Counter{vec: vec}, nil
}

// Inc adds one to the series with the given label values, which must match
// the label names in number and order.
func (c *Counter) Inc(labelValues ...string) {
	if c == nil {
		return
	}
	c.vec.WithLabelValues(labelValues...).Inc()
}

// Add adds v (which must not be negative) to the series with the given
// label values.
func (c *Counter) Add(v float64, labelValues ...string) {
	if c == nil {
		return
	}
	c.vec.WithLabelValues(labelValues...).Add(v)
}

// Init creates the series with the given label values at zero, so it is
// exported before its first event (for example both hit and miss of a
// cache counter).
func (c *Counter) Init(labelValues ...string) {
	if c == nil {
		return
	}
	c.vec.WithLabelValues(labelValues...)
}
