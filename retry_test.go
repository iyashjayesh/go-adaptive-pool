package adaptivepool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestRetrySucceedsAfterFailures(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Backoff: ConstantBackoff{Delay: time.Second}, Clock: clock})

	job, calls := failN(2, errTransient)
	attempts, err := r.run(context.Background(), job)

	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.Equal(t, 3, *calls)
	assert.Equal(t, []time.Duration{time.Second, time.Second}, clock.Waits())
}

func TestRetryExhausted(t *testing.T) {
	clock := newFakeClock()
	r := newRetrier(RetryPolicy{MaxAttempts: 3, Clock: clock})

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
	r := newRetrier(RetryPolicy{MaxAttempts: 1, Clock: clock})

	attempts, err := r.run(context.Background(), func(context.Context) error { return errTransient })
	assert.Equal(t, 1, attempts)
	assert.ErrorIs(t, err, ErrRetriesExhausted)
	assert.Empty(t, clock.Waits())
}

func TestRetryStopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Clock: newFakeClock()})

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
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Backoff: ConstantBackoff{Delay: time.Hour}})

	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	attempts, err := r.run(ctx, func(context.Context) error { return errTransient })

	assert.Less(t, time.Since(start), time.Second, "backoff wait must abort on cancellation")
	assert.Equal(t, 1, attempts)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, errTransient)
}

func TestRetryDoesNotRetryPanics(t *testing.T) {
	r := newRetrier(RetryPolicy{MaxAttempts: 5, Clock: newFakeClock()})

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
