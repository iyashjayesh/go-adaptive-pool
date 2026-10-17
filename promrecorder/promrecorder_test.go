package promrecorder

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
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

func TestRegistrationAndUnregistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	p, err := adaptivepool.New(
		adaptivepool.WithName("emails"),
		adaptivepool.WithMinWorkers(2), adaptivepool.WithMaxWorkers(4), adaptivepool.WithQueueSize(10),
		adaptivepool.WithLogger(slog.New(slog.DiscardHandler)),
		adaptivepool.WithRecorder(New(reg)),
	)
	require.NoError(t, err)

	expected := `
# HELP adaptivepool_active_workers Current number of active workers
# TYPE adaptivepool_active_workers gauge
adaptivepool_active_workers{pool="emails"} 2
# HELP adaptivepool_max_workers Configured maximum number of workers
# TYPE adaptivepool_max_workers gauge
adaptivepool_max_workers{pool="emails"} 4
# HELP adaptivepool_queue_capacity Capacity of the job queue
# TYPE adaptivepool_queue_capacity gauge
adaptivepool_queue_capacity{pool="emails"} 10
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"adaptivepool_active_workers", "adaptivepool_max_workers", "adaptivepool_queue_capacity"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, p.Shutdown(ctx))

	families, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, families, "metrics should be unregistered on shutdown")
}

func TestDuplicatePoolNameFailsNew(t *testing.T) {
	reg := prometheus.NewRegistry()
	newPool(t, adaptivepool.WithName("a"), adaptivepool.WithRecorder(New(reg)))

	_, err := adaptivepool.New(adaptivepool.WithName("a"), adaptivepool.WithRecorder(New(reg)))
	require.Error(t, err)

	// A distinct name can share the registry
	newPool(t, adaptivepool.WithName("b"), adaptivepool.WithRecorder(New(reg)))
}

func TestRecorderServesOnePoolAtATime(t *testing.T) {
	rec := New(prometheus.NewRegistry())
	first, err := adaptivepool.New(adaptivepool.WithRecorder(rec), adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)

	_, err = adaptivepool.New(adaptivepool.WithRecorder(rec))
	require.ErrorIs(t, err, ErrAlreadyAttached)

	// Once the first pool shuts down, the recorder can be reused
	require.NoError(t, first.Shutdown(context.Background()))
	newPool(t, adaptivepool.WithRecorder(rec))
}

func TestJobOutcomesByKind(t *testing.T) {
	reg := prometheus.NewRegistry()
	p := newPool(t, adaptivepool.WithRecorder(New(reg)))

	ctx := context.Background()
	ok := func(context.Context) error { return nil }
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "email", Run: ok}))
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "email", Run: func(context.Context) error { return errors.New("x") }}))
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "resize", Run: func(context.Context) error { panic("x") }}))
	require.NoError(t, p.Submit(ctx, ok))

	require.Eventually(t, func() bool { return p.Metrics().JobsProcessed() == 4 }, 2*time.Second, 10*time.Millisecond)

	expected := `
# HELP adaptivepool_jobs_completed_total Jobs that finished executing, by final status
# TYPE adaptivepool_jobs_completed_total counter
adaptivepool_jobs_completed_total{kind="default",status="success"} 1
adaptivepool_jobs_completed_total{kind="email",status="error"} 1
adaptivepool_jobs_completed_total{kind="email",status="success"} 1
adaptivepool_jobs_completed_total{kind="resize",status="panic"} 1
# HELP adaptivepool_jobs_submitted_total Jobs accepted into the queue
# TYPE adaptivepool_jobs_submitted_total counter
adaptivepool_jobs_submitted_total{kind="default"} 1
adaptivepool_jobs_submitted_total{kind="email"} 2
adaptivepool_jobs_submitted_total{kind="resize"} 1
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"adaptivepool_jobs_completed_total", "adaptivepool_jobs_submitted_total"))
	assert.Equal(t, 3, testutil.CollectAndCount(reg, "adaptivepool_job_duration_seconds"))
}

func TestRejectionReasons(t *testing.T) {
	rec := New(prometheus.NewRegistry())
	p := newPool(t, adaptivepool.WithMinWorkers(1), adaptivepool.WithMaxWorkers(1), adaptivepool.WithQueueSize(1),
		adaptivepool.WithRecorder(rec))

	release := make(chan struct{})
	defer close(release)
	task := adaptivepool.Task{Kind: "email", Run: func(context.Context) error { <-release; return nil }}
	ctx := context.Background()
	require.NoError(t, p.TrySubmit(ctx, task))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.TrySubmit(ctx, task)) // fills the queue

	for range 2 {
		assert.ErrorIs(t, p.TrySubmit(ctx, task), adaptivepool.ErrQueueFull)
	}
	sms := task
	sms.Kind = "sms"
	assert.ErrorIs(t, p.TrySubmit(ctx, sms), adaptivepool.ErrQueueFull)

	s := rec.state.Load()
	queueFull := string(adaptivepool.RejectQueueFull)
	assert.InDelta(t, 2, testutil.ToFloat64(s.jobsRejected.WithLabelValues("email", queueFull)), 0)
	assert.InDelta(t, 1, testutil.ToFloat64(s.jobsRejected.WithLabelValues("sms", queueFull)), 0)
	assert.Equal(t, 2, testutil.CollectAndCount(s.jobsSubmitted), "every kind seen exposes its series, at zero until used")
}

func TestRetryMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	results := make(chan adaptivepool.TaskResult, 2)
	p := newPool(t,
		adaptivepool.WithMinWorkers(1), adaptivepool.WithMaxWorkers(1),
		adaptivepool.WithRecorder(New(reg)),
		adaptivepool.WithRetry(adaptivepool.RetryPolicy{MaxAttempts: 3, Backoff: adaptivepool.ConstantBackoff{}}),
		adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) { results <- r }),
	)

	errTransient := errors.New("transient")
	failedOnce := false
	ctx := context.Background()
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "sync", Run: func(context.Context) error {
		if !failedOnce {
			failedOnce = true
			return errTransient
		}
		return nil
	}}))
	require.NoError(t, p.SubmitTask(ctx, adaptivepool.Task{Kind: "sync", Run: func(context.Context) error { return errTransient }}))
	<-results
	<-results

	expected := `
# HELP adaptivepool_jobs_completed_total Jobs that finished executing, by final status
# TYPE adaptivepool_jobs_completed_total counter
adaptivepool_jobs_completed_total{kind="sync",status="retries_exhausted"} 1
adaptivepool_jobs_completed_total{kind="sync",status="success"} 1
# HELP adaptivepool_jobs_retried_total Jobs that needed at least one retry
# TYPE adaptivepool_jobs_retried_total counter
adaptivepool_jobs_retried_total{kind="sync"} 2
# HELP adaptivepool_retry_attempts_total Retries performed across all jobs
# TYPE adaptivepool_retry_attempts_total counter
adaptivepool_retry_attempts_total{kind="sync"} 3
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"adaptivepool_jobs_completed_total", "adaptivepool_jobs_retried_total", "adaptivepool_retry_attempts_total"))
}

func attachedRecorder(tb testing.TB) *Recorder {
	tb.Helper()
	rec := New(prometheus.NewRegistry())
	p, err := adaptivepool.New(adaptivepool.WithRecorder(rec), adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return rec
}

func TestJobCompletedDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector adds allocations")
	}
	rec := attachedRecorder(t)
	rec.JobCompleted("email", adaptivepool.StatusSuccess, 2, time.Millisecond, time.Millisecond) // create series
	allocs := testing.AllocsPerRun(1000, func() {
		rec.JobCompleted("email", adaptivepool.StatusSuccess, 2, time.Millisecond, time.Millisecond)
	})
	assert.Zero(t, allocs)
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

// A reused recorder must not keep series from the previous pool's vectors
func TestReusedRecorderReportsToNewPool(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := New(reg)
	run := func() {
		p, err := adaptivepool.New(adaptivepool.WithRecorder(rec), adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
		require.NoError(t, err)
		require.NoError(t, p.SubmitTask(context.Background(), adaptivepool.Task{Kind: "email", Run: func(context.Context) error { return nil }}))
		require.Eventually(t, func() bool { return p.Metrics().JobsProcessed() == 1 }, 2*time.Second, time.Millisecond)
		assert.InDelta(t, 1, testutil.ToFloat64(rec.state.Load().jobsSubmitted.WithLabelValues("email")), 0)
		assert.Equal(t, 1, testutil.CollectAndCount(reg, "adaptivepool_jobs_submitted_total"))
		require.NoError(t, p.Shutdown(context.Background()))
	}
	run()
	run() // counts start from zero against the new pool's collectors
}

// stubStats satisfies adaptivepool.Metrics without a pool
type stubStats struct{}

func (stubStats) QueueDepth() int              { return 0 }
func (stubStats) ActiveWorkers() int           { return 0 }
func (stubStats) BusyWorkers() int             { return 0 }
func (stubStats) JobsProcessed() int64         { return 0 }
func (stubStats) JobsFailed() int64            { return 0 }
func (stubStats) JobsRejected() int64          { return 0 }
func (stubStats) JobsRetried() int64           { return 0 }
func (stubStats) RetryAttempts() int64         { return 0 }
func (stubStats) AvgJobLatency() time.Duration { return 0 }
func (stubStats) AvgQueueWait() time.Duration  { return 0 }

// Regression: Attach rewrote the recorder's collectors without
// synchronization while events from a previous pool could still read them
// (run with -race).
func TestAttachDetachWhileRecording(t *testing.T) {
	rec := New(prometheus.NewRegistry())
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
	rec := New(prometheus.NewRegistry())
	require.NoError(t, rec.Attach(adaptivepool.PoolInfo{}, stubStats{}))
	rec.Detach()
	rec.Detach() // idempotent

	rec.JobSubmitted("k")
	rec.JobRejected("k", adaptivepool.RejectTimeout)
	rec.JobCompleted("k", adaptivepool.StatusSuccess, 1, 0, 0)
	rec.ScaledUp(1)
	assert.Nil(t, rec.state.Load())
}
