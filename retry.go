package adaptivepool

import (
	"context"
	"fmt"
	"time"
)

const defaultRetryDelay = 100 * time.Millisecond

// RetryPolicy configures how failed jobs are retried. Retries run inline on
// the worker that executed the job: no re-queueing and no extra goroutines.
//
// Every error is retried unless the job's context is done. Panics are never
// retried. When retrying stops, the job's final error is a *RetryError.
type RetryPolicy struct {
	// MaxAttempts is the maximum number of times a job runs, including the
	// first attempt. Required and must be >= 1.
	MaxAttempts int

	// Backoff computes the wait before each retry.
	// Default: ConstantBackoff{Delay: 100ms}
	Backoff Backoff

	// Clock is the time source used for backoff waits. Override it in tests.
	// Default: the system clock
	Clock Clock
}

// Backoff computes the delay before a retry. retry starts at 1 for the
// first retry. Implementations must be safe for concurrent use; the
// built-in ones are stateless values.
type Backoff interface {
	Next(retry int) time.Duration
}

// ConstantBackoff waits the same Delay before every retry.
type ConstantBackoff struct {
	Delay time.Duration
}

// Next implements Backoff
func (b ConstantBackoff) Next(int) time.Duration {
	return b.Delay
}

// Clock abstracts time so retry timing can be tested deterministically.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// WithRetry enables retries for every job submitted to the pool.
// Default: disabled
func WithRetry(policy RetryPolicy) Option {
	return func(c *Config) {
		c.retry = &policy
	}
}

func (rp *RetryPolicy) validate() error {
	if rp.MaxAttempts < 1 {
		return fmt.Errorf("%w: retry MaxAttempts must be >= 1", ErrInvalidConfig)
	}
	return nil
}

// retrier executes jobs according to a normalized RetryPolicy
type retrier struct {
	policy RetryPolicy
}

func newRetrier(policy RetryPolicy) *retrier {
	if policy.Backoff == nil {
		policy.Backoff = ConstantBackoff{Delay: defaultRetryDelay}
	}
	if policy.Clock == nil {
		policy.Clock = systemClock{}
	}
	return &retrier{policy: policy}
}

// run executes job until it succeeds or the policy says to stop. It returns
// the number of attempts made and the final error.
func (r *retrier) run(ctx context.Context, job Job) (int, error) {
	for attempt := 1; ; attempt++ {
		err := runJob(ctx, job)
		if err == nil {
			return attempt, nil
		}
		if _, panicked := err.(*PanicError); panicked {
			return attempt, err
		}
		if reason := r.stopReason(ctx, attempt); reason != nil {
			return attempt, &RetryError{Reason: reason, Attempts: attempt, Err: err}
		}
		if waitErr := r.wait(ctx, r.policy.Backoff.Next(attempt)); waitErr != nil {
			return attempt, &RetryError{Reason: waitErr, Attempts: attempt, Err: err}
		}
	}
}

// stopReason returns why retrying must stop after a failed attempt, or nil to retry
func (r *retrier) stopReason(ctx context.Context, attempt int) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case attempt >= r.policy.MaxAttempts:
		return ErrRetriesExhausted
	}
	return nil
}

// wait blocks for d, returning early with the context's error if it is done
func (r *retrier) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.policy.Clock.After(d):
		return nil
	}
}
