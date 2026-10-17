package adaptivepool

import (
	"context"
	"log/slog"
	"time"
)

// Shutdown gracefully shuts down the pool
func (p *pool) Shutdown(ctx context.Context) error {
	var shutdownErr error

	p.state.shutdownOnce.Do(func() {
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

		select {
		case <-workersDone:
			// All workers finished gracefully
			p.logger.Info("all workers finished gracefully")

		case <-ctx.Done():
			// Timeout or cancellation - force shutdown
			p.logger.Warn("shutdown timeout, forcing worker termination")

			// Abort retry backoff waits and stop idle workers
			close(p.state.forceStop)

			// Wait a bit more for workers to respond
			select {
			case <-workersDone:
				p.logger.Info("workers terminated after force signal")
			case <-time.After(1 * time.Second):
				p.logger.Warn("some workers may not have terminated cleanly")
				shutdownErr = ctx.Err()
			}
		}

		if a, ok := p.metrics.rec.(Attacher); ok {
			a.Detach()
		}

		// Transition to terminated state
		p.state.setShutdownState(shutdownStateTerminated)
		close(p.state.shutdownDone)

		// Check if there are any jobs left in the queue
		remainingJobs := p.state.getQueueDepth()
		if remainingJobs > 0 {
			p.logger.Warn("shutdown complete with jobs remaining in queue", slog.Int("remaining", remainingJobs))
		} else {
			p.logger.Info("shutdown complete, all jobs processed")
		}
	})

	// Wait for shutdown to complete
	<-p.state.shutdownDone

	return shutdownErr
}
