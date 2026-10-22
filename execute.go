package adaptivepool

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// executeJob runs a job on the calling worker and records its outcome
func (p *pool) executeJob(workerID int, job jobWrapper) {
	p.state.busyWorkers.Add(1)
	defer p.state.busyWorkers.Add(-1)

	start := time.Now()
	attempts, err := 1, error(nil)
	if p.retrier != nil {
		attempts, err = p.retrier.execute(&job)
	} else {
		err = runJob(job.ctx, job.job)
	}
	duration := time.Since(start)
	status := statusOf(err)
	if status == StatusError && job.ctx.Err() != nil {
		// Match the retry engine: a failure after the context is done is canceled
		status = StatusCanceled
	}

	queueWait := start.Sub(p.epoch) - time.Duration(job.submittedAt)

	p.metrics.recordJobDone(job.kind, status, attempts, queueWait, duration)
	if err != nil {
		p.logFailure(workerID, job, status, attempts, duration, err)
	}
	if p.config.onTaskDone != nil {
		p.notifyTaskDone(TaskResult{
			ID:        job.taskID,
			Kind:      job.kind,
			QueueWait: queueWait,
			Duration:  duration,
			Attempts:  attempts,
			Status:    status,
			Err:       err,
		})
	}
}

// dropJob reports a queued job that a forced shutdown discarded before it ran
func (p *pool) dropJob(job jobWrapper) {
	p.metrics.recordJobDropped(job.kind)
	if p.config.onTaskDone != nil {
		p.notifyTaskDone(TaskResult{
			ID:        job.taskID,
			Kind:      job.kind,
			QueueWait: time.Since(p.epoch) - time.Duration(job.submittedAt),
			Status:    StatusShutdown,
			Err:       ErrPoolShutdown,
		})
	}
}

// notifyTaskDone invokes the OnTaskDone hook, shielding the worker from panics
func (p *pool) notifyTaskDone(res TaskResult) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("OnTaskDone hook panicked",
				slog.String("task_id", res.ID),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			)
		}
	}()
	p.config.onTaskDone(res)
}

// runJob executes the job, converting a panic into a *PanicError
func runJob(ctx context.Context, job Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Value: r, Stack: debug.Stack()}
		}
	}()
	return job(ctx)
}

func (p *pool) logFailure(workerID int, job jobWrapper, status Status, attempts int, duration time.Duration, err error) {
	if !p.logger.Enabled(job.ctx, slog.LevelError) {
		return
	}
	attrs := []slog.Attr{
		slog.Int("worker", workerID),
		slog.String("task_id", job.taskID),
		slog.String("kind", job.kind),
		slog.String("status", string(status)),
		slog.Int("attempts", attempts),
		slog.Duration("duration", duration),
		slog.Any("err", err),
	}
	if pe, ok := err.(*PanicError); ok {
		attrs = append(attrs, slog.String("stack", string(pe.Stack)))
	}
	p.logger.LogAttrs(job.ctx, slog.LevelError, "task failed", attrs...)
}
