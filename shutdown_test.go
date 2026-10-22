package adaptivepool

import (
	"context"
	"fmt"
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

	// One job is running; the other four are still queued and get dropped
	err = pool.Shutdown(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	var se *ShutdownError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, numJobs-1, se.Dropped)

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

// Regression: select picks randomly when forceStop and the queue are both
// ready, so a worker that finished its job after a forced shutdown could
// still start queued ones.
func TestForcedShutdownDoesNotStartQueuedJobs(t *testing.T) {
	for range 20 {
		var started atomic.Int64
		release := make(chan struct{})
		p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(50), WithLogger(discardLogger()))
		require.NoError(t, err)

		require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
			<-release
			return nil
		}))
		require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
		for range 50 {
			require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
				started.Add(1)
				return nil
			}))
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		go func() {
			<-ctx.Done()
			time.Sleep(20 * time.Millisecond) // forceStop is closed by now
			close(release)
		}()
		var se *ShutdownError
		require.ErrorAs(t, p.Shutdown(ctx), &se)
		cancel()
		assert.Zero(t, started.Load(), "queued jobs started after a forced shutdown")
		assert.Equal(t, 50, se.Dropped)
	}
}

// A forced shutdown reports each queued job it drops through OnTaskDone, the
// recorder and Shutdown's error, so callers can tell that work was lost.
func TestForcedShutdownReportsDroppedJobs(t *testing.T) {
	const queued = 10
	release := make(chan struct{})
	rec := &fakeRecorder{}
	var mu sync.Mutex
	var results []TaskResult
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(queued),
		WithLogger(discardLogger()), WithRecorder(rec),
		WithOnTaskDone(func(r TaskResult) {
			mu.Lock()
			defer mu.Unlock()
			results = append(results, r)
		}))
	require.NoError(t, err)

	bg := context.Background()
	require.NoError(t, p.SubmitTask(bg, Task{ID: "running", Kind: "k", Run: func(context.Context) error {
		<-release
		return nil
	}}))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	for i := range queued {
		require.NoError(t, p.SubmitTask(bg, Task{ID: fmt.Sprint(i), Kind: "k", Run: func(context.Context) error { return nil }}))
	}

	ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	go func() {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // forceStop is closed by now
		close(release)
	}()
	err = p.Shutdown(ctx)

	var se *ShutdownError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, queued, se.Dropped)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorIs(t, p.Shutdown(bg), context.DeadlineExceeded, "a later call should see the same result")
	assert.Equal(t, int64(queued), p.Metrics().JobsDropped())
	assert.Equal(t, int64(1), p.Metrics().JobsProcessed(), "dropped jobs aren't processed")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, results, queued+1)
	for _, r := range results {
		if r.ID == "running" {
			assert.Equal(t, StatusSuccess, r.Status)
			continue
		}
		assert.Equal(t, StatusShutdown, r.Status)
		assert.Zero(t, r.Attempts)
		assert.ErrorIs(t, r.Err, ErrPoolShutdown)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Len(t, rec.dropped, queued)
	assert.Len(t, rec.completed, 1)
}

// Regression: a zero-length backoff skipped the forceStop check, so the retry
// loop kept running at full speed after a forced shutdown.
func TestForcedShutdownStopsZeroDelayRetries(t *testing.T) {
	var attempts atomic.Int64
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithLogger(discardLogger()),
		WithRetry(RetryPolicy{MaxAttempts: 1 << 30, Backoff: ConstantBackoff{}}))
	require.NoError(t, err)
	require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
		attempts.Add(1)
		return errTransient
	}))
	require.Eventually(t, func() bool { return attempts.Load() > 0 }, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.NoError(t, p.Shutdown(ctx), "the worker should stop within the grace period")
	assert.Less(t, time.Since(start), 900*time.Millisecond)

	after := attempts.Load()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, after, attempts.Load(), "retries kept running after Shutdown returned")
}

// Regression: Detach ran when the grace period ended even if workers were
// still running and reporting to the recorder.
func TestRecorderDetachedAfterWorkersExit(t *testing.T) {
	rec := &fakeRecorder{}
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithLogger(discardLogger()), WithRecorder(rec))
	require.NoError(t, err)
	require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
		time.Sleep(1300 * time.Millisecond) // outlives the 1s grace period
		return nil
	}))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, p.Shutdown(ctx), context.DeadlineExceeded)

	rec.mu.Lock()
	assert.False(t, rec.detached, "detached while a worker was still running")
	rec.mu.Unlock()

	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.detached
	}, 3*time.Second, 10*time.Millisecond)
	assert.Len(t, rec.completedEvents(), 1, "the late job should be reported before Detach")
}

// Regression: a second Shutdown returned nil even when the first had timed
// out, because only the call that ran the shutdown kept its result.
func TestLaterShutdownReportsFirstResult(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithLogger(discardLogger()))
	require.NoError(t, err)
	require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
		<-release
		return nil
	}))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, p.Shutdown(ctx), context.DeadlineExceeded) // the worker is stuck

	assert.ErrorIs(t, p.Shutdown(context.Background()), context.DeadlineExceeded,
		"a later call should report that the shutdown timed out")
	expired, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	assert.ErrorIs(t, p.Shutdown(expired), context.DeadlineExceeded,
		"a finished shutdown's result wins over the caller's own context")
}

// Regression: sync.Once made a later Shutdown wait for the first to finish
// whatever its own context said.
func TestLaterShutdownHonoursItsOwnContext(t *testing.T) {
	release := make(chan struct{})
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithLogger(discardLogger()))
	require.NoError(t, err)
	require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
		<-release
		return nil
	}))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)

	first := make(chan error, 1)
	go func() { first <- p.Shutdown(context.Background()) }() // blocked on the running job
	require.Eventually(t, func() bool { return p.(*pool).state.isShutdown() }, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	later := make(chan error, 1)
	go func() { later <- p.Shutdown(ctx) }()
	select {
	case err := <-later:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("a later Shutdown ignored its own context")
	}

	close(release)
	require.NoError(t, <-first)
}
