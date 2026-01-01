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
