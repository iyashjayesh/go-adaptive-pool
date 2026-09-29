package adaptivepool

import "errors"

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
	return StatusError
}
