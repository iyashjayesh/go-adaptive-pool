package adaptivepool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock advances instantly on After and records every requested wait.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// failN returns a job that fails n times with err, then succeeds.
func failN(n int, err error) (Job, *int) {
	calls := 0
	return func(context.Context) error {
		calls++
		if calls <= n {
			return err
		}
		return nil
	}, &calls
}

var errTransient = errors.New("transient")

func (r *retrier) run(ctx context.Context, job Job) (int, error) {
	return r.execute(&jobWrapper{ctx: ctx, job: job})
}

func TestRetrySucceedsAfterFailures(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Backoff: ConstantBackoff{Delay: time.Second}, Clock: clock}, nil)

	job, calls := failN(2, errTransient)
	attempts, err := r.run(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.Equal(t, 3, *calls)
	assert.Equal(t, []time.Duration{time.Second, time.Second}, clock.Waits())
}

func TestRetryExhausted(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{MaxAttempts: 3, Clock: clock}, nil)

	job, calls := failN(10, errTransient)
	attempts, err := r.run(context.Background(), job)

	assert.Equal(t, 3, attempts)
	assert.Equal(t, 3, *calls)
	assert.ErrorIs(t, err, ErrRetriesExhausted)
	assert.ErrorIs(t, err, errTransient)
	assert.Equal(t, StatusRetriesExhausted, statusOf(err))

	var re *RetryError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, 3, re.Attempts)
	assert.Equal(t, []time.Duration{100 * time.Millisecond, 150 * time.Millisecond}, clock.Waits(), "default exponential backoff")
}

func TestRetrySingleAttempt(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{MaxAttempts: 1, Clock: clock}, nil)

	attempts, err := r.run(context.Background(), func(context.Context) error { return errTransient })
	assert.Equal(t, 1, attempts)
	assert.ErrorIs(t, err, ErrRetriesExhausted)
	assert.Empty(t, clock.Waits())
}

func TestRetryStopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Clock: newFakeClock()}, nil)

	attempts, err := r.run(ctx, func(context.Context) error {
		cancel()
		return context.Canceled
	})
	assert.Equal(t, 1, attempts, "context errors are not retried")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, StatusCanceled, statusOf(err))
}

func TestRetryWaitInterruptedByContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Backoff: ConstantBackoff{Delay: time.Hour}}, nil)

	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	attempts, err := r.run(ctx, func(context.Context) error { return errTransient })

	assert.Less(t, time.Since(start), time.Second, "backoff wait must abort on cancellation")
	assert.Equal(t, 1, attempts)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, errTransient)
}

func TestRetryDoesNotRetryPanics(t *testing.T) {
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Clock: newFakeClock()}, nil)

	attempts, err := r.run(context.Background(), func(context.Context) error { panic("bug") })
	assert.Equal(t, 1, attempts)
	var pe *PanicError
	assert.ErrorAs(t, err, &pe)
}

func TestRetryPolicyValidation(t *testing.T) {
	_, err := New(WithRetry(RetryPolicy{}))
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestPoolRetriesJobs(t *testing.T) {
	results := make(chan TaskResult, 1)
	p := newTestPool(t,
		WithLogger(discardLogger()),
		WithRetry(RetryPolicy{MaxAttempts: 3, Backoff: ConstantBackoff{Delay: time.Millisecond}}),
		WithOnTaskDone(func(r TaskResult) { results <- r }),
	)

	job, _ := failN(2, errTransient)
	require.NoError(t, p.SubmitTask(context.Background(), Task{ID: "t1", Run: job}))

	select {
	case r := <-results:
		assert.Equal(t, StatusSuccess, r.Status)
		assert.Equal(t, 3, r.Attempts)
	case <-time.After(2 * time.Second):
		t.Fatal("task did not finish")
	}
}

func TestRetryMaxDelayCapsBackoff(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{
		MaxAttempts: 5,
		Backoff:     ExponentialBackoff{Base: time.Second, Multiplier: 2},
		MaxDelay:    3 * time.Second,
		Clock:       clock,
	}, nil)

	_, _ = r.run(context.Background(), func(context.Context) error { return errTransient })
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second}, clock.Waits())
}

func TestRetryFullJitter(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{
		MaxAttempts: 200,
		Backoff:     ConstantBackoff{Delay: time.Second},
		MaxDelay:    500 * time.Millisecond,
		Jitter:      true,
		Clock:       clock,
	}, nil)

	_, _ = r.run(context.Background(), func(context.Context) error { return errTransient })

	waits := clock.Waits()
	distinct := map[time.Duration]bool{}
	for _, w := range waits {
		distinct[w] = true
		assert.GreaterOrEqual(t, w, time.Duration(0))
		assert.Less(t, w, 500*time.Millisecond, "jitter applies after MaxDelay")
	}
	assert.Greater(t, len(distinct), 1, "waits should be randomized")
}

func TestRetryNegativeMaxDelayInvalid(t *testing.T) {
	_, err := New(WithRetry(RetryPolicy{MaxAttempts: 2, MaxDelay: -time.Second}))
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestRetryMaxElapsedTime(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{
		MaxAttempts:    100,
		Backoff:        ConstantBackoff{Delay: 3 * time.Second},
		MaxElapsedTime: 10 * time.Second,
		Clock:          clock,
	}, nil)

	attempts, err := r.run(context.Background(), func(context.Context) error { return errTransient })

	// waits at 0s, 3s, 6s; a 4th wait would end at 12s > 10s
	assert.Equal(t, 4, attempts)
	assert.Len(t, clock.Waits(), 3)
	assert.ErrorIs(t, err, ErrRetryBudgetExceeded)
	assert.ErrorIs(t, err, errTransient)
	assert.Equal(t, StatusBudgetExceeded, statusOf(err))
}

func TestRetryMaxElapsedTimeCountsAttemptDuration(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{
		MaxAttempts:    100,
		Backoff:        ConstantBackoff{Delay: time.Second},
		MaxElapsedTime: 10 * time.Second,
		Clock:          clock,
	}, nil)

	attempts, err := r.run(context.Background(), func(context.Context) error {
		clock.Advance(4 * time.Second)
		return errTransient
	})

	// 4s run, 1s wait, 4s run = 9s; another 1s wait fits (10s) but the
	// attempt after it overruns, so the next wait check fails at 14s
	assert.Equal(t, 3, attempts)
	assert.ErrorIs(t, err, ErrRetryBudgetExceeded)
}

func TestRetryMaxElapsedTimeHugeDelayDoesNotOverflow(t *testing.T) {
	r := newRetrier(RetryPolicy{
		MaxAttempts:    5,
		Backoff:        ExponentialBackoff{Base: time.Hour, Multiplier: 1e9},
		MaxElapsedTime: time.Minute,
		Clock:          newFakeClock(),
	}, nil)

	attempts, err := r.run(context.Background(), func(context.Context) error { return errTransient })
	assert.Equal(t, 1, attempts)
	assert.ErrorIs(t, err, ErrRetryBudgetExceeded)
}

func TestRetryStopsOnPermanent(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Clock: clock}, nil)
	errBadInput := errors.New("bad input")

	attempts, err := r.run(context.Background(), func(context.Context) error {
		return fmt.Errorf("validating: %w", Permanent(errBadInput))
	})

	assert.Equal(t, 1, attempts)
	assert.Empty(t, clock.Waits())
	assert.ErrorIs(t, err, ErrPermanentFailure)
	assert.ErrorIs(t, err, errBadInput)
	assert.Equal(t, StatusPermanent, statusOf(err))
	assert.Equal(t, "permanent failure after 1 attempt(s): validating: bad input", err.Error())
}

func TestPermanentNil(t *testing.T) {
	assert.NoError(t, Permanent(nil))
}

func TestRetryAfterOverridesBackoff(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{
		MaxAttempts: 4,
		Backoff:     ConstantBackoff{Delay: time.Second},
		MaxDelay:    10 * time.Second,
		Jitter:      true,
		Clock:       clock,
	}, nil)
	errRateLimited := errors.New("429")

	calls := 0
	_, err := r.run(context.Background(), func(context.Context) error {
		calls++
		switch calls {
		case 1:
			return RetryAfter(errRateLimited, 7*time.Second)
		case 2:
			return RetryAfter(errRateLimited, time.Minute)
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []time.Duration{7 * time.Second, 10 * time.Second}, clock.Waits(), "hint is exact, not jittered, and capped by MaxDelay")
}

func TestRetryAfterKeepsError(t *testing.T) {
	errRateLimited := errors.New("429")
	err := RetryAfter(errRateLimited, time.Second)
	assert.ErrorIs(t, err, errRateLimited)
	assert.Equal(t, "429", err.Error())
	assert.NoError(t, RetryAfter(nil, time.Second))
}

func TestRetryErrorLimits(t *testing.T) {
	errRateLimited := errors.New("rate limited")
	errTimeout := errors.New("timeout")

	tests := []struct {
		name         string
		errs         []error // error returned by each attempt; nil = success
		wantAttempts int
		wantErr      error
	}{
		{"limited error stops early", []error{errTimeout, errTimeout, errTimeout}, 2, ErrRetriesExhausted},
		{"wrapped errors match", []error{fmt.Errorf("op: %w", errTimeout), errTimeout}, 2, ErrRetriesExhausted},
		{"counts per error", []error{errTimeout, errRateLimited, errRateLimited, errRateLimited, nil}, 5, nil},
		{"MaxAttempts still applies", []error{errRateLimited, errTransient, errRateLimited, errTransient, errRateLimited, errTransient}, 6, ErrRetriesExhausted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRetrier(RetryPolicy{
				MaxAttempts: 6,
				ErrorLimits: []ErrorLimit{{Err: errTimeout, MaxAttempts: 2}, {Err: errRateLimited, MaxAttempts: 5}},
				Clock:       newFakeClock(),
			}, nil)
			calls := 0
			attempts, err := r.run(context.Background(), func(context.Context) error {
				err := tt.errs[calls]
				calls++
				return err
			})
			assert.Equal(t, tt.wantAttempts, attempts)
			if tt.wantErr == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestRetryErrorLimitsBeyondStackBuffer(t *testing.T) {
	limits := make([]ErrorLimit, 10)
	for i := range limits {
		limits[i] = ErrorLimit{Err: fmt.Errorf("err %d", i), MaxAttempts: 3}
	}
	r := newRetrier(RetryPolicy{MaxAttempts: 10, ErrorLimits: limits, Clock: newFakeClock()}, nil)

	attempts, err := r.run(context.Background(), func(context.Context) error { return limits[9].Err })
	assert.Equal(t, 3, attempts)
	assert.ErrorIs(t, err, ErrRetriesExhausted)
}

func TestRetryErrorLimitsValidation(t *testing.T) {
	for _, l := range []ErrorLimit{{Err: nil, MaxAttempts: 1}, {Err: errTransient, MaxAttempts: 0}} {
		_, err := New(WithRetry(RetryPolicy{MaxAttempts: 2, ErrorLimits: []ErrorLimit{l}}))
		assert.ErrorIs(t, err, ErrInvalidConfig)
	}
}

func TestPoolRetryObservability(t *testing.T) {
	var rec logRecorder
	reg := prometheus.NewRegistry()
	results := make(chan TaskResult, 2)
	p := newTestPool(t,
		WithMinWorkers(1), WithMaxWorkers(1),
		WithLogger(rec.logger()),
		WithPrometheusRegisterer(reg),
		WithRetry(RetryPolicy{MaxAttempts: 3, Backoff: ConstantBackoff{Delay: time.Millisecond}}),
		WithOnTaskDone(func(r TaskResult) { results <- r }),
	)

	ctx := context.Background()
	recovers, _ := failN(1, errTransient)
	require.NoError(t, p.SubmitTask(ctx, Task{ID: "a", Kind: "sync", Run: recovers}))
	require.NoError(t, p.SubmitTask(ctx, Task{ID: "b", Kind: "sync", Run: func(context.Context) error { return errTransient }}))

	got := map[string]TaskResult{}
	for range 2 {
		select {
		case r := <-results:
			got[r.ID] = r
		case <-time.After(2 * time.Second):
			t.Fatal("tasks did not finish")
		}
	}

	assert.Equal(t, StatusSuccess, got["a"].Status)
	assert.Equal(t, 2, got["a"].Attempts)
	assert.Equal(t, StatusRetriesExhausted, got["b"].Status)
	assert.Equal(t, 3, got["b"].Attempts)
	assert.ErrorIs(t, got["b"].Err, errTransient)

	assert.Equal(t, int64(2), p.Metrics().JobsRetried())
	assert.Equal(t, int64(3), p.Metrics().RetryAttempts())
	assert.Equal(t, int64(1), p.Metrics().JobsFailed())

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

	retries := rec.records("retrying task")
	require.Len(t, retries, 3)
	assert.Equal(t, "sync", retries[0]["kind"])
	failed := rec.records("task failed")
	require.Len(t, failed, 1)
	assert.Equal(t, "b", failed[0]["task_id"])
	assert.Equal(t, "retries_exhausted", failed[0]["status"])
	assert.InDelta(t, 3, failed[0]["attempts"], 0)
}
