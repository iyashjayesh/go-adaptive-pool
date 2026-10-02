package adaptivepool

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

const (
	defaultRetryDelay = 100 * time.Millisecond
	defaultMultiplier = 1.5
	maxDuration       = time.Duration(math.MaxInt64)
)

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
	// Default: ExponentialBackoff{Base: 100ms}
	Backoff Backoff

	// MaxDelay caps any single backoff wait. Zero means no cap.
	MaxDelay time.Duration

	// Jitter enables full jitter: each wait is drawn uniformly from
	// [0, delay), where delay is the backoff value after MaxDelay is applied.
	// It spreads out retries of jobs that failed together, avoiding a
	// thundering herd on the downstream system.
	Jitter bool

	// MaxElapsedTime bounds the total time spent on a job across all
	// attempts and waits. Retrying stops early if the next wait would exceed
	// it; an attempt already running is never interrupted. Zero means no
	// budget. Applies in addition to MaxAttempts.
	MaxElapsedTime time.Duration

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

// LinearBackoff waits Base * retry: Base, 2*Base, 3*Base, ...
type LinearBackoff struct {
	Base time.Duration
}

// Next implements Backoff
func (b LinearBackoff) Next(retry int) time.Duration {
	if b.Base > 0 && time.Duration(retry) > maxDuration/b.Base {
		return maxDuration
	}
	return b.Base * time.Duration(retry)
}

// ExponentialBackoff waits Base * Multiplier^(retry-1).
// A zero Multiplier defaults to 1.5.
type ExponentialBackoff struct {
	Base       time.Duration
	Multiplier float64
}

// Next implements Backoff
func (b ExponentialBackoff) Next(retry int) time.Duration {
	m := b.Multiplier
	if m == 0 {
		m = defaultMultiplier
	}
	d := float64(b.Base) * math.Pow(m, float64(retry-1))
	if d >= float64(maxDuration) {
		return maxDuration
	}
	return time.Duration(d)
}

func (b ConstantBackoff) validate() error { return nonNegative("ConstantBackoff.Delay", b.Delay) }
func (b LinearBackoff) validate() error   { return nonNegative("LinearBackoff.Base", b.Base) }
func (b ExponentialBackoff) validate() error {
	if b.Multiplier != 0 && b.Multiplier < 1 {
		return fmt.Errorf("%w: ExponentialBackoff.Multiplier must be >= 1", ErrInvalidConfig)
	}
	return nonNegative("ExponentialBackoff.Base", b.Base)
}

func nonNegative(field string, d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("%w: %s must not be negative", ErrInvalidConfig, field)
	}
	return nil
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
	if err := nonNegative("retry MaxDelay", rp.MaxDelay); err != nil {
		return err
	}
	if err := nonNegative("retry MaxElapsedTime", rp.MaxElapsedTime); err != nil {
		return err
	}
	if v, ok := rp.Backoff.(interface{ validate() error }); ok {
		return v.validate()
	}
	return nil
}

// retrier executes jobs according to a normalized RetryPolicy
type retrier struct {
	policy RetryPolicy
}

func newRetrier(policy RetryPolicy) *retrier {
	if policy.Backoff == nil {
		policy.Backoff = ExponentialBackoff{Base: defaultRetryDelay}
	}
	if policy.Clock == nil {
		policy.Clock = systemClock{}
	}
	return &retrier{policy: policy}
}

// run executes job until it succeeds or the policy says to stop. It returns
// the number of attempts made and the final error.
func (r *retrier) run(ctx context.Context, job Job) (int, error) {
	var start time.Time
	if r.policy.MaxElapsedTime > 0 {
		start = r.policy.Clock.Now()
	}

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
		delay := r.delay(attempt)
		if r.policy.MaxElapsedTime > 0 && delay > r.policy.MaxElapsedTime-r.policy.Clock.Now().Sub(start) {
			return attempt, &RetryError{Reason: ErrRetryBudgetExceeded, Attempts: attempt, Err: err}
		}
		if waitErr := r.wait(ctx, delay); waitErr != nil {
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

// delay computes the wait before the given retry
func (r *retrier) delay(retry int) time.Duration {
	d := r.policy.Backoff.Next(retry)
	if r.policy.MaxDelay > 0 {
		d = min(d, r.policy.MaxDelay)
	}
	if r.policy.Jitter && d > 0 {
		d = rand.N(d)
	}
	return d
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
