package otelrecorder

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// collector reads metrics from an in-memory reader
type collector struct {
	t      *testing.T
	reader *sdkmetric.ManualReader
}

func newCollector(t *testing.T) (*collector, *sdkmetric.MeterProvider) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	return &collector{t: t, reader: reader}, mp
}

func (c *collector) metric(name string) (metricdata.Metrics, bool) {
	var rm metricdata.ResourceMetrics
	require.NoError(c.t, c.reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// sum returns the int64 sum data point value matching attrs
func (c *collector) sum(name string, attrs ...attribute.KeyValue) int64 {
	m, ok := c.metric(name)
	require.True(c.t, ok, "metric %s not found", name)
	want := attribute.NewSet(attrs...)
	for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
		if dp.Attributes.Equals(&want) {
			return dp.Value
		}
	}
	c.t.Fatalf("no %s data point with %v", name, attrs)
	return 0
}

// histogramCount returns the observation count of the data point matching attrs
func (c *collector) histogramCount(name string, attrs ...attribute.KeyValue) uint64 {
	m, ok := c.metric(name)
	require.True(c.t, ok, "metric %s not found", name)
	want := attribute.NewSet(attrs...)
	for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
		if dp.Attributes.Equals(&want) {
			return dp.Count
		}
	}
	c.t.Fatalf("no %s data point with %v", name, attrs)
	return 0
}

func newPool(t *testing.T, opts ...adaptivepool.Option) adaptivepool.Pool {
	t.Helper()
	opts = append([]adaptivepool.Option{adaptivepool.WithLogger(slog.New(slog.DiscardHandler))}, opts...)
	p, err := adaptivepool.New(opts...)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	return p
}

func TestInstrumentsFollowSemanticConventions(t *testing.T) {
	c, mp := newCollector(t)
	p := newPool(t, adaptivepool.WithName("orders"), adaptivepool.WithRecorder(New(mp)))
	require.NoError(t, p.SubmitTask(context.Background(), adaptivepool.Task{Kind: "email", Run: func(context.Context) error { return nil }}))
	require.Eventually(t, func() bool { return p.Metrics().JobsProcessed() == 1 }, 2*time.Second, time.Millisecond)

	units := map[string]string{
		"adaptivepool.worker.count":    "{worker}",
		"adaptivepool.worker.limit":    "{worker}",
		"adaptivepool.worker.min":      "{worker}",
		"adaptivepool.queue.usage":     "{job}",
		"adaptivepool.queue.limit":     "{job}",
		"adaptivepool.job.submissions": "{submission}",
		"adaptivepool.job.duration":    "s",
		"adaptivepool.job.wait_time":   "s",
	}
	for name, unit := range units {
		m, ok := c.metric(name)
		if assert.True(t, ok, name) {
			assert.Equal(t, unit, m.Unit, name)
		}
	}
}

func TestGauges(t *testing.T) {
	c, mp := newCollector(t)
	p := newPool(t, adaptivepool.WithName("orders"),
		adaptivepool.WithMinWorkers(2), adaptivepool.WithMaxWorkers(4), adaptivepool.WithQueueSize(10),
		adaptivepool.WithRecorder(New(mp)))

	release := make(chan struct{})
	defer close(release)
	require.NoError(t, p.Submit(context.Background(), func(context.Context) error { <-release; return nil }))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, 2*time.Second, time.Millisecond)

	pool := PoolNameKey.String("orders")
	assert.Equal(t, int64(1), c.sum("adaptivepool.worker.count", pool, WorkerStateKey.String("busy")))
	assert.Equal(t, int64(1), c.sum("adaptivepool.worker.count", pool, WorkerStateKey.String("idle")))
	assert.Equal(t, int64(4), c.sum("adaptivepool.worker.limit", pool))
	assert.Equal(t, int64(2), c.sum("adaptivepool.worker.min", pool))
	assert.Equal(t, int64(0), c.sum("adaptivepool.queue.usage", pool))
	assert.Equal(t, int64(10), c.sum("adaptivepool.queue.limit", pool))
}

func TestJobOutcomes(t *testing.T) {
	c, mp := newCollector(t)
	results := make(chan adaptivepool.TaskResult, 3)
	p := newPool(t, adaptivepool.WithName("orders"), adaptivepool.WithRecorder(New(mp)),
		adaptivepool.WithMinWorkers(1), adaptivepool.WithMaxWorkers(1),
		adaptivepool.WithRetry(adaptivepool.RetryPolicy{MaxAttempts: 3, Backoff: adaptivepool.ConstantBackoff{}}),
		adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) { results <- r }))

	ctx := context.Background()
	ok := func(context.Context) error { return nil }
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "email", Run: ok}))
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "email", Run: func(context.Context) error { return errors.New("down") }}))
	require.NoError(t, p.Submit(ctx, ok))
	for range 3 {
		<-results
	}

	pool := PoolNameKey.String("orders")
	email, def := JobKindKey.String("email"), JobKindKey.String("default")
	assert.Equal(t, int64(2), c.sum("adaptivepool.job.submissions", pool, email))
	assert.Equal(t, int64(1), c.sum("adaptivepool.job.submissions", pool, def))
	assert.Equal(t, uint64(1), c.histogramCount("adaptivepool.job.duration", pool, email, JobStatusKey.String("success")))
	assert.Equal(t, uint64(1), c.histogramCount("adaptivepool.job.duration", pool, email, JobStatusKey.String("retries_exhausted")))
	assert.Equal(t, uint64(2), c.histogramCount("adaptivepool.job.wait_time", pool, email))
	assert.Equal(t, int64(2), c.sum("adaptivepool.job.retries", pool, email))
}

func TestDroppedJobs(t *testing.T) {
	c, mp := newCollector(t)
	rec := New(mp)
	require.NoError(t, rec.Attach(adaptivepool.PoolInfo{Name: "orders"}, stubStats{}))
	defer rec.Detach()

	rec.JobDropped("email")
	rec.JobDropped("email")
	assert.Equal(t, int64(2), c.sum("adaptivepool.job.dropped", PoolNameKey.String("orders"), JobKindKey.String("email")))
}

func TestRejections(t *testing.T) {
	c, mp := newCollector(t)
	p := newPool(t, adaptivepool.WithName("orders"), adaptivepool.WithMinWorkers(1), adaptivepool.WithMaxWorkers(1),
		adaptivepool.WithQueueSize(1), adaptivepool.WithRecorder(New(mp)))

	release := make(chan struct{})
	defer close(release)
	task := adaptivepool.Task{Kind: "email", Run: func(context.Context) error { <-release; return nil }}
	ctx := context.Background()
	require.NoError(t, p.TrySubmit(ctx, task))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.TrySubmit(ctx, task)) // fills the queue

	assert.ErrorIs(t, p.TrySubmit(ctx, task), adaptivepool.ErrQueueFull)
	assert.Equal(t, int64(1), c.sum("adaptivepool.job.rejections",
		PoolNameKey.String("orders"), JobKindKey.String("email"), RejectionReasonKey.String("queue_full")))
}

func TestRecorderServesOnePoolAtATime(t *testing.T) {
	_, mp := newCollector(t)
	rec := New(mp)
	first, err := adaptivepool.New(adaptivepool.WithRecorder(rec), adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)

	_, err = adaptivepool.New(adaptivepool.WithRecorder(rec))
	require.ErrorIs(t, err, ErrAlreadyAttached)

	require.NoError(t, first.Shutdown(context.Background()))
	newPool(t, adaptivepool.WithRecorder(rec))
}

func TestDetachStopsGauges(t *testing.T) {
	c, mp := newCollector(t)
	p, err := adaptivepool.New(adaptivepool.WithName("orders"), adaptivepool.WithRecorder(New(mp)),
		adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	_, ok := c.metric("adaptivepool.worker.count")
	require.True(t, ok)

	require.NoError(t, p.Shutdown(context.Background()))
	_, ok = c.metric("adaptivepool.worker.count")
	assert.False(t, ok, "the gauge callback must be unregistered on shutdown")
}

func attachedRecorder(tb testing.TB) *Recorder {
	tb.Helper()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	rec := New(mp)
	p, err := adaptivepool.New(adaptivepool.WithRecorder(rec), adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		_ = p.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	return rec
}

func TestRecordingDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector adds allocations")
	}
	rec := attachedRecorder(t)
	record := func() {
		rec.JobSubmitted("email")
		rec.JobCompleted("email", adaptivepool.StatusSuccess, 2, time.Millisecond, time.Millisecond)
	}
	record() // populate the option caches
	assert.Zero(t, testing.AllocsPerRun(1000, record))
}

func BenchmarkJobCompleted(b *testing.B) {
	rec := attachedRecorder(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec.JobCompleted("email", adaptivepool.StatusSuccess, 1, time.Millisecond, time.Millisecond)
		}
	})
}

type stubStats struct{}

func (stubStats) QueueDepth() int              { return 0 }
func (stubStats) ActiveWorkers() int           { return 0 }
func (stubStats) BusyWorkers() int             { return 0 }
func (stubStats) JobsProcessed() int64         { return 0 }
func (stubStats) JobsFailed() int64            { return 0 }
func (stubStats) JobsRejected() int64          { return 0 }
func (stubStats) JobsDropped() int64           { return 0 }
func (stubStats) JobsRetried() int64           { return 0 }
func (stubStats) RetryAttempts() int64         { return 0 }
func (stubStats) AvgJobLatency() time.Duration { return 0 }
func (stubStats) AvgQueueWait() time.Duration  { return 0 }

// Regression: Attach rewrote the recorder's instruments without
// synchronization while events from a previous pool could still read them
// (run with -race).
func TestAttachDetachWhileRecording(t *testing.T) {
	_, mp := newCollector(t)
	rec := New(mp)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec.JobSubmitted("k")
				rec.JobRejected("k", adaptivepool.RejectTimeout)
				rec.JobCompleted("k", adaptivepool.StatusSuccess, 2, time.Millisecond, time.Millisecond)
				rec.ScaledUp(1)
			}
		})
	}
	for range 100 {
		require.NoError(t, rec.Attach(adaptivepool.PoolInfo{Name: "p", MaxWorkers: 1, QueueSize: 1}, stubStats{}))
		rec.Detach()
	}
	close(stop)
	wg.Wait()
}

func TestEventsAfterDetachAreDropped(t *testing.T) {
	_, mp := newCollector(t)
	rec := New(mp)
	require.NoError(t, rec.Attach(adaptivepool.PoolInfo{}, stubStats{}))
	rec.Detach()
	rec.Detach() // idempotent

	rec.JobSubmitted("k")
	rec.JobRejected("k", adaptivepool.RejectTimeout)
	rec.JobCompleted("k", adaptivepool.StatusSuccess, 1, 0, 0)
	rec.ScaledUp(1)
	assert.Nil(t, rec.state.Load())
}
