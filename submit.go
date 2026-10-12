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
	return p.submit(ctx, task, true)
}

// TrySubmit submits a task only if the queue has space
func (p *pool) TrySubmit(ctx context.Context, task Task) error {
	return p.submit(ctx, task, false)
}

func (p *pool) submit(ctx context.Context, task Task, wait bool) error {
	if task.Run == nil {
		return ErrNilJob
	}

	// Register before checking the state: Shutdown sets the state and then
	// waits for submitters to reach zero, so either this call sees the
	// shutdown or Shutdown waits for it before closing the queue.
	p.state.submitters.Add(1)
	defer p.state.submitters.Add(-1)

	if p.state.isShutdown() {
		p.metrics.recordJobRejected(task.Kind, RejectShutdown)
		return ErrPoolShutdown
	}

	runCtx := task.Context
	if runCtx == nil {
		runCtx = ctx
	}
	wrapper := jobWrapper{
		job:         task.Run,
		SubmittedAt: time.Now(),
		ctx:         runCtx,
		taskID:      task.ID,
		kind:        task.Kind,
	}

	// Enqueue immediately if there is space. Without this, a context that is
	// already done (e.g. a zero timeout meaning "don't wait") would race the
	// send in the select below and be rejected at random even with room.
	select {
	case p.state.jobQueue <- wrapper:
		p.metrics.recordJobSubmitted(task.Kind)
		return nil
	default:
	}

	if !wait {
		p.metrics.recordJobRejected(task.Kind, RejectQueueFull)
		return ErrQueueFull
	}

	// Queue is full: wait for space with backpressure
	select {
	case p.state.jobQueue <- wrapper:
		p.metrics.recordJobSubmitted(task.Kind)
		return nil

	case <-p.state.closing:
		p.metrics.recordJobRejected(task.Kind, RejectShutdown)
		return ErrPoolShutdown

	case <-ctx.Done():
		// Context cancelled or timed out
		if ctx.Err() == context.DeadlineExceeded {
			p.metrics.recordJobRejected(task.Kind, RejectTimeout)
			return ErrTimeout
		}
		p.metrics.recordJobRejected(task.Kind, RejectCanceled)
		return ctx.Err()
	}
}
