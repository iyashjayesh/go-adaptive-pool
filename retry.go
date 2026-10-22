package adaptivepool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

const (
	defaultRetryDelay = 100 * time.Millisecond
	defaultMultiplier = 1.5
	maxDuration       = time.Duration(math.MaxInt64)
)

// RetryPolicy configures how failed jobs are retried. Retries run inline on
// the worker; errors are retried unless wrapped with Permanent or the job's
// context is done, and panics are never retried. A job that stops retrying
// fails with a *RetryError.
//
// A graceful Shutdown lets retries finish; a forced one aborts backoff waits.
type RetryPolicy struct {
	// MaxAttempts is the maximum number of times a job runs, including the
	// first attempt. Required and must be >= 1.
	MaxAttempts int

	// Backoff computes the wait before each retry.
	// Default: ExponentialBackoff{Base: 100ms}
	Backoff Backoff

	// MaxDelay caps any single wait, including RetryAfter hints, so a job
	// cannot hold a worker for an unbounded time. Zero means no cap.
	MaxDelay time.Duration

	// Jitter draws each wait uniformly from [0, delay), after MaxDelay, to
	// spread out retries of jobs that failed together.
	Jitter bool

	// MaxElapsedTime bounds a job's total time across attempts and waits.
	// Retrying stops if the next wait would exceed it; a running attempt is
	// never interrupted. Zero means no budget.
	MaxElapsedTime time.Duration

	// ErrorLimits caps attempts for specific errors. Failures are matched
	// with errors.Is against each limit in order; the first match counts.
	// MaxAttempts still applies to the job as a whole.
	ErrorLimits []ErrorLimit

	// Clock is the time source used for backoff waits. Override it in tests.
	// Default: the system clock
	Clock Clock
}

// ErrorLimit limits how many attempts may fail with errors matching Err.
type ErrorLimit struct {
	Err         error
	MaxAttempts int
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
	if b.Base == 0 {
		return 0 // 0 * +Inf below would be NaN, which converts to an arbitrary Duration
	}
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
	// NaN fails the >= 1 check
	if b.Multiplier != 0 && !(b.Multiplier >= 1 && !math.IsInf(b.Multiplier, 1)) {
		return fmt.Errorf("%w: ExponentialBackoff.Multiplier must be a finite value >= 1", ErrInvalidConfig)
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
	for _, l := range rp.ErrorLimits {
		if l.Err == nil || l.MaxAttempts < 1 {
			return fmt.Errorf("%w: ErrorLimit needs a non-nil Err and MaxAttempts >= 1", ErrInvalidConfig)
		}
	}
	if v, ok := rp.Backoff.(interface{ validate() error }); ok {
		return v.validate()
	}
	return nil
}

// retrier executes jobs according to a normalized RetryPolicy
type retrier struct {
	policy RetryPolicy
	logger *slog.Logger
	stop   <-chan struct{} // aborts backoff waits on forced shutdown
}

func newRetrier(policy RetryPolicy, logger *slog.Logger, stop <-chan struct{}) *retrier {
	if policy.Backoff == nil {
		policy.Backoff = ExponentialBackoff{Base: defaultRetryDelay}
	}
	if policy.Clock == nil {
		policy.Clock = systemClock{}
	}
	policy.ErrorLimits = slices.Clone(policy.ErrorLimits)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &retrier{policy: policy, logger: logger, stop: stop}
}

// execute runs the job until it succeeds or the policy says to stop. It
// returns the number of attempts made and the final error.
func (r *retrier) execute(w *jobWrapper) (int, error) {
	ctx := w.ctx
	var start time.Time
	if r.policy.MaxElapsedTime > 0 {
		start = r.policy.Clock.Now()
	}

	// Per-error failure counts live on the stack for typical limit counts
	var buf [8]int
	var counts []int
	if n := len(r.policy.ErrorLimits); n > len(buf) {
		counts = make([]int, n)
	} else {
		counts = buf[:n]
	}

	for attempt := 1; ; attempt++ {
		err := runJob(ctx, w.job)
		if err == nil {
			return attempt, nil
		}
		if _, panicked := err.(*PanicError); panicked {
			return attempt, err
		}
		if reason := r.stopReason(ctx, attempt, err, counts); reason != nil {
			return attempt, &RetryError{Reason: reason, Attempts: attempt, Err: err}
		}
		delay := r.delay(attempt, err)
		if r.policy.MaxElapsedTime > 0 && delay > r.policy.MaxElapsedTime-r.policy.Clock.Now().Sub(start) {
			return attempt, &RetryError{Reason: ErrRetryBudgetExceeded, Attempts: attempt, Err: err}
		}
		r.logRetry(w, attempt, delay, err)
		if waitErr := r.wait(ctx, delay); waitErr != nil {
			return attempt, &RetryError{Reason: waitErr, Attempts: attempt, Err: err}
		}
	}
}

func (r *retrier) logRetry(w *jobWrapper, attempt int, delay time.Duration, err error) {
	if !r.logger.Enabled(w.ctx, slog.LevelDebug) {
		return
	}
	r.logger.LogAttrs(w.ctx, slog.LevelDebug, "retrying task",
		slog.String("task_id", w.taskID),
		slog.String("kind", w.kind),
		slog.Int("attempt", attempt),
		slog.Duration("delay", delay),
		slog.Any("err", err),
	)
}

// stopReason returns why retrying must stop after a failed attempt, or nil to retry
func (r *retrier) stopReason(ctx context.Context, attempt int, err error, counts []int) error {
	switch {
	case hasMarker[*permanentError](err):
		return ErrPermanentFailure
	case ctx.Err() != nil:
		return ctx.Err()
	case attempt >= r.policy.MaxAttempts, r.errorLimitReached(err, counts):
		return ErrRetriesExhausted
	}
	return nil
}

// errorLimitReached counts err against the first matching ErrorLimit and
// reports whether that limit is now used up
func (r *retrier) errorLimitReached(err error, counts []int) bool {
	for i, l := range r.policy.ErrorLimits {
		if errors.Is(err, l.Err) {
			counts[i]++
			return counts[i] >= l.MaxAttempts
		}
	}
	return false
}

// delay computes the wait before the given retry. A RetryAfter hint on err
// replaces the backoff value and is not jittered; MaxDelay caps both.
func (r *retrier) delay(retry int, err error) time.Duration {
	if hint, ok := findMarker[*retryAfterError](err); ok {
		return r.capDelay(hint.delay)
	}
	d := r.capDelay(r.policy.Backoff.Next(retry))
	if r.policy.Jitter && d > 0 {
		d = rand.N(d)
	}
	return d
}

func (r *retrier) capDelay(d time.Duration) time.Duration {
	if r.policy.MaxDelay > 0 {
		return min(d, r.policy.MaxDelay)
	}
	return d
}

// wait blocks for d, returning early if the context is done or the pool is
// force-stopped
func (r *retrier) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if closed(r.stop) {
			return ErrPoolShutdown
		}
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.stop:
		return ErrPoolShutdown
	case <-r.policy.Clock.After(d):
		return nil
	}
}
