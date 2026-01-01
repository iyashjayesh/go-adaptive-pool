package adaptivepool

import (
	"context"
	"log"
	"time"
)

// Shutdown gracefully shuts down the pool
func (p *pool) Shutdown(ctx context.Context) error {
	var shutdownErr error

	p.state.shutdownOnce.Do(func() {
		log.Println("initiating pool shutdown")

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
			log.Println("all workers finished gracefully")

		case <-ctx.Done():
			// Timeout or cancellation - force shutdown
			log.Println("shutdown timeout, forcing worker termination")

			// Signal all workers to terminate
			currentWorkers := p.state.getWorkerCount()
			for i := 0; i < currentWorkers; i++ {
				select {
				case p.state.workerShutdown <- struct{}{}:
				default:
				}
			}

			// Wait a bit more for workers to respond
			select {
			case <-workersDone:
				log.Println("workers terminated after force signal")
			case <-time.After(1 * time.Second):
				log.Println("some workers may not have terminated cleanly")
				shutdownErr = ctx.Err()
			}
		}

		// Transition to terminated state
		p.state.setShutdownState(shutdownStateTerminated)
		close(p.state.shutdownDone)

		// Check if there are any jobs left in the queue
		remainingJobs := p.state.getQueueDepth()
		if remainingJobs > 0 {
			log.Printf("shutdown complete with %d jobs remaining in queue", remainingJobs)
		} else {
			log.Println("shutdown complete, all jobs processed")
		}
	})

	// Wait for shutdown to complete
	<-p.state.shutdownDone

	return shutdownErr
}
