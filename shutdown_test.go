package adaptivepool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownGraceful(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(4),
		WithQueueSize(10),
	)
	require.NoError(t, err)

	// Submit some jobs
	var completed atomic.Int32
	numJobs := 10

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Shutdown with sufficient timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = pool.Shutdown(ctx)
	assert.NoError(t, err)

	// All jobs should have completed
	assert.Equal(t, int32(numJobs), completed.Load())
	assert.Equal(t, int64(numJobs), pool.Metrics().JobsProcessed())
}

func TestShutdownWithTimeout(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(2),
		WithQueueSize(10),
	)
	require.NoError(t, err)

	// Submit long-running jobs
	blockChan := make(chan struct{})
	numJobs := 5

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			<-blockChan
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Shutdown with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = pool.Shutdown(ctx)
	assert.Error(t, err)
	assert.Equal(t, context.DeadlineExceeded, err)

	// Cleanup
	close(blockChan)
}

func TestShutdownIdempotent(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(2),
		WithQueueSize(10),
	)
	require.NoError(t, err)

	// First shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = pool.Shutdown(ctx)
	assert.NoError(t, err)

	// Second shutdown should also succeed (idempotent)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	err = pool.Shutdown(ctx2)
	assert.NoError(t, err)
}

func TestShutdownNoJobs(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(4),
		WithQueueSize(10),
	)
	require.NoError(t, err)

	// Shutdown immediately without Submitting jobs
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = pool.Shutdown(ctx)
	assert.NoError(t, err)

	// No jobs should be processed
	assert.Equal(t, int64(0), pool.Metrics().JobsProcessed())
}

func TestShutdownWithQueuedJobs(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(1),
		WithQueueSize(20),
	)
	require.NoError(t, err)

	// Submit many jobs that will queue up
	var completed atomic.Int32
	numJobs := 20

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			time.Sleep(20 * time.Millisecond)
			completed.Add(1)
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Give some time for jobs to start
	time.Sleep(100 * time.Millisecond)

	// Shutdown with sufficient timeout to drain queue
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = pool.Shutdown(ctx)
	assert.NoError(t, err)

	// All jobs should have completed
	assert.Equal(t, int32(numJobs), completed.Load())
}

func TestShutdownConcurrent(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(4),
		WithQueueSize(10),
	)
	require.NoError(t, err)

	// Submit a job
	job := func(_ context.Context) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	}
	_ = pool.Submit(context.Background(), job)

	// Try to shutdown concurrently from multiple goroutines
	numGoroutines := 5
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errors <- pool.Shutdown(ctx)
		}()
	}

	// All shutdowns should succeed (idempotent)
	for i := 0; i < numGoroutines; i++ {
		err := <-errors
		assert.NoError(t, err)
	}
}
