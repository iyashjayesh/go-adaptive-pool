package adaptivepool

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

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

	// Prometheus metrics (optional)
	promQueueDepth    prometheus.Gauge
	promActiveWorkers prometheus.Gauge
	promJobsProcessed prometheus.Counter
	promJobsRejected  prometheus.Counter
	promJobLatency    prometheus.Histogram
}

// newMetrics Creates a new metrics collector
func newMetrics(state *poolState, enabled bool, namespace string) *metrics {
	m := &metrics{
		enabled: enabled,
		state:   state,
	}

	if enabled {
		m.promQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "queue_depth",
			Help:      "Current number of jobs in the queue",
		})

		m.promActiveWorkers = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "active_workers",
			Help:      "Current number of active workers",
		})

		m.promJobsProcessed = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "jobs_processed_total",
			Help:      "Total number of jobs processed",
		})

		m.promJobsRejected = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "jobs_rejected_total",
			Help:      "Total number of jobs rejected",
		})

		m.promJobLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "job_latency_seconds",
			Help:      "Job execution latency in seconds",
			Buckets:   prometheus.DefBuckets,
		})
	}

	return m
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

// updateGauges updates the gauge metrics
func (m *metrics) updateGauges() {
	if m.enabled {
		m.promQueueDepth.Set(float64(m.QueueDepth()))
		m.promActiveWorkers.Set(float64(m.ActiveWorkers()))
	}
}
