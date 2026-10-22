// Package otelrecorder exports adaptive pool metrics through OpenTelemetry,
// so they can reach any backend with an OpenTelemetry exporter or the
// OpenTelemetry Collector.
//
//	pool, err := adaptivepool.New(
//		adaptivepool.WithName("orders"),
//		adaptivepool.WithRecorder(otelrecorder.New(meterProvider)),
//	)
//
// Metric names, units and attributes follow the OpenTelemetry semantic
// conventions, modelled on the database connection pool metrics
// (db.client.connection.*):
//
//	adaptivepool.worker.count     UpDownCounter {worker}     adaptivepool.worker.state = idle | busy
//	adaptivepool.worker.limit     UpDownCounter {worker}
//	adaptivepool.worker.min       UpDownCounter {worker}
//	adaptivepool.queue.usage      UpDownCounter {job}
//	adaptivepool.queue.limit      UpDownCounter {job}
//	adaptivepool.job.submissions  Counter       {submission} adaptivepool.job.kind
//	adaptivepool.job.rejections   Counter       {rejection}  adaptivepool.job.kind, adaptivepool.rejection.reason
//	adaptivepool.job.duration     Histogram     s            adaptivepool.job.kind, adaptivepool.job.status
//	adaptivepool.job.wait_time    Histogram     s            adaptivepool.job.kind
//	adaptivepool.job.retries      Counter       {retry}      adaptivepool.job.kind
//	adaptivepool.worker.scale_ups Counter       {scale_up}
//
// Every metric carries adaptivepool.pool.name. Completed jobs are counted by
// the job.duration histogram, broken down by status.
package otelrecorder

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const instrumentationName = "github.com/iyashjayesh/go-adaptive-pool/otelrecorder"

// Attribute keys
const (
	PoolNameKey        = attribute.Key("adaptivepool.pool.name")
	JobKindKey         = attribute.Key("adaptivepool.job.kind")
	JobStatusKey       = attribute.Key("adaptivepool.job.status")
	RejectionReasonKey = attribute.Key("adaptivepool.rejection.reason")
	WorkerStateKey     = attribute.Key("adaptivepool.worker.state")
)

// durationBuckets are the explicit bucket boundaries, in seconds,
// recommended by the semantic conventions for duration histograms.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// ErrAlreadyAttached is returned when a Recorder is used by a second pool
// while the first is still running.
var ErrAlreadyAttached = errors.New("otelrecorder: recorder already attached to a pool")

// Recorder exports pool events as OpenTelemetry metrics. Use one Recorder
// per pool.
type Recorder struct {
	provider metric.MeterProvider
	attached atomic.Bool

	// state holds the instruments of the attached pool, nil when detached.
	// Each event loads it once, so Attach and Detach never race with recording.
	state atomic.Pointer[state]
}

type state struct {
	poolAttr     attribute.KeyValue
	submissions  metric.Int64Counter
	rejections   metric.Int64Counter
	dropped      metric.Int64Counter
	duration     metric.Float64Histogram
	waitTime     metric.Float64Histogram
	retries      metric.Int64Counter
	scaleUps     metric.Int64Counter
	scaleUpOpts  []metric.AddOption
	registration metric.Registration

	// kinds caches each kind's precomputed measurement options
	// (*kindOptions), so recording does not allocate attribute sets.
	kinds sync.Map
}

type kindOptions struct {
	add      []metric.AddOption    // pool, kind
	record   []metric.RecordOption // pool, kind
	byStatus sync.Map              // adaptivepool.Status -> []metric.RecordOption
	byReason sync.Map              // adaptivepool.RejectReason -> []metric.AddOption
}

var (
	_ adaptivepool.Recorder = (*Recorder)(nil)
	_ adaptivepool.Attacher = (*Recorder)(nil)
)

// New returns a Recorder that creates its instruments from provider. A nil
// provider uses the global one (otel.GetMeterProvider).
func New(provider metric.MeterProvider) *Recorder {
	if provider == nil {
		provider = otel.GetMeterProvider()
	}
	return &Recorder{provider: provider}
}

// Attach creates the instruments and registers the callback that reports
// worker and queue gauges. It is called by adaptivepool.New.
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
	meter := r.provider.Meter(instrumentationName)
	s := &state{poolAttr: PoolNameKey.String(info.Name)}
	s.scaleUpOpts = []metric.AddOption{metric.WithAttributeSet(attribute.NewSet(s.poolAttr))}

	var err error
	if s.submissions, err = meter.Int64Counter("adaptivepool.job.submissions",
		metric.WithUnit("{submission}"), metric.WithDescription("Jobs accepted into the queue")); err != nil {
		return nil, err
	}
	if s.rejections, err = meter.Int64Counter("adaptivepool.job.rejections",
		metric.WithUnit("{rejection}"), metric.WithDescription("Jobs rejected at submission")); err != nil {
		return nil, err
	}
	if s.dropped, err = meter.Int64Counter("adaptivepool.job.dropped",
		metric.WithUnit("{job}"), metric.WithDescription("Queued jobs a forced shutdown discarded without running")); err != nil {
		return nil, err
	}
	if s.duration, err = meter.Float64Histogram("adaptivepool.job.duration",
		metric.WithUnit("s"), metric.WithDescription("Job execution time, including retries"),
		metric.WithExplicitBucketBoundaries(durationBuckets...)); err != nil {
		return nil, err
	}
	if s.waitTime, err = meter.Float64Histogram("adaptivepool.job.wait_time",
		metric.WithUnit("s"), metric.WithDescription("Time jobs spent queued before execution"),
		metric.WithExplicitBucketBoundaries(durationBuckets...)); err != nil {
		return nil, err
	}
	if s.retries, err = meter.Int64Counter("adaptivepool.job.retries",
		metric.WithUnit("{retry}"), metric.WithDescription("Retries performed across all jobs")); err != nil {
		return nil, err
	}
	if s.scaleUps, err = meter.Int64Counter("adaptivepool.worker.scale_ups",
		metric.WithUnit("{scale_up}"), metric.WithDescription("Number of scale-up events")); err != nil {
		return nil, err
	}

	workers, err := meter.Int64ObservableUpDownCounter("adaptivepool.worker.count",
		metric.WithUnit("{worker}"), metric.WithDescription("Workers by state"))
	if err != nil {
		return nil, err
	}
	workerLimit, err := meter.Int64ObservableUpDownCounter("adaptivepool.worker.limit",
		metric.WithUnit("{worker}"), metric.WithDescription("Maximum number of workers"))
	if err != nil {
		return nil, err
	}
	workerMin, err := meter.Int64ObservableUpDownCounter("adaptivepool.worker.min",
		metric.WithUnit("{worker}"), metric.WithDescription("Minimum number of workers"))
	if err != nil {
		return nil, err
	}
	queueUsage, err := meter.Int64ObservableUpDownCounter("adaptivepool.queue.usage",
		metric.WithUnit("{job}"), metric.WithDescription("Jobs waiting in the queue"))
	if err != nil {
		return nil, err
	}
	queueLimit, err := meter.Int64ObservableUpDownCounter("adaptivepool.queue.limit",
		metric.WithUnit("{job}"), metric.WithDescription("Capacity of the queue"))
	if err != nil {
		return nil, err
	}

	pool := metric.WithAttributeSet(attribute.NewSet(s.poolAttr))
	idle := metric.WithAttributeSet(attribute.NewSet(s.poolAttr, WorkerStateKey.String("idle")))
	busy := metric.WithAttributeSet(attribute.NewSet(s.poolAttr, WorkerStateKey.String("busy")))
	s.registration, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		active, inUse := stats.ActiveWorkers(), stats.BusyWorkers()
		o.ObserveInt64(workers, int64(max(active-inUse, 0)), idle)
		o.ObserveInt64(workers, int64(inUse), busy)
		o.ObserveInt64(workerLimit, int64(info.MaxWorkers), pool)
		o.ObserveInt64(workerMin, int64(info.MinWorkers), pool)
		o.ObserveInt64(queueUsage, int64(stats.QueueDepth()), pool)
		o.ObserveInt64(queueLimit, int64(info.QueueSize), pool)
		return nil
	}, workers, workerLimit, workerMin, queueUsage, queueLimit)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Detach unregisters the gauge callback and drops later events. It is called
// when the pool shuts down, after which the Recorder may be attached to a new
// pool.
func (r *Recorder) Detach() {
	s := r.state.Swap(nil)
	if s == nil {
		return
	}
	_ = s.registration.Unregister()
	r.attached.Store(false)
}

// options returns the cached measurement options for kind
func (s *state) options(kind string) *kindOptions {
	if o, ok := s.kinds.Load(kind); ok {
		return o.(*kindOptions)
	}
	opt := metric.WithAttributeSet(attribute.NewSet(s.poolAttr, JobKindKey.String(kind)))
	o, _ := s.kinds.LoadOrStore(kind, &kindOptions{
		add:    []metric.AddOption{opt},
		record: []metric.RecordOption{opt},
	})
	return o.(*kindOptions)
}

func (s *state) statusOptions(o *kindOptions, kind string, status adaptivepool.Status) []metric.RecordOption {
	if v, ok := o.byStatus.Load(status); ok {
		return v.([]metric.RecordOption)
	}
	set := attribute.NewSet(s.poolAttr, JobKindKey.String(kind), JobStatusKey.String(string(status)))
	v, _ := o.byStatus.LoadOrStore(status, []metric.RecordOption{metric.WithAttributeSet(set)})
	return v.([]metric.RecordOption)
}

func (s *state) reasonOptions(o *kindOptions, kind string, reason adaptivepool.RejectReason) []metric.AddOption {
	if v, ok := o.byReason.Load(reason); ok {
		return v.([]metric.AddOption)
	}
	set := attribute.NewSet(s.poolAttr, JobKindKey.String(kind), RejectionReasonKey.String(string(reason)))
	v, _ := o.byReason.LoadOrStore(reason, []metric.AddOption{metric.WithAttributeSet(set)})
	return v.([]metric.AddOption)
}

// JobSubmitted implements adaptivepool.Recorder
func (r *Recorder) JobSubmitted(kind string) {
	if s := r.state.Load(); s != nil {
		s.submissions.Add(context.Background(), 1, s.options(kind).add...)
	}
}

// JobRejected implements adaptivepool.Recorder
func (r *Recorder) JobRejected(kind string, reason adaptivepool.RejectReason) {
	if s := r.state.Load(); s != nil {
		s.rejections.Add(context.Background(), 1, s.reasonOptions(s.options(kind), kind, reason)...)
	}
}

// JobCompleted implements adaptivepool.Recorder
func (r *Recorder) JobCompleted(kind string, status adaptivepool.Status, attempts int, queueWait, duration time.Duration) {
	s := r.state.Load()
	if s == nil {
		return
	}
	ctx := context.Background()
	o := s.options(kind)
	s.duration.Record(ctx, duration.Seconds(), s.statusOptions(o, kind, status)...)
	s.waitTime.Record(ctx, queueWait.Seconds(), o.record...)
	if retries := attempts - 1; retries > 0 {
		s.retries.Add(ctx, int64(retries), o.add...)
	}
}

// JobDropped implements adaptivepool.Recorder
func (r *Recorder) JobDropped(kind string) {
	if s := r.state.Load(); s != nil {
		s.dropped.Add(context.Background(), 1, s.options(kind).add...)
	}
}

// ScaledUp implements adaptivepool.Recorder
func (r *Recorder) ScaledUp(int) {
	if s := r.state.Load(); s != nil {
		s.scaleUps.Add(context.Background(), 1, s.scaleUpOpts...)
	}
}
