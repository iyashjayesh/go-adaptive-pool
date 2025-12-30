package adaptivepool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewPool(t *testing.T) {
	tests := []struct {
		name    string
		options []Option
		wantErr bool
	}{
		{
			name:    "default configuration",
			options: nil,
			wantErr: false,
		},
		{
			name: "custom configuration",
			options: []Option{
				WithMinWorkers(2),
				WithMaxWorkers(10),
				WithQueueSize(500),
			},
			wantErr: false,
		},
		{
			name: "invalid min workers",
			options: []Option{
				WithMinWorkers(0),
			},
			wantErr: true,
		},
		{
			name: "invalid max workers",
			options: []Option{
				WithMinWorkers(10),
				WithMaxWorkers(5),
			},
			wantErr: true,
		},
		{
			name: "invalid queue size",
			options: []Option{
				WithQueueSize(0),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, err := New(tt.options...)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, pool)
			} else {
				require.NoError(t, err)
				require.NotNil(t, pool)

				// Clean shutdown
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err = pool.Shutdown(ctx)
				assert.NoError(t, err)
			}
		})
	}
}

func TestPoolSubmitAndExecute(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(4),
		WithQueueSize(10),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit a simple job
	var executed atomic.Bool
	job := func(_ context.Context) error {
		executed.Store(true)
		return nil
	}

	ctx := context.Background()
	err = pool.Submit(ctx, job)
	require.NoError(t, err)

	// Wait for job to execute
	time.Sleep(100 * time.Millisecond)
	assert.True(t, executed.Load())
}

func TestPoolSubmitMultipleJobs(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(8),
		WithQueueSize(100),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit multiple jobs
	numJobs := 50
	var completed atomic.Int32

	for i := 0; i < numJobs; i++ {
		job := func(_ context.Context) error {
			time.Sleep(10 * time.Millisecond)
			completed.Add(1)
			return nil
		}

		ctx := context.Background()
		err = pool.Submit(ctx, job)
		require.NoError(t, err)
	}

	// Wait for all jobs to complete
	assert.Eventually(t, func() bool {
		return int(completed.Load()) == numJobs
	}, 5*time.Second, 100*time.Millisecond)
}

func TestPoolJobWithError(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(2),
		WithQueueSize(10),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit a job that returns an error
	var executed atomic.Bool
	job := func(_ context.Context) error {
		executed.Store(true)
		return errors.New("job error")
	}

	ctx := context.Background()
	err = pool.Submit(ctx, job)
	require.NoError(t, err)

	// Wait for job to execute
	time.Sleep(100 * time.Millisecond)
	assert.True(t, executed.Load())

	// Pool should still be functional
	assert.Greater(t, pool.Metrics().JobsProcessed(), int64(0))
}

func TestPoolJobPanic(t *testing.T) {
	pool, err := New(
		WithMinWorkers(1),
		WithMaxWorkers(2),
		WithQueueSize(10),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	// Submit a job that panics
	job := func(_ context.Context) error {
		panic("job panic")
	}

	ctx := context.Background()
	err = pool.Submit(ctx, job)
	require.NoError(t, err)

	// Wait for job to execute
	time.Sleep(100 * time.Millisecond)

	// Pool should still be functional after panic
	assert.Greater(t, pool.Metrics().JobsProcessed(), int64(0))

	// Submit another job to Verify pool is still working
	var executed atomic.Bool
	job2 := func(_ context.Context) error {
		executed.Store(true)
		return nil
	}

	err = pool.Submit(ctx, job2)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	assert.True(t, executed.Load())
}

func TestPoolMetrics(t *testing.T) {
	pool, err := New(
		WithMinWorkers(2),
		WithMaxWorkers(4),
		WithQueueSize(10),
	)
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	metrics := pool.Metrics()
	assert.NotNil(t, metrics)

	// Initial state
	assert.Equal(t, 2, metrics.ActiveWorkers()) // min workers
	assert.Equal(t, int64(0), metrics.JobsProcessed())
	assert.Equal(t, int64(0), metrics.JobsRejected())

	// Submit a job
	job := func(_ context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	ctx := context.Background()
	err = pool.Submit(ctx, job)
	require.NoError(t, err)

	// Wait for job to complete
	time.Sleep(200 * time.Millisecond)

	assert.Greater(t, metrics.JobsProcessed(), int64(0))
	assert.Greater(t, metrics.AvgJobLatency(), time.Duration(0))
}
