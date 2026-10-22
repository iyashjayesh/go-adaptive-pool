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

	// Shutdown sets the state, then takes the write lock to close the queue:
	// either this call sees the shutdown or Shutdown waits for it to finish.
	p.state.submitMu.RLock()
	defer p.state.submitMu.RUnlock()

	if p.state.isShutdown() {
		p.metrics.recordJobRejected(task.Kind, RejectShutdown)
		return ErrPoolShutdown
	}

	// Without Task.Context, ctx is also the job's context: a job queued with a
	// done one would be cancelled before it runs
	runCtx := task.Context
	if runCtx == nil {
		if ctx.Err() != nil {
			return p.rejectDone(ctx, task.Kind)
		}
		runCtx = ctx
	}
	wrapper := jobWrapper{
		job:         task.Run,
		submittedAt: int64(time.Since(p.epoch)),
		ctx:         runCtx,
		taskID:      task.ID,
		kind:        task.Kind,
	}

	// Enqueue immediately if there is space. Without this, a done submit
	// context (with Task.Context set) would race the send in the select below
	// and be rejected at random even with room.
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
		return p.rejectDone(ctx, task.Kind)
	}
}

// rejectDone records and returns the rejection for a done submit context
func (p *pool) rejectDone(ctx context.Context, kind string) error {
	if ctx.Err() == context.DeadlineExceeded {
		p.metrics.recordJobRejected(kind, RejectTimeout)
		return ErrTimeout
	}
	p.metrics.recordJobRejected(kind, RejectCanceled)
	return ctx.Err()
}
