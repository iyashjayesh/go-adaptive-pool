package adaptivepool

import (
	"context"
	"time"
)

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
		p.metrics.recordJobRejected(task.Kind, rejectShutdown)
		return ErrPoolShutdown
	}

	wrapper := jobWrapper{
		job:         task.Run,
		SubmittedAt: time.Now(),
		ctx:         ctx,
		taskID:      task.ID,
		kind:        task.Kind,
	}

	// Try to enqueue the job with backpressure
	select {
	case p.state.jobQueue <- wrapper:
		p.metrics.recordJobSubmitted(task.Kind)
		return nil

	case <-ctx.Done():
		// Context cancelled or timed out
		if ctx.Err() == context.DeadlineExceeded {
			p.metrics.recordJobRejected(task.Kind, rejectTimeout)
			return ErrTimeout
		}
		p.metrics.recordJobRejected(task.Kind, rejectCanceled)
		return ctx.Err()
	}
}
