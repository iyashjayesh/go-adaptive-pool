package adaptivepool

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsNamespace = "adaptivepool"
	defaultKind      = "default"
)

// Rejection reasons used for the "reason" label of jobs_rejected_total
const (
	rejectShutdown = "shutdown"
	rejectTimeout  = "timeout"
	rejectCanceled = "canceled"
)

// Metrics provides observability into pool behavior
type Metrics interface {
	// QueueDepth returns the current number of jobs in the queue
	QueueDepth() int

	// ActiveWorkers returns the current number of active workers
	ActiveWorkers() int

	// BusyWorkers returns the number of workers currently executing a job
	BusyWorkers() int

	// JobsProcessed returns the total number of jobs processed
	JobsProcessed() int64

	// JobsFailed returns the number of processed jobs that ended with an error or panic
	JobsFailed() int64

	// JobsRejected returns the total number of jobs rejected
	JobsRejected() int64

	// JobsRetried returns the number of processed jobs that needed at least one retry
	JobsRetried() int64

	// RetryAttempts returns the total number of retries across all jobs
	RetryAttempts() int64

	// AvgJobLatency returns the average job execution time
	AvgJobLatency() time.Duration

	// AvgQueueWait returns the average time jobs spent queued before execution
	AvgQueueWait() time.Duration
}

// metrics implements the Metrics interface with Prometheus integration
type metrics struct {
	enabled bool

	// Internal counters (always tracked)
	jobsProcessed  atomic.Int64
	jobsFailed     atomic.Int64
	jobsRejected   atomic.Int64
	jobsRetried    atomic.Int64
	retryAttempts  atomic.Int64
	totalLatency   atomic.Int64 // in nanoseconds
	totalQueueWait atomic.Int64 // in nanoseconds

	// Reference to pool state for current values
	state *poolState

	// Prometheus metrics (optional). Gauges are GaugeFuncs read at scrape
	// time, so they cost nothing between scrapes.
	collectors        []prometheus.Collector
	promJobsSubmitted *prometheus.CounterVec   // kind
	promJobsRejected  *prometheus.CounterVec   // kind, reason
	promJobsCompleted *prometheus.CounterVec   // kind, status
	promJobDuration   *prometheus.HistogramVec // kind
	promQueueWait     *prometheus.HistogramVec // kind
	promJobsRetried   *prometheus.CounterVec   // kind
	promRetries       *prometheus.CounterVec   // kind
	promScaleUps      prometheus.Counter
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
	counterVec := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace, Name: name, Help: help, ConstLabels: constLabels,
		}, labels)
	}
	histogramVec := func(name, help string, buckets []float64) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: metricsNamespace, Name: name, Help: help, ConstLabels: constLabels, Buckets: buckets,
		}, []string{"kind"})
	}

	m.promJobsSubmitted = counterVec("jobs_submitted_total", "Jobs accepted into the queue", "kind")
	m.promJobsRejected = counterVec("jobs_rejected_total", "Jobs rejected at submission", "kind", "reason")
	m.promJobsCompleted = counterVec("jobs_completed_total", "Jobs that finished executing, by final status", "kind", "status")
	m.promJobDuration = histogramVec("job_duration_seconds", "Job execution time in seconds", prometheus.DefBuckets)
	m.promQueueWait = histogramVec("job_queue_wait_seconds", "Time jobs spent queued before execution in seconds",
		prometheus.ExponentialBuckets(0.0005, 4, 10))
	m.promJobsRetried = counterVec("jobs_retried_total", "Jobs that needed at least one retry", "kind")
	m.promRetries = counterVec("retry_attempts_total", "Retries performed across all jobs", "kind")
	m.promScaleUps = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace, Name: "scale_up_events_total", Help: "Number of scale-up events", ConstLabels: constLabels,
	})

	m.collectors = []prometheus.Collector{
		m.promJobsSubmitted,
		m.promJobsRejected,
		m.promJobsCompleted,
		m.promJobDuration,
		m.promQueueWait,
		m.promJobsRetried,
		m.promRetries,
		m.promScaleUps,
		gauge("queue_depth", "Current number of jobs in the queue",
			func() float64 { return float64(state.getQueueDepth()) }),
		gauge("queue_capacity", "Capacity of the job queue",
			func() float64 { return float64(state.getQueueCapacity()) }),
		gauge("active_workers", "Current number of active workers",
			func() float64 { return float64(state.getWorkerCount()) }),
		gauge("busy_workers", "Number of workers currently executing a job",
			func() float64 { return float64(state.busyWorkers.Load()) }),
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

func kindLabel(kind string) string {
	if kind == "" {
		return defaultKind
	}
	return kind
}

// QueueDepth returns the current queue depth
func (m *metrics) QueueDepth() int {
	return m.state.getQueueDepth()
}

// ActiveWorkers returns the current number of active workers
func (m *metrics) ActiveWorkers() int {
	return m.state.getWorkerCount()
}

// BusyWorkers returns the number of workers currently executing a job
func (m *metrics) BusyWorkers() int {
	return int(m.state.busyWorkers.Load())
}

// JobsProcessed returns the total number of jobs processed
func (m *metrics) JobsProcessed() int64 {
	return m.jobsProcessed.Load()
}

// JobsFailed returns the number of processed jobs that did not succeed
func (m *metrics) JobsFailed() int64 {
	return m.jobsFailed.Load()
}

// JobsRejected returns the total number of jobs rejected
func (m *metrics) JobsRejected() int64 {
	return m.jobsRejected.Load()
}

// JobsRetried returns the number of jobs that needed at least one retry
func (m *metrics) JobsRetried() int64 {
	return m.jobsRetried.Load()
}

// RetryAttempts returns the total number of retries across all jobs
func (m *metrics) RetryAttempts() int64 {
	return m.retryAttempts.Load()
}

// AvgJobLatency returns the average job execution time
func (m *metrics) AvgJobLatency() time.Duration {
	return average(m.totalLatency.Load(), m.jobsProcessed.Load())
}

// AvgQueueWait returns the average time jobs spent queued
func (m *metrics) AvgQueueWait() time.Duration {
	return average(m.totalQueueWait.Load(), m.jobsProcessed.Load())
}

func average(totalNs, n int64) time.Duration {
	if n == 0 {
		return 0
	}
	return time.Duration(totalNs / n)
}

// recordJobSubmitted records a job accepted into the queue
func (m *metrics) recordJobSubmitted(kind string) {
	if m.enabled {
		m.promJobsSubmitted.WithLabelValues(kindLabel(kind)).Inc()
	}
}

// recordJobRejected records a rejected job
func (m *metrics) recordJobRejected(kind, reason string) {
	m.jobsRejected.Add(1)

	if m.enabled {
		m.promJobsRejected.WithLabelValues(kindLabel(kind), reason).Inc()
	}
}

// recordJobDone records a finished job. Retry counters are derived from the
// attempt count here rather than per retry, keeping the retry loop free of
// metric updates.
func (m *metrics) recordJobDone(kind string, status Status, attempts int, queueWait, duration time.Duration) {
	m.jobsProcessed.Add(1)
	m.totalLatency.Add(int64(duration))
	m.totalQueueWait.Add(int64(queueWait))
	if status != StatusSuccess {
		m.jobsFailed.Add(1)
	}
	retries := attempts - 1
	if retries > 0 {
		m.jobsRetried.Add(1)
		m.retryAttempts.Add(int64(retries))
	}

	if m.enabled {
		kind = kindLabel(kind)
		m.promJobsCompleted.WithLabelValues(kind, string(status)).Inc()
		m.promJobDuration.WithLabelValues(kind).Observe(duration.Seconds())
		m.promQueueWait.WithLabelValues(kind).Observe(queueWait.Seconds())
		if retries > 0 {
			m.promJobsRetried.WithLabelValues(kind).Inc()
			m.promRetries.WithLabelValues(kind).Add(float64(retries))
		}
	}
}

// recordScaleUp records a scale-up event
func (m *metrics) recordScaleUp() {
	if m.enabled {
		m.promScaleUps.Inc()
	}
}
