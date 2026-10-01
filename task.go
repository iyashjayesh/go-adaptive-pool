package adaptivepool

import (
	"context"
	"errors"
	"time"
)

// Task is a Job annotated with identity used for observability.
type Task struct {
	// ID identifies a single task (e.g. an order or request ID). It appears in
	// logs and TaskResult only, never in metric labels, so it may be unique.
	ID string

	// Kind groups tasks of the same type (e.g. "send_email") and is used as a
	// metric label. Keep the set of kinds small and bounded.
	Kind string

	// Run is the work to execute.
	Run Job
}

// TaskResult describes how a task finished. It is passed to the OnTaskDone hook.
type TaskResult struct {
	ID   string
	Kind string

	// QueueWait is the time between submission and the start of execution
	QueueWait time.Duration
	// Duration is the total execution time, including any retries
	Duration time.Duration
	// Attempts is the number of times the job was run
	Attempts int

	Status Status
	// Err is the final error, nil on success
	Err error
}

// Status describes how a task finished. The same values are used for the
// "status" metric label and log attribute.
type Status string

const (
	// StatusSuccess means the job returned nil
	StatusSuccess Status = "success"
	// StatusError means the job returned an error
	StatusError Status = "error"
	// StatusPanic means the job panicked; the error is a *PanicError
	StatusPanic Status = "panic"
	// StatusRetriesExhausted means the job failed on every allowed attempt
	StatusRetriesExhausted Status = "retries_exhausted"
	// StatusCanceled means the job's context was done before it succeeded
	StatusCanceled Status = "canceled"
)

// statusOf classifies the final error of a task
func statusOf(err error) Status {
	if err == nil {
		return StatusSuccess
	}
	var pe *PanicError
	if errors.As(err, &pe) {
		return StatusPanic
	}
	var re *RetryError
	if errors.As(err, &re) {
		switch {
		case re.Reason == ErrRetriesExhausted:
			return StatusRetriesExhausted
		case errors.Is(re.Reason, context.Canceled), errors.Is(re.Reason, context.DeadlineExceeded):
			return StatusCanceled
		}
	}
	return StatusError
}
