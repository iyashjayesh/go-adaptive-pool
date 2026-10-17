// Package adaptivepool provides a production-grade adaptive worker pool for Go.
package adaptivepool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackpressureQueueFull(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(1),
		WithQueueSize(2),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// creating a blocking job
	blockChan := make(chan struct{})
	blockingJob := func(_ context.Context) error { //nolint:unparam
		<-blockChan
		return nil
	}

	// filling the queue and worker
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		err = pool.Submit(ctx, blockingJob)
		require.NoError(t, err)
	}

	// Next submission should block
	SubmitDone := make(chan struct{})
	go func() {
		ctx := context.Background()
		_ = pool.Submit(ctx, blockingJob)
		close(SubmitDone)
	}()

	// verifying it's blocking
	select {
	case <-SubmitDone:
		t.Fatal("expected submission to block")
	case <-time.After(100 * time.Millisecond):
		// Expected: submission is blocking
	}

	// unblocking jobs
	close(blockChan)

	// Now submission should complete
	select {
	case <-SubmitDone:
		// Expected: submission completed
	case <-time.After(2 * time.Second):
		t.Fatal("submission did not complete after unblockinging")
	}
}

func TestBackpressureContextTimeout(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(1),
		WithQueueSize(2),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// creating a blocking job
	blockChan := make(chan struct{})
	blockingJob := func(_ context.Context) error { //nolint:unparam
		<-blockChan
		return nil
	}

	// filling the queue and worker
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		err = pool.Submit(ctx, blockingJob)
		require.NoError(t, err)
	}

	// Submit with timeout should fail
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = pool.Submit(ctx, blockingJob)
	assert.Error(t, err)
	assert.Equal(t, ErrTimeout, err)

	// verifying rejected metric
	assert.Greater(t, pool.Metrics().JobsRejected(), int64(0))

	// Cleanup
	close(blockChan)
}

func TestBackpressureContextCancellation(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(1),
		WithQueueSize(2),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// creating a blocking job
	blockChan := make(chan struct{})
	blockingJob := func(_ context.Context) error { //nolint:unparam
		<-blockChan
		return nil
	}

	// filling the queue and worker
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		err = pool.Submit(ctx, blockingJob)
		require.NoError(t, err)
	}

	// Submit with cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Start submission in goroutine
	SubmitErr := make(chan error, 1)
	go func() {
		SubmitErr <- pool.Submit(ctx, blockingJob)
	}()

	// Cancel the context
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Should get cancellation error
	err = <-SubmitErr
	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)

	// Cleanup
	close(blockChan)
}

func TestSubmitAfterShutdown(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(2),
		WithQueueSize(10),
	)
	require.NoError(t, err)

	// Shutdown the pool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = pool.Shutdown(ctx)
	require.NoError(t, err)

	// Try to Submit after shutdown
	job := func(_ context.Context) error { //nolint:unparam
		return nil
	}

	err = pool.Submit(context.Background(), job)
	assert.Error(t, err)
	assert.Equal(t, ErrPoolShutdown, err)

	// verifying rejected metric
	assert.Greater(t, pool.Metrics().JobsRejected(), int64(0))
}

func TestConcurrentSubmissions(t *testing.T) {
	pool, err := New(
		WithMinWorkers(4),
		WithMaxWorkers(16),
		WithQueueSize(100),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit jobs concurrently from multiple goroutines
	numGoroutines := 10
	jobsPerGoroutine := 20
	var completed atomic.Int32

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < jobsPerGoroutine; j++ {
				job := func(_ context.Context) error { //nolint:unparam
					time.Sleep(5 * time.Millisecond)
					completed.Add(1)
					return nil
				}

				ctx := context.Background()
				_ = pool.Submit(ctx, job)
			}
		}()
	}

	// Wait for all jobs to complete
	expectedJobs := numGoroutines * jobsPerGoroutine
	assert.Eventually(t, func() bool {
		return int(completed.Load()) == expectedJobs
	}, 10*time.Second, 100*time.Millisecond)

	assert.Equal(t, int64(expectedJobs), pool.Metrics().JobsProcessed())
}

// Regression: with an already-expired submit context and Task.Context set,
// SubmitTask raced the queue send against ctx.Done() and rejected about half
// of submissions at random even though the queue had space.
func TestSubmitPrefersQueueSpaceOverDoneContext(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(1000), WithLogger(discardLogger()))

	release := make(chan struct{})
	defer close(release)
	task := Task{Context: context.Background(), Run: func(context.Context) error { //nolint:unparam
		<-release
		return nil
	}}

	for i := range 1000 {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		err := p.SubmitTask(ctx, task)
		cancel()
		require.NoError(t, err, "submit %d rejected with queue space available", i)
	}

	// The worker holds one job; refill its slot so the queue is full
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.SubmitTask(context.Background(), task))

	// Once the queue is full, a done context rejects immediately
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	assert.ErrorIs(t, p.SubmitTask(ctx, task), ErrTimeout)
}

// Regression: a done ctx that is also the job's context was queued, so the
// job started with a cancelled context and could still report success.
func TestSubmitRejectsDoneContextUsedAsJobContext(t *testing.T) {
	rec := &fakeRecorder{}
	p := newTestPool(t, WithLogger(discardLogger()), WithRecorder(rec))
	var ran atomic.Int64
	run := func(context.Context) error { ran.Add(1); return nil } //nolint:unparam

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithTimeout(context.Background(), 0)
	defer cancelExpired()

	for _, submit := range []func(context.Context) error{
		func(ctx context.Context) error { return p.Submit(ctx, run) },
		func(ctx context.Context) error { return p.SubmitTask(ctx, Task{Kind: "k", Run: run}) },
		func(ctx context.Context) error { return p.TrySubmit(ctx, Task{Kind: "k", Run: run}) },
	} {
		assert.ErrorIs(t, submit(canceled), context.Canceled)
		assert.ErrorIs(t, submit(expired), ErrTimeout)
	}

	time.Sleep(20 * time.Millisecond)
	assert.Zero(t, ran.Load(), "no job should have been queued")
	assert.Equal(t, int64(6), p.Metrics().JobsRejected())
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.submitted)
	assert.Equal(t, []string{
		"default/canceled", "default/timeout", "k/canceled", "k/timeout", "k/canceled", "k/timeout",
	}, rec.rejected)
}

func TestTrySubmitQueueFull(t *testing.T) {
	rec := &fakeRecorder{}
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(1), WithLogger(discardLogger()), WithRecorder(rec))

	release := make(chan struct{})
	defer close(release)
	block := func(context.Context) error { //nolint:unparam
		<-release
		return nil
	}

	ctx := context.Background()
	require.NoError(t, p.TrySubmit(ctx, Task{Run: block}))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.TrySubmit(ctx, Task{Run: block}))

	assert.ErrorIs(t, p.TrySubmit(ctx, Task{Kind: "email", Run: block}), ErrQueueFull)
	assert.Equal(t, int64(1), p.Metrics().JobsRejected())
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Equal(t, []string{"email/queue_full"}, rec.rejected)
}

func TestTrySubmitContext(t *testing.T) {
	p := newTestPool(t, WithLogger(discardLogger()))

	type key struct{}
	got := make(chan any, 2)
	run := func(ctx context.Context) error {
		got <- ctx.Value(key{})
		return nil
	}

	ctx := context.WithValue(context.Background(), key{}, "submit")
	require.NoError(t, p.TrySubmit(ctx, Task{Run: run}))
	assert.Equal(t, "submit", <-got)

	taskCtx := context.WithValue(context.Background(), key{}, "task")
	require.NoError(t, p.TrySubmit(ctx, Task{Run: run, Context: taskCtx}))
	assert.Equal(t, "task", <-got)
}

func TestTrySubmitErrors(t *testing.T) {
	p, err := New(WithLogger(discardLogger()))
	require.NoError(t, err)

	ctx := context.Background()
	assert.ErrorIs(t, p.TrySubmit(ctx, Task{}), ErrNilJob)
	require.NoError(t, p.Shutdown(ctx))
	assert.ErrorIs(t, p.TrySubmit(ctx, Task{Run: func(context.Context) error { return nil }}), ErrPoolShutdown)
}
