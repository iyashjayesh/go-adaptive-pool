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

func TestScalingUp(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(8),
		WithQueueSize(20),
		WithScaleUpThreshold(0.5), // Scale up at 50% queue utilization
		WithScaleCooldown(100*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	initialWorkers := pool.Metrics().ActiveWorkers()
	assert.Equal(t, 2, initialWorkers)

	// Submit many jobs to trigger scaling
	blockChan := make(chan struct{})
	numJobs := 15 // More than 50% of queue

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			<-blockChan
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Wait for scaling to occur
	time.Sleep(2 * time.Second)

	// Should have scaled up
	currentWorkers := pool.Metrics().ActiveWorkers()
	assert.Greater(t, currentWorkers, initialWorkers, "workers should have scaled up")

	// Cleanup
	close(blockChan)
}

func TestScalingDown(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(8),
		WithQueueSize(20),
		WithScaleDownIdleDuration(500*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Manually spawn extra workers by Submitting jobs
	blockChan := make(chan struct{})
	numJobs := 10

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			<-blockChan
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Wait for potential scale-up
	time.Sleep(2 * time.Second)
	workersAfterLoad := pool.Metrics().ActiveWorkers()

	// unblocking jobs
	close(blockChan)

	// Wait for jobs to complete and workers to idle
	time.Sleep(2 * time.Second)

	// Workers should scale down towards minimum (but may not reach exactly min)
	currentWorkers := pool.Metrics().ActiveWorkers()
	assert.LessOrEqual(t, currentWorkers, workersAfterLoad, "workers should scale down or stay same")
}

func TestScalingRespectLimits(t *testing.T) {
	minWorkers := 2
	maxWorkers := 4

	pool, err := New(
		WithMinWorkers(minWorkers),
		WithMaxWorkers(maxWorkers),
		WithQueueSize(50),
		WithScaleUpThreshold(0.3),
		WithScaleCooldown(100*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit many jobs to try to trigger maximum scaling
	blockChan := make(chan struct{})
	numJobs := 40

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			<-blockChan
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Wait for scaling
	time.Sleep(3 * time.Second)

	// Should not exceed max workers
	currentWorkers := pool.Metrics().ActiveWorkers()
	assert.LessOrEqual(t, currentWorkers, maxWorkers, "should not exceed max workers")
	assert.GreaterOrEqual(t, currentWorkers, minWorkers, "should not go below min workers")

	// Cleanup
	close(blockChan)

	// Wait for scale down
	time.Sleep(2 * time.Second)

	// Should not go below min workers
	finalWorkers := pool.Metrics().ActiveWorkers()
	assert.GreaterOrEqual(t, finalWorkers, minWorkers, "should not go below min workers")
}

func TestScalingCooldown(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(10),
		WithQueueSize(30),
		WithScaleUpThreshold(0.5),
		WithScaleCooldown(2*time.Second), // Long cooldown
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit jobs to trigger scaling
	blockChan := make(chan struct{})
	numJobs := 20

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			<-blockChan
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Wait briefly
	time.Sleep(1500 * time.Millisecond)

	workersAfterFirstCheck := pool.Metrics().ActiveWorkers()

	// Wait for cooldown to expire
	time.Sleep(1 * time.Second)

	// Should potentially scale more after cooldown
	workersAfterCooldown := pool.Metrics().ActiveWorkers()

	// At minimum, workers should not have decreased
	assert.GreaterOrEqual(t, workersAfterCooldown, workersAfterFirstCheck)

	// Cleanup
	close(blockChan)
}

func TestScalingUnderLoad(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(16),
		WithQueueSize(100),
		WithScaleUpThreshold(0.6),
		WithScaleCooldown(200*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Simulate sustained load
	var completed atomic.Int32
	numJobs := 100

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
			return nil
		}

		err = pool.Submit(context.Background(), job)
		require.NoError(t, err)
	}

	// Monitor worker count during execution
	maxWorkersSeen := 0
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		workers := pool.Metrics().ActiveWorkers()
		if workers > maxWorkersSeen {
			maxWorkersSeen = workers
		}
	}

	// Should have scaled up under load
	assert.Greater(t, maxWorkersSeen, 2, "should have scaled up under sustained load")

	// Wait for all jobs to complete
	assert.Eventually(t, func() bool {
		return int(completed.Load()) == numJobs
	}, 10*time.Second, 100*time.Millisecond)
}
