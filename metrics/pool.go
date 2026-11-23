package metrics

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolStater reports connection pool statistics; satisfied by
// *pgxpool.Pool.
type PoolStater interface {
	Stat() *pgxpool.Stat
}

// RegisterPool exports p's statistics, read at every scrape, as
// Namespace_db_pool_* metrics: connection gauges (acquired, idle,
// constructing, total, max) and cumulative counters (acquires, canceled
// and empty acquires, acquire wait time, new connections, connections
// destroyed for max lifetime and max idle time). It fails when a pool is
// already registered on this Metrics. A nil Metrics or nil p does nothing.
func (m *Metrics) RegisterPool(p PoolStater) error {
	if m == nil || p == nil {
		return nil
	}
	if err := m.reg.Register(newPoolCollector(m.namespace, p)); err != nil {
		return fmt.Errorf("metrics: pool: %w", err)
	}
	return nil
}

type poolCollector struct {
	pool PoolStater

	acquired, idle, constructing, total, max *prometheus.Desc

	acquires, canceledAcquires, emptyAcquires, acquireSeconds, emptyWaitSeconds *prometheus.Desc
	newConns, lifetimeDestroys, idleDestroys                                    *prometheus.Desc
}

func newPoolCollector(namespace string, p PoolStater) *poolCollector {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(namespace, "db_pool", name), help, nil, nil)
	}
	return &poolCollector{
		pool:             p,
		acquired:         desc("acquired_conns", "Connections currently checked out of the pool."),
		idle:             desc("idle_conns", "Idle connections in the pool."),
		constructing:     desc("constructing_conns", "Connections currently being established."),
		total:            desc("total_conns", "Connections in the pool: acquired, idle and constructing."),
		max:              desc("max_conns", "Maximum size of the pool."),
		acquires:         desc("acquires_total", "Successful connection acquires."),
		canceledAcquires: desc("canceled_acquires_total", "Acquires canceled by their context."),
		emptyAcquires:    desc("empty_acquires_total", "Successful acquires that had to wait for a connection because the pool was empty."),
		acquireSeconds:   desc("acquire_duration_seconds_total", "Total time spent in successful acquires."),
		emptyWaitSeconds: desc("empty_acquire_wait_seconds_total", "Total time successful acquires spent waiting because the pool was empty."),
		newConns:         desc("new_conns_total", "Connections opened."),
		lifetimeDestroys: desc("max_lifetime_destroys_total", "Connections closed for exceeding the maximum lifetime."),
		idleDestroys:     desc("max_idle_destroys_total", "Connections closed for exceeding the maximum idle time."),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.acquired, c.idle, c.constructing, c.total, c.max,
		c.acquires, c.canceledAcquires, c.emptyAcquires, c.acquireSeconds, c.emptyWaitSeconds,
		c.newConns, c.lifetimeDestroys, c.idleDestroys,
	} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	gauge := func(d *prometheus.Desc, v int32) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, float64(v))
	}
	counter := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v)
	}
	gauge(c.acquired, s.AcquiredConns())
	gauge(c.idle, s.IdleConns())
	gauge(c.constructing, s.ConstructingConns())
	gauge(c.total, s.TotalConns())
	gauge(c.max, s.MaxConns())
	counter(c.acquires, float64(s.AcquireCount()))
	counter(c.canceledAcquires, float64(s.CanceledAcquireCount()))
	counter(c.emptyAcquires, float64(s.EmptyAcquireCount()))
	counter(c.acquireSeconds, s.AcquireDuration().Seconds())
	counter(c.emptyWaitSeconds, s.EmptyAcquireWaitTime().Seconds())
	counter(c.newConns, float64(s.NewConnsCount()))
	counter(c.lifetimeDestroys, float64(s.MaxLifetimeDestroyCount()))
	counter(c.idleDestroys, float64(s.MaxIdleDestroyCount()))
}
