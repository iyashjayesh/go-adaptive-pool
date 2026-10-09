package adaptivepool

import (
	"context"
	"sync"
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

// Regression: a Submit blocked on a full queue panicked with "send on
// closed channel" when Shutdown closed the queue.
func TestShutdownWakesBlockedSubmit(t *testing.T) {
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(1), WithLogger(discardLogger()))
	require.NoError(t, err)

	release := make(chan struct{})
	slow := func(context.Context) error { //nolint:unparam
		<-release
		return nil
	}
	require.NoError(t, p.Submit(context.Background(), slow)) // occupies the worker
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.Submit(context.Background(), slow)) // fills the queue

	blocked := make(chan error, 1)
	go func() { blocked <- p.Submit(context.Background(), slow) }()
	time.Sleep(20 * time.Millisecond) // let it block on the full queue

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- p.Shutdown(context.Background()) }()

	select {
	case err := <-blocked:
		assert.ErrorIs(t, err, ErrPoolShutdown)
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Submit was not woken by Shutdown")
	}
	close(release)
	require.NoError(t, <-shutdownDone)
}

// Regression: Submits racing with Shutdown must never panic, and every job
// whose Submit returned nil must still run during the graceful drain.
func TestSubmitConcurrentWithShutdown(t *testing.T) {
	for range 20 {
		p, err := New(WithMinWorkers(2), WithMaxWorkers(4), WithQueueSize(8), WithLogger(discardLogger()))
		require.NoError(t, err)

		var accepted, executed atomic.Int64
		job := func(context.Context) error { //nolint:unparam
			executed.Add(1)
			return nil
		}

		var submitters sync.WaitGroup
		for range 16 {
			submitters.Go(func() {
				for {
					if err := p.Submit(context.Background(), job); err != nil {
						assert.ErrorIs(t, err, ErrPoolShutdown)
						return
					}
					accepted.Add(1)
				}
			})
		}

		time.Sleep(5 * time.Millisecond)
		require.NoError(t, p.Shutdown(context.Background()))
		submitters.Wait()
		assert.Equal(t, accepted.Load(), executed.Load(), "accepted jobs must not be dropped")
	}
}
