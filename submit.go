package adaptivepool

import (
	"context"
	"time"
)

// Submit Submits a job to the pool for execution
func (p *pool) Submit(ctx context.Context, job Job) error {
	// Check if pool is shutdown
	if p.state.isShutdown() {
		p.metrics.recordJobRejected()
		return ErrPoolShutdown
	}

	// creating job wrapper
	wrapper := jobWrapper{
		job:         job,
		SubmittedAt: time.Now(),
		id:          p.state.nextID(),
		ctx:         ctx,
	}

	// Try to enqueue the job with backpressure
	select {
	case p.state.jobQueue <- wrapper:
		// Job successfully enqueued
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
