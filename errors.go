package adaptivepool

import (
	"errors"
	"fmt"
)

var (
	// ErrPoolShutdown is returned when attempting to Submit a job to a shutdown pool
	ErrPoolShutdown = errors.New("pool is shutdown")

	// ErrQueueFull is returned when the job queue is at capacity and cannot accept new jobs
	ErrQueueFull = errors.New("queue is full")

	// ErrTimeout is returned when a job submission times out
	ErrTimeout = errors.New("submission timeout")

	// ErrNilJob is returned when submitting a nil job
	ErrNilJob = errors.New("job is nil")

	// ErrRetriesExhausted is the RetryError reason when a job used all its attempts
	ErrRetriesExhausted = errors.New("retries exhausted")

	// ErrRetryBudgetExceeded is the RetryError reason when the next retry would exceed MaxElapsedTime
	ErrRetryBudgetExceeded = errors.New("retry time budget exceeded")

	// ErrPermanentFailure is the RetryError reason when a job returned an error wrapped with Permanent
	ErrPermanentFailure = errors.New("permanent failure")

	// ErrInvalidConfig is returned when pool configuration is invalid
	ErrInvalidConfig = errors.New("invalid pool configuration")
)

// PanicError is the error recorded when a job panics. Stack holds the
// goroutine stack captured at the time of the panic.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("job panicked: %v", e.Value)
}

// RetryError is the final error of a job that was retried and gave up.
// errors.Is matches both the stop Reason (e.g. ErrRetriesExhausted or
// context.Canceled) and the job's last error.
type RetryError struct {
	// Reason explains why retrying stopped
	Reason error
	// Attempts is the number of times the job ran
	Attempts int
	// Err is the error returned by the last attempt
	Err error
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("%v after %d attempt(s): %v", e.Reason, e.Attempts, e.Err)
}

// Unwrap returns the stop reason and the last attempt's error
func (e *RetryError) Unwrap() []error {
	return []error{e.Reason, e.Err}
}

// Permanent marks err as not retryable: the retry engine stops immediately.
// Use it for failures that cannot succeed on retry, such as invalid input.
// Permanent(nil) returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }
