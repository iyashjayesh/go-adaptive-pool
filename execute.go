package adaptivepool

import (
	"errors"
	"log/slog"
	"runtime/debug"
	"time"
)

// executeJob runs a job on the calling worker and records its outcome
func (p *pool) executeJob(workerID int, job jobWrapper) {
	p.state.busyWorkers.Add(1)
	defer p.state.busyWorkers.Add(-1)

	start := time.Now()
	err := runJob(job)
	duration := time.Since(start)
	status := statusOf(err)

	p.metrics.recordJobDone(job.kind, status, start.Sub(job.SubmittedAt), duration)
	if err != nil {
		p.logFailure(workerID, job, status, duration, err)
	}
}

// runJob executes the job, converting a panic into a *PanicError
func runJob(job jobWrapper) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Value: r, Stack: debug.Stack()}
		}
	}()
	return job.job(job.ctx)
}

func (p *pool) logFailure(workerID int, job jobWrapper, status Status, duration time.Duration, err error) {
	if !p.logger.Enabled(job.ctx, slog.LevelError) {
		return
	}
	attrs := []slog.Attr{
		slog.Int("worker", workerID),
		slog.String("task_id", job.taskID),
		slog.String("kind", job.kind),
		slog.String("status", string(status)),
		slog.Duration("duration", duration),
		slog.Any("err", err),
	}
	var pe *PanicError
	if errors.As(err, &pe) {
		attrs = append(attrs, slog.String("stack", string(pe.Stack)))
	}
	p.logger.LogAttrs(job.ctx, slog.LevelError, "task failed", attrs...)
}
