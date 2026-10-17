package adaptivepool

import (
	"context"
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

	// Context is the context Run executes with. When nil, the ctx passed to
	// SubmitTask is used for both waiting for queue space and execution. Set it
	// so a submit deadline doesn't cancel the job; see ExamplePool_SubmitTask.
	Context context.Context
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
	// StatusError means the job returned an error while its context was still active
	StatusError Status = "error"
	// StatusPanic means the job panicked; the error is a *PanicError
	StatusPanic Status = "panic"
	// StatusRetriesExhausted means the job failed on every allowed attempt
	StatusRetriesExhausted Status = "retries_exhausted"
	// StatusBudgetExceeded means retrying stopped because of MaxElapsedTime
	StatusBudgetExceeded Status = "budget_exceeded"
	// StatusPermanent means the job returned an error wrapped with Permanent,
	// with or without retries
	StatusPermanent Status = "permanent"
	// StatusShutdown means retrying was aborted by a forced pool shutdown
	StatusShutdown Status = "shutdown"
	// StatusCanceled means the job failed after its context was done
	StatusCanceled Status = "canceled"
)

// statusOf classifies the final error of a task. Errors reaching it come
// straight from runJob or the retrier, so a type switch is sufficient, apart
// from Permanent, which also reaches it unwrapped when retries are disabled.
func statusOf(err error) Status {
	switch e := err.(type) {
	case nil:
		return StatusSuccess
	case *PanicError:
		return StatusPanic
	case *RetryError:
		switch e.Reason {
		case ErrRetriesExhausted:
			return StatusRetriesExhausted
		case ErrRetryBudgetExceeded:
			return StatusBudgetExceeded
		case ErrPermanentFailure:
			return StatusPermanent
		case ErrPoolShutdown:
			return StatusShutdown
		case context.Canceled, context.DeadlineExceeded:
			return StatusCanceled
		}
	}
	if hasMarker[*permanentError](err) {
		return StatusPermanent
	}
	return StatusError
}
