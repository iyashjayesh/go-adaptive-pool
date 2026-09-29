package adaptivepool

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const metricsNamespace = "adaptivepool"

// Metrics provides observability into pool behavior
type Metrics interface {
	// QueueDepth returns the current number of jobs in the queue
	QueueDepth() int

	// ActiveWorkers returns the current number of active workers
	ActiveWorkers() int

	// JobsProcessed returns the total number of jobs processed
	JobsProcessed() int64

	// JobsRejected returns the total number of jobs rejected
	JobsRejected() int64

	// AvgJobLatency returns the average job latency
	AvgJobLatency() time.Duration
}

// metrics implements the Metrics interface with Prometheus integration
type metrics struct {
	enabled bool

	// Internal counters (always tracked)
	jobsProcessed atomic.Int64
	jobsRejected  atomic.Int64
	totalLatency  atomic.Int64 // in nanoseconds

	// Reference to pool state for current values
	state *poolState

	// Prometheus metrics (optional). Gauges are GaugeFuncs read at scrape
	// time, so they cost nothing between scrapes.
	collectors        []prometheus.Collector
	promJobsProcessed prometheus.Counter
	promJobsRejected  prometheus.Counter
	promJobLatency    prometheus.Histogram
}

// newMetrics Creates a new metrics collector
func newMetrics(state *poolState, config *Config) *metrics {
	m := &metrics{
		enabled: config.metricsEnabled,
		state:   state,
	}
	if !m.enabled {
		return m
	}

	var constLabels prometheus.Labels
	if config.name != "" {
		constLabels = prometheus.Labels{"pool": config.name}
	}
	gauge := func(name, help string, fn func() float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: metricsNamespace, Name: name, Help: help, ConstLabels: constLabels,
		}, fn)
	}

	m.promJobsProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   metricsNamespace,
		Name:        "jobs_processed_total",
		Help:        "Total number of jobs processed",
		ConstLabels: constLabels,
	})
	m.promJobsRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   metricsNamespace,
		Name:        "jobs_rejected_total",
		Help:        "Total number of jobs rejected",
		ConstLabels: constLabels,
	})
	m.promJobLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   metricsNamespace,
		Name:        "job_latency_seconds",
		Help:        "Job execution latency in seconds",
		Buckets:     prometheus.DefBuckets,
		ConstLabels: constLabels,
	})

	m.collectors = []prometheus.Collector{
		m.promJobsProcessed,
		m.promJobsRejected,
		m.promJobLatency,
		gauge("queue_depth", "Current number of jobs in the queue",
			func() float64 { return float64(state.getQueueDepth()) }),
		gauge("queue_capacity", "Capacity of the job queue",
			func() float64 { return float64(state.getQueueCapacity()) }),
		gauge("active_workers", "Current number of active workers",
			func() float64 { return float64(state.getWorkerCount()) }),
		gauge("max_workers", "Configured maximum number of workers",
			func() float64 { return float64(config.maxWorkers) }),
	}

	return m
}

// register registers all collectors, rolling back on failure
func (m *metrics) register(reg prometheus.Registerer) error {
	for i, c := range m.collectors {
		if err := reg.Register(c); err != nil {
			for _, done := range m.collectors[:i] {
				reg.Unregister(done)
			}
			return err
		}
	}
	return nil
}

// unregister removes all collectors from the registry
func (m *metrics) unregister(reg prometheus.Registerer) {
	for _, c := range m.collectors {
		reg.Unregister(c)
	}
}

// QueueDepth returns the current queue depth
func (m *metrics) QueueDepth() int {
	return m.state.getQueueDepth()
}

// ActiveWorkers returns the current number of active workers
func (m *metrics) ActiveWorkers() int {
	return m.state.getWorkerCount()
}

// JobsProcessed returns the total number of jobs processed
func (m *metrics) JobsProcessed() int64 {
	return m.jobsProcessed.Load()
}

// JobsRejected returns the total number of jobs rejected
func (m *metrics) JobsRejected() int64 {
	return m.jobsRejected.Load()
}

// AvgJobLatency returns the average job latency
func (m *metrics) AvgJobLatency() time.Duration {
	processed := m.jobsProcessed.Load()
	if processed == 0 {
		return 0
	}
	totalNs := m.totalLatency.Load()
	return time.Duration(totalNs / processed)
}

// recordJobProcessed records a processed job with its latency
func (m *metrics) recordJobProcessed(latency time.Duration) {
	m.jobsProcessed.Add(1)
	m.totalLatency.Add(int64(latency))

	if m.enabled {
		m.promJobsProcessed.Inc()
		m.promJobLatency.Observe(latency.Seconds())
	}
}

// recordJobRejected records a rejected job
func (m *metrics) recordJobRejected() {
	m.jobsRejected.Add(1)

	if m.enabled {
		m.promJobsRejected.Inc()
	}
}
