package adaptivepool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmitTask(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1))

	done := make(chan struct{})
	err := p.SubmitTask(context.Background(), Task{
		ID:   "order-1",
		Kind: "send_email",
		Run: func(context.Context) error {
			close(done)
			return nil
		},
	})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not run")
	}
}

func TestSubmitNilJob(t *testing.T) {
	p := newTestPool(t)

	assert.ErrorIs(t, p.Submit(context.Background(), nil), ErrNilJob)
	assert.ErrorIs(t, p.SubmitTask(context.Background(), Task{ID: "x"}), ErrNilJob)
}

type ctxKey struct{}

func TestTaskContextOutlivesSubmitContext(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1), WithLogger(discardLogger()))

	reqCtx, cancelReq := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "trace-1"))
	result := make(chan error, 1)
	err := p.SubmitTask(reqCtx, Task{
		Context: context.WithoutCancel(reqCtx),
		Run: func(ctx context.Context) error {
			assert.Equal(t, "trace-1", ctx.Value(ctxKey{}), "values carry over")
			select {
			case <-time.After(20 * time.Millisecond):
				result <- nil
			case <-ctx.Done():
				result <- ctx.Err()
			}
			return nil
		},
	})
	require.NoError(t, err)
	cancelReq() // the request ends right after submitting

	select {
	case err := <-result:
		assert.NoError(t, err, "job must not be cancelled with the submit context")
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish")
	}
}

func TestTaskContextBoundsOnlyTheWait(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(1), WithLogger(discardLogger()))

	release := make(chan struct{})
	defer close(release)
	block := func(context.Context) error { //nolint:unparam
		<-release
		return nil
	}
	require.NoError(t, p.Submit(context.Background(), block))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.Submit(context.Background(), block)) // queue now full

	submitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := p.SubmitTask(submitCtx, Task{Context: context.Background(), Run: block})
	assert.ErrorIs(t, err, ErrTimeout, "the submit context still bounds waiting for space")
}

func TestTaskContextUsedForRetries(t *testing.T) {
	results := make(chan TaskResult, 1)
	p := newTestPool(t,
		WithLogger(discardLogger()),
		WithRetry(RetryPolicy{MaxAttempts: 5, Backoff: ConstantBackoff{Delay: time.Hour}}),
		WithOnTaskDone(func(r TaskResult) { results <- r }),
	)

	runCtx, cancelRun := context.WithCancel(context.Background())
	attempted := make(chan struct{})
	require.NoError(t, p.SubmitTask(context.Background(), Task{
		Context: runCtx,
		Run: func(context.Context) error {
			close(attempted)
			return errors.New("transient")
		},
	}))
	<-attempted
	cancelRun() // aborts the hour-long backoff wait

	select {
	case r := <-results:
		assert.Equal(t, StatusCanceled, r.Status)
	case <-time.After(2 * time.Second):
		t.Fatal("backoff wait was not tied to the execution context")
	}
}

// Regression: without WithRetry, Permanent errors were reported as plain
// errors, so the status depended on whether retries were configured.
func TestPermanentStatusDoesNotDependOnRetry(t *testing.T) {
	cause := errors.New("bad input")
	for name, opts := range map[string][]Option{
		"no retry": nil,
		"retry":    {WithRetry(RetryPolicy{MaxAttempts: 3, Backoff: ConstantBackoff{}})},
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan TaskResult, 1)
			opts = append(opts, WithLogger(discardLogger()), WithOnTaskDone(func(r TaskResult) { done <- r }))
			p := newTestPool(t, opts...)
			require.NoError(t, p.Submit(context.Background(), func(context.Context) error { return Permanent(cause) }))

			r := <-done
			assert.Equal(t, StatusPermanent, r.Status)
			assert.Equal(t, 1, r.Attempts)
			assert.ErrorIs(t, r.Err, cause)
		})
	}
}
