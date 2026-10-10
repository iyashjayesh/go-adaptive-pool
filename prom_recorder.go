package adaptivepool

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const metricsNamespace = "adaptivepool"

// promRecorder exports pool events as Prometheus metrics. Gauges are
// GaugeFuncs read at scrape time, so they cost nothing between scrapes.
type promRecorder struct {
	reg        prometheus.Registerer
	collectors []prometheus.Collector

	jobsSubmitted *prometheus.CounterVec   // kind
	jobsRejected  *prometheus.CounterVec   // kind, reason
	jobsCompleted *prometheus.CounterVec   // kind, status
	jobDuration   *prometheus.HistogramVec // kind
	queueWait     *prometheus.HistogramVec // kind
	jobsRetried   *prometheus.CounterVec   // kind
	retries       *prometheus.CounterVec   // kind
	scaleUps      prometheus.Counter
}

func newPromRecorder(reg prometheus.Registerer) *promRecorder {
	return &promRecorder{reg: reg}
}

// Attach creates the collectors, labelled with the pool name, and registers
// them, rolling back on failure
func (r *promRecorder) Attach(info poolInfo, stats Metrics) error {
	var constLabels prometheus.Labels
	if info.Name != "" {
		constLabels = prometheus.Labels{"pool": info.Name}
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

	r.jobsSubmitted = counterVec("jobs_submitted_total", "Jobs accepted into the queue", "kind")
	r.jobsRejected = counterVec("jobs_rejected_total", "Jobs rejected at submission", "kind", "reason")
	r.jobsCompleted = counterVec("jobs_completed_total", "Jobs that finished executing, by final status", "kind", "status")
	r.jobDuration = histogramVec("job_duration_seconds", "Job execution time in seconds", prometheus.DefBuckets)
	r.queueWait = histogramVec("job_queue_wait_seconds", "Time jobs spent queued before execution in seconds",
		prometheus.ExponentialBuckets(0.0005, 4, 10))
	r.jobsRetried = counterVec("jobs_retried_total", "Jobs that needed at least one retry", "kind")
	r.retries = counterVec("retry_attempts_total", "Retries performed across all jobs", "kind")
	r.scaleUps = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace, Name: "scale_up_events_total", Help: "Number of scale-up events", ConstLabels: constLabels,
	})

	r.collectors = []prometheus.Collector{
		r.jobsSubmitted,
		r.jobsRejected,
		r.jobsCompleted,
		r.jobDuration,
		r.queueWait,
		r.jobsRetried,
		r.retries,
		r.scaleUps,
		gauge("queue_depth", "Current number of jobs in the queue",
			func() float64 { return float64(stats.QueueDepth()) }),
		gauge("queue_capacity", "Capacity of the job queue",
			func() float64 { return float64(info.QueueSize) }),
		gauge("active_workers", "Current number of active workers",
			func() float64 { return float64(stats.ActiveWorkers()) }),
		gauge("busy_workers", "Number of workers currently executing a job",
			func() float64 { return float64(stats.BusyWorkers()) }),
		gauge("max_workers", "Configured maximum number of workers",
			func() float64 { return float64(info.MaxWorkers) }),
	}

	for i, c := range r.collectors {
		if err := r.reg.Register(c); err != nil {
			for _, done := range r.collectors[:i] {
				r.reg.Unregister(done)
			}
			return err
		}
	}
	return nil
}

// Detach unregisters all collectors
func (r *promRecorder) Detach() {
	for _, c := range r.collectors {
		r.reg.Unregister(c)
	}
}

func (r *promRecorder) JobSubmitted(kind string) {
	r.jobsSubmitted.WithLabelValues(kind).Inc()
}

func (r *promRecorder) JobRejected(kind string, reason rejectReason) {
	r.jobsRejected.WithLabelValues(kind, string(reason)).Inc()
}

func (r *promRecorder) JobCompleted(kind string, status Status, attempts int, queueWait, duration time.Duration) {
	r.jobsCompleted.WithLabelValues(kind, string(status)).Inc()
	r.jobDuration.WithLabelValues(kind).Observe(duration.Seconds())
	r.queueWait.WithLabelValues(kind).Observe(queueWait.Seconds())
	if retries := attempts - 1; retries > 0 {
		r.jobsRetried.WithLabelValues(kind).Inc()
		r.retries.WithLabelValues(kind).Add(float64(retries))
	}
}

func (r *promRecorder) ScaledUp(int) {
	r.scaleUps.Inc()
}
