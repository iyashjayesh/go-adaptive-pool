package adaptivepool

import (
	"context"
	"log/slog"
	"time"
)

// Shutdown gracefully shuts down the pool. The first call does the work; a
// later call returns the first call's result, or ctx's error if ctx ends
// before the shutdown does.
func (p *pool) Shutdown(ctx context.Context) error {
	if p.state.shutdownStarted.CompareAndSwap(false, true) {
		p.logger.Info("initiating pool shutdown")

		// Transition to draining state and wake Submits blocked on a full queue
		p.state.setShutdownState(shutdownStateDraining)
		close(p.state.closing)

		// Stop the dispatcher
		p.dispatcherCancel()
		p.dispatcherWg.Wait()

		// Closing the queue is only safe once no Submit can still send to it.
		// In-flight calls either finish their send or return via closing, so
		// taking the lock is brief, and new Submits wait behind it.
		p.state.submitMu.Lock()
		close(p.state.jobQueue)
		p.state.submitMu.Unlock()

		// Wait for workers to finish with timeout
		workersDone := make(chan struct{})
		go func() {
			p.state.workerWg.Wait()
			close(workersDone)
		}()

		drained := false
		select {
		case <-workersDone:
			// All workers finished gracefully
			p.logger.Info("all workers finished gracefully")
			drained = true

		case <-ctx.Done():
			// Timeout or cancellation - force shutdown
			p.logger.Warn("shutdown timeout, forcing worker termination")

			// Abort retry backoff waits and stop idle workers
			close(p.state.forceStop)

			// Drop what is still queued. The queue is closed, so this ends
			// once it is empty; a worker that took a job just before seeing
			// forceStop drops that one itself.
			for job := range p.state.jobQueue {
				p.dropJob(job)
			}

			// Wait a bit more for workers to respond
			select {
			case <-workersDone:
				p.logger.Info("workers terminated after force signal")
				drained = true
			case <-time.After(1 * time.Second):
				p.logger.Warn("some workers may not have terminated cleanly")
				p.state.shutdownErr = ctx.Err()
			}
		}

		dropped := p.metrics.JobsDropped()
		if dropped > 0 {
			p.state.shutdownErr = &ShutdownError{Dropped: int(dropped), Err: ctx.Err()}
		}

		p.detachRecorder(workersDone, drained)

		// Transition to terminated state
		p.state.setShutdownState(shutdownStateTerminated)
		close(p.state.shutdownDone)

		if dropped > 0 {
			p.logger.Warn("shutdown complete, dropped queued jobs", slog.Int64("dropped", dropped))
		} else {
			p.logger.Info("shutdown complete, all jobs processed")
		}
		return p.state.shutdownErr
	}

	// Not sync.Once: it would hold this call until the first one finishes,
	// whatever ctx says
	if closed(p.state.shutdownDone) {
		return p.state.shutdownErr
	}
	select {
	case <-p.state.shutdownDone:
		return p.state.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// detachRecorder detaches the recorder once no worker can still report to it:
// now if the workers have exited, otherwise when the last one does
func (p *pool) detachRecorder(workersDone <-chan struct{}, drained bool) {
	if p.metrics.rec == nil {
		return
	}
	if drained {
		p.metrics.detach()
		return
	}
	go func() {
		<-workersDone
		p.metrics.detach()
	}()
}
