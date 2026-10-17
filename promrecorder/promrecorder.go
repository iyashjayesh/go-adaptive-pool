// Package promrecorder exports adaptive pool metrics to Prometheus.
//
//	reg := prometheus.NewRegistry()
//	pool, err := adaptivepool.New(
//		adaptivepool.WithName("orders"),
//		adaptivepool.WithRecorder(promrecorder.New(reg)),
//	)
//	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
//
// Metrics are named adaptivepool_* and carry a "pool" label when the pool is
// named. Collectors are registered when the pool is created and unregistered
// when it shuts down; give each pool sharing a registry a distinct name.
package promrecorder

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "adaptivepool"

// ErrAlreadyAttached is returned when a Recorder is used by a second pool
// while the first is still running.
var ErrAlreadyAttached = errors.New("promrecorder: recorder already attached to a pool")

// Recorder exports pool events as Prometheus metrics. Gauges are GaugeFuncs
// read at scrape time, so they cost nothing between scrapes. Use one
// Recorder per pool.
type Recorder struct {
	reg      prometheus.Registerer
	attached atomic.Bool

	// state holds the collectors of the attached pool, nil when detached.
	// Each event loads it once, so Attach and Detach never race with recording.
	state atomic.Pointer[state]
}

type state struct {
	collectors []prometheus.Collector

	jobsSubmitted *prometheus.CounterVec   // kind
	jobsRejected  *prometheus.CounterVec   // kind, reason
	jobsCompleted *prometheus.CounterVec   // kind, status
	jobDuration   *prometheus.HistogramVec // kind
	queueWait     *prometheus.HistogramVec // kind
	jobsRetried   *prometheus.CounterVec   // kind
	retries       *prometheus.CounterVec   // kind
	scaleUps      prometheus.Counter

	// kinds caches each kind's resolved series (*kindSeries). Resolving a
	// labelled series takes a lock inside client_golang; caching it makes
	// the per-job path a lock-free map read.
	kinds sync.Map
}

type kindSeries struct {
	submitted   prometheus.Counter
	duration    prometheus.Observer
	queueWait   prometheus.Observer
	jobsRetried prometheus.Counter
	retries     prometheus.Counter
	completed   sync.Map // adaptivepool.Status -> prometheus.Counter
	rejected    sync.Map // adaptivepool.RejectReason -> prometheus.Counter
}

var (
	_ adaptivepool.Recorder = (*Recorder)(nil)
	_ adaptivepool.Attacher = (*Recorder)(nil)
)

// New returns a Recorder that registers its metrics with reg, e.g.
// prometheus.DefaultRegisterer or a dedicated registry.
func New(reg prometheus.Registerer) *Recorder {
	return &Recorder{reg: reg}
}

// Attach creates the collectors, labelled with the pool name, and registers
// them, rolling back on failure. It is called by adaptivepool.New.
func (r *Recorder) Attach(info adaptivepool.PoolInfo, stats adaptivepool.Metrics) error {
	if !r.attached.CompareAndSwap(false, true) {
		return ErrAlreadyAttached
	}
	s, err := r.register(info, stats)
	if err != nil {
		r.attached.Store(false)
		return err
	}
	r.state.Store(s)
	return nil
}

func (r *Recorder) register(info adaptivepool.PoolInfo, stats adaptivepool.Metrics) (*state, error) {
	var constLabels prometheus.Labels
	if info.Name != "" {
		constLabels = prometheus.Labels{"pool": info.Name}
	}
	gauge := func(name, help string, fn func() float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: namespace, Name: name, Help: help, ConstLabels: constLabels,
		}, fn)
	}
	counterVec := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: name, Help: help, ConstLabels: constLabels,
		}, labels)
	}
	histogramVec := func(name, help string, buckets []float64) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: name, Help: help, ConstLabels: constLabels, Buckets: buckets,
		}, []string{"kind"})
	}

	s := &state{}
	s.jobsSubmitted = counterVec("jobs_submitted_total", "Jobs accepted into the queue", "kind")
	s.jobsRejected = counterVec("jobs_rejected_total", "Jobs rejected at submission", "kind", "reason")
	s.jobsCompleted = counterVec("jobs_completed_total", "Jobs that finished executing, by final status", "kind", "status")
	s.jobDuration = histogramVec("job_duration_seconds", "Job execution time in seconds", prometheus.DefBuckets)
	s.queueWait = histogramVec("job_queue_wait_seconds", "Time jobs spent queued before execution in seconds",
		prometheus.ExponentialBuckets(0.0005, 4, 10))
	s.jobsRetried = counterVec("jobs_retried_total", "Jobs that needed at least one retry", "kind")
	s.retries = counterVec("retry_attempts_total", "Retries performed across all jobs", "kind")
	s.scaleUps = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Name: "scale_up_events_total", Help: "Number of scale-up events", ConstLabels: constLabels,
	})

	s.collectors = []prometheus.Collector{
		s.jobsSubmitted,
		s.jobsRejected,
		s.jobsCompleted,
		s.jobDuration,
		s.queueWait,
		s.jobsRetried,
		s.retries,
		s.scaleUps,
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

	for i, c := range s.collectors {
		if err := r.reg.Register(c); err != nil {
			for _, done := range s.collectors[:i] {
				r.reg.Unregister(done)
			}
			return nil, err
		}
	}
	return s, nil
}

// Detach unregisters all collectors and drops later events. It is called
// when the pool shuts down, after which the Recorder may be attached to a
// new pool.
func (r *Recorder) Detach() {
	s := r.state.Swap(nil)
	if s == nil {
		return
	}
	for _, c := range s.collectors {
		r.reg.Unregister(c)
	}
	r.attached.Store(false)
}

// series returns the cached series for kind, resolving them on first use
func (s *state) series(kind string) *kindSeries {
	if k, ok := s.kinds.Load(kind); ok {
		return k.(*kindSeries)
	}
	k, _ := s.kinds.LoadOrStore(kind, &kindSeries{
		submitted:   s.jobsSubmitted.WithLabelValues(kind),
		duration:    s.jobDuration.WithLabelValues(kind),
		queueWait:   s.queueWait.WithLabelValues(kind),
		jobsRetried: s.jobsRetried.WithLabelValues(kind),
		retries:     s.retries.WithLabelValues(kind),
	})
	return k.(*kindSeries)
}

// completedCounter returns the cached completion counter for kind and status
func (s *state) completedCounter(k *kindSeries, kind string, status adaptivepool.Status) prometheus.Counter {
	if c, ok := k.completed.Load(status); ok {
		return c.(prometheus.Counter)
	}
	c, _ := k.completed.LoadOrStore(status, s.jobsCompleted.WithLabelValues(kind, string(status)))
	return c.(prometheus.Counter)
}

// rejectedCounter returns the cached rejection counter for kind and reason
func (s *state) rejectedCounter(k *kindSeries, kind string, reason adaptivepool.RejectReason) prometheus.Counter {
	if c, ok := k.rejected.Load(reason); ok {
		return c.(prometheus.Counter)
	}
	c, _ := k.rejected.LoadOrStore(reason, s.jobsRejected.WithLabelValues(kind, string(reason)))
	return c.(prometheus.Counter)
}

// JobSubmitted implements adaptivepool.Recorder
func (r *Recorder) JobSubmitted(kind string) {
	if s := r.state.Load(); s != nil {
		s.series(kind).submitted.Inc()
	}
}

// JobRejected implements adaptivepool.Recorder
func (r *Recorder) JobRejected(kind string, reason adaptivepool.RejectReason) {
	if s := r.state.Load(); s != nil {
		s.rejectedCounter(s.series(kind), kind, reason).Inc()
	}
}

// JobCompleted implements adaptivepool.Recorder
func (r *Recorder) JobCompleted(kind string, status adaptivepool.Status, attempts int, queueWait, duration time.Duration) {
	s := r.state.Load()
	if s == nil {
		return
	}
	k := s.series(kind)
	s.completedCounter(k, kind, status).Inc()
	k.duration.Observe(duration.Seconds())
	k.queueWait.Observe(queueWait.Seconds())
	if retries := attempts - 1; retries > 0 {
		k.jobsRetried.Inc()
		k.retries.Add(float64(retries))
	}
}

// ScaledUp implements adaptivepool.Recorder
func (r *Recorder) ScaledUp(int) {
	if s := r.state.Load(); s != nil {
		s.scaleUps.Inc()
	}
}
