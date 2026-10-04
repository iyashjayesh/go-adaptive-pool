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

		// Transition to draining state
		p.state.setShutdownState(shutdownStateDraining)

		// Stop the dispatcher
		p.dispatcherCancel()
		p.dispatcherWg.Wait()

		// Close the job queue to prevent new submissions
		close(p.state.jobQueue)

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

			// Abort retry backoff waits and signal all workers to terminate
			close(p.state.forceStop)
			currentWorkers := p.state.getWorkerCount()
			for range currentWorkers {
				select {
				case p.state.workerShutdown <- struct{}{}:
				default:
				}
			}

			// Wait a bit more for workers to respond
			select {
			case <-workersDone:
				p.logger.Info("workers terminated after force signal")
			case <-time.After(1 * time.Second):
				p.logger.Warn("some workers may not have terminated cleanly")
				shutdownErr = ctx.Err()
			}
		}

		if p.config.registerer != nil {
			p.metrics.unregister(p.config.registerer)
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
