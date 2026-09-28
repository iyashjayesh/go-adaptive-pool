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
}

// Submit Submits a job to the pool for execution
func (p *pool) Submit(ctx context.Context, job Job) error {
	return p.SubmitTask(ctx, Task{Run: job})
}

// SubmitTask submits a task to the pool for execution
func (p *pool) SubmitTask(ctx context.Context, task Task) error {
	if task.Run == nil {
		return ErrNilJob
	}

	// Check if pool is shutdown
	if p.state.isShutdown() {
		p.metrics.recordJobRejected()
		return ErrPoolShutdown
	}

	wrapper := jobWrapper{
		job:         task.Run,
		SubmittedAt: time.Now(),
		id:          p.state.nextID(),
		ctx:         ctx,
		taskID:      task.ID,
		kind:        task.Kind,
	}

	// Try to enqueue the job with backpressure
	select {
	case p.state.jobQueue <- wrapper:
		return nil

	case <-ctx.Done():
		// Context cancelled or timed out
		p.metrics.recordJobRejected()
		if ctx.Err() == context.DeadlineExceeded {
			return ErrTimeout
		}
		return ctx.Err()
	}
}
