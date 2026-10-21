package adaptivepool

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Job represents a unit of work to be executed by the pool
type Job func(ctx context.Context) error

// jobWrapper wraps a job with metadata for tracking
type jobWrapper struct {
	job         Job
	ctx         context.Context
	taskID      string
	kind        string
	submittedAt int64 // nanoseconds since pool.epoch
}

// shutdownState represents the shutdown state of the pool
type shutdownState int32

const (
	shutdownStateRunning shutdownState = iota
	shutdownStateDraining
	shutdownStateTerminated
)

// poolState holds the internal state of the pool
type poolState struct {
	// Job queue
	jobQueue chan jobWrapper

	// Worker management
	workerWg    sync.WaitGroup
	workerCount atomic.Int32
	busyWorkers atomic.Int32

	// Shutdown coordination. Submit holds submitMu for reading while it
	// checks the state and sends; Shutdown takes it for writing to close the
	// queue, so no send can race the close. closing wakes Submits blocked on a
	// full queue so they release the lock.
	shutdownStarted atomic.Bool // set by the first Shutdown call, which runs it
	shutdownErr     error       // its result, written before shutdownDone is closed
	shutdownState   atomic.Int32
	shutdownDone    chan struct{}
	closing         chan struct{} // closed when Shutdown starts
	forceStop       chan struct{} // closed when Shutdown's context expires
	submitMu        sync.RWMutex

	// Scaling coordination
	scalingMu   sync.Mutex
	lastScaleUp time.Time
}

// newPoolState Creates a new pool state
func newPoolState(queueSize int) *poolState {
	ps := &poolState{
		jobQueue:     make(chan jobWrapper, queueSize),
		shutdownDone: make(chan struct{}),
		closing:      make(chan struct{}),
		forceStop:    make(chan struct{}),
	}
	ps.shutdownState.Store(int32(shutdownStateRunning))
	return ps
}

// closed reports whether c is closed, without blocking
func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// isShutdown returns true if the pool is shutdown or draining
func (ps *poolState) isShutdown() bool {
	state := shutdownState(ps.shutdownState.Load())
	return state != shutdownStateRunning
}

// setShutdownState sets the shutdown state
func (ps *poolState) setShutdownState(state shutdownState) {
	ps.shutdownState.Store(int32(state))
}

// getWorkerCount returns the current number of workers
func (ps *poolState) getWorkerCount() int {
	return int(ps.workerCount.Load())
}

// incrementWorkerCount increments the worker count
func (ps *poolState) incrementWorkerCount() int {
	return int(ps.workerCount.Add(1))
}

// decrementWorkerCount decrements the worker count
func (ps *poolState) decrementWorkerCount() int {
	return int(ps.workerCount.Add(-1))
}

// getQueueDepth returns the current queue depth
func (ps *poolState) getQueueDepth() int {
	return len(ps.jobQueue)
}

// getQueueCapacity returns the queue capacity
func (ps *poolState) getQueueCapacity() int {
	return cap(ps.jobQueue)
}

// getQueueUtilization returns the queue utilization as a percentage (0.0 to 1.0)
func (ps *poolState) getQueueUtilization() float64 {
	capacity := ps.getQueueCapacity()
	if capacity == 0 {
		return 0.0
	}
	return float64(ps.getQueueDepth()) / float64(capacity)
}
