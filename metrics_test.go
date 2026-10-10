package adaptivepool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	p, err := New(WithName("emails"), WithMinWorkers(2), WithMaxWorkers(4), WithQueueSize(10), WithPrometheusRegisterer(reg))
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

func TestPrometheusDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	newTestPool(t, WithName("a"), WithPrometheusRegisterer(reg))

	_, err := New(WithName("a"), WithPrometheusRegisterer(reg))
	require.Error(t, err)

	// A distinct name can share the registry
	newTestPool(t, WithName("b"), WithPrometheusRegisterer(reg))
}

func TestMetricsDisabledRegistersNothing(t *testing.T) {
	reg := prometheus.NewRegistry()
	newTestPool(t, WithMetricsEnabled(false), WithPrometheusRegisterer(reg))

	families, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, families)
}

func TestPrometheusJobOutcomesByKind(t *testing.T) {
	reg := prometheus.NewRegistry()
	p := newTestPool(t, WithPrometheusRegisterer(reg), WithLogger(discardLogger()))

	ctx := context.Background()
	require.NoError(t, p.SubmitTask(ctx, Task{Kind: "email", Run: func(context.Context) error { return nil }}))
	require.NoError(t, p.SubmitTask(ctx, Task{Kind: "email", Run: func(context.Context) error { return errors.New("x") }}))
	require.NoError(t, p.SubmitTask(ctx, Task{Kind: "resize", Run: func(context.Context) error { panic("x") }}))
	require.NoError(t, p.Submit(ctx, func(context.Context) error { return nil }))

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
	assert.Equal(t, int64(2), p.Metrics().JobsFailed())
}

func TestPrometheusRejectionReasons(t *testing.T) {
	reg := prometheus.NewRegistry()
	p, err := New(WithPrometheusRegisterer(reg), WithLogger(discardLogger()))
	require.NoError(t, err)

	// Collect from the pool's own collectors since Shutdown unregisters them
	rec := p.Metrics().(*metrics).rec.(*promRecorder)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, p.Shutdown(ctx))

	assert.ErrorIs(t, p.SubmitTask(context.Background(), Task{Kind: "email", Run: func(context.Context) error { return nil }}), ErrPoolShutdown)
	assert.InDelta(t, 1, testutil.ToFloat64(rec.jobsRejected.WithLabelValues("email", string(rejectShutdown))), 0)
	assert.Equal(t, int64(1), p.Metrics().JobsRejected())
}

func TestBusyWorkers(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(2), WithMaxWorkers(2))

	release := make(chan struct{})
	for range 2 {
		require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
			<-release
			return nil
		}))
	}
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 2 }, 2*time.Second, 10*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Positive(t, p.Metrics().AvgQueueWait()+p.Metrics().AvgJobLatency())
}
