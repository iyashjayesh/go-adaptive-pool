package adaptivepool

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Pool represents a worker pool that can execute jobs concurrently
type Pool interface {
	// Submit Submits a job to the pool for execution
	// Returns ErrPoolShutdown if the pool is shutdown
	// Blocks until the job is enqueued or context is cancelled
	Submit(ctx context.Context, job Job) error

	// Shutdown gracefully shuts down the pool
	// Waits for in-flight jobs to complete within the context timeout
	Shutdown(ctx context.Context) error

	// Metrics returns the pool metrics
	Metrics() Metrics
}

// pool is the concrete implementation of Pool
type pool struct {
	config  *Config
	state   *poolState
	metrics *metrics

	// Dispatcher coordination
	dispatcherWg     sync.WaitGroup
	dispatcherCtx    context.Context
	dispatcherCancel context.CancelFunc
}

// New Creates a new adaptive worker pool
func New(options ...Option) (Pool, error) {
	// Apply options to default config
	config := defaultConfig()
	for _, opt := range options {
		opt(config)
	}

	// Validate configuration
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// creating pool state
	state := newPoolState(config.queueSize)

	// creating metrics
	metrics := newMetrics(state, config.metricsEnabled, "adaptivepool")

	// creating dispatcher context
	dispatcherCtx, dispatcherCancel := context.WithCancel(context.Background())

	p := &pool{
		config:           config,
		state:            state,
		metrics:          metrics,
		dispatcherCtx:    dispatcherCtx,
		dispatcherCancel: dispatcherCancel,
	}

	// Start the dispatcher
	p.dispatcherWg.Add(1)
	go p.runDispatcher()

	// Start minimum workers
	for i := 0; i < config.minWorkers; i++ {
		p.spawnWorker()
	}

	return p, nil
}

// Metrics returns the pool metrics
func (p *pool) Metrics() Metrics {
	return p.metrics
}

// spawnWorker spawns a new worker
func (p *pool) spawnWorker() {
	workerID := p.state.incrementWorkerCount()
	p.state.workerWg.Add(1)
	go p.runWorker(workerID)
}

// runDispatcher is the main dispatcher loop
func (p *pool) runDispatcher() {
	defer p.dispatcherWg.Done()

	scaleTicker := time.NewTicker(1 * time.Second)
	defer scaleTicker.Stop()

	metricsTicker := time.NewTicker(5 * time.Second)
	defer metricsTicker.Stop()

	for {
		select {
		case <-p.dispatcherCtx.Done():
			// Shutdown initiated
			return

		case <-scaleTicker.C:
			// Check if we need to scale
			p.checkScaling()

		case <-metricsTicker.C:
			// Update gauge metrics
			p.metrics.updateGauges()
		}
	}
}

// runWorker is the worker loop
func (p *pool) runWorker(id int) {
	defer p.state.workerWg.Done()
	defer p.state.decrementWorkerCount()

	idleTimer := time.NewTimer(p.config.scaleDownIdleDuration)
	defer idleTimer.Stop()

	for {
		select {
		case <-p.state.workerShutdown:
			// Shutdown signal received
			return

		case job, ok := <-p.state.jobQueue:
			if !ok {
				// Queue closed, shutdown
				return
			}

			// Reset idle timer
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(p.config.scaleDownIdleDuration)

			// Execute job
			p.executeJob(id, job)

		case <-idleTimer.C:
			// Worker has been idle, check if we should terminate
			currentWorkers := p.state.getWorkerCount()
			if currentWorkers > p.config.minWorkers {
				// We can terminate this worker
				return
			}
			// Reset timer if we're at minimum
			idleTimer.Reset(p.config.scaleDownIdleDuration)
		}
	}
}

// executeJob executes a job with panic recovery and metrics
func (p *pool) executeJob(workerID int, job jobWrapper) {
	start := time.Now()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("worker %d: panic executing job %d: %v", workerID, job.id, r)
		}

		// Record metrics
		latency := time.Since(start)
		p.metrics.recordJobProcessed(latency)
	}()

	// Execute the job
	if err := job.job(job.ctx); err != nil {
		log.Printf("worker %d: job %d failed: %v", workerID, job.id, err)
	}
}

// checkScaling checks if we need to scale workers up or down
func (p *pool) checkScaling() {
	utilization := p.state.getQueueUtilization()
	currentWorkers := p.state.getWorkerCount()

	// Check if we need to scale up
	if utilization >= p.config.scaleUpThreshold && currentWorkers < p.config.maxWorkers {
		p.scaleUp()
	}
}

// scaleUp scales up the number of workers
func (p *pool) scaleUp() {
	p.state.scalingMu.Lock()
	defer p.state.scalingMu.Unlock()

	// Check cooldown
	if time.Since(p.state.lastScaleUp) < p.config.scaleCooldown {
		return
	}

	currentWorkers := p.state.getWorkerCount()
	if currentWorkers >= p.config.maxWorkers {
		return
	}

	// Scale up by 25% or at least 1 worker
	toAdd := max(1, currentWorkers/4)
	toAdd = min(toAdd, p.config.maxWorkers-currentWorkers)

	for i := 0; i < toAdd; i++ {
		p.spawnWorker()
	}

	p.state.lastScaleUp = time.Now()
	log.Printf("scaled up: added %d workers (total: %d)", toAdd, p.state.getWorkerCount())
}
