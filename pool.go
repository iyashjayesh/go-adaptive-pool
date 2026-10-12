package adaptivepool

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Pool represents a worker pool that can execute jobs concurrently
type Pool interface {
	// Submit submits a job to the pool for execution. If the queue is full
	// it blocks until there is space, ctx is done or the pool shuts down.
	// ctx is also the context the job runs with; use SubmitTask with
	// Task.Context to give the job a different one.
	//
	// It returns ErrNilJob for a nil job, ErrPoolShutdown once Shutdown has
	// started, ErrTimeout if ctx's deadline passes while waiting, and
	// context.Canceled if ctx is cancelled while waiting.
	Submit(ctx context.Context, job Job) error

	// SubmitTask is like Submit but carries a task ID and kind for logs and
	// metrics, and an optional execution context separate from ctx. It
	// returns the same errors as Submit.
	SubmitTask(ctx context.Context, task Task) error

	// TrySubmit is like SubmitTask but never waits: if the queue is full it
	// returns ErrQueueFull. ctx is only the job's context, used when
	// task.Context is nil.
	TrySubmit(ctx context.Context, task Task) error

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
	logger  *slog.Logger
	retrier *retrier // nil when retries are disabled

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

	metrics := newMetrics(state, config.recorder)
	if a, ok := config.recorder.(Attacher); ok {
		info := PoolInfo{Name: config.name, MinWorkers: config.minWorkers, MaxWorkers: config.maxWorkers, QueueSize: config.queueSize}
		if err := a.Attach(info, metrics); err != nil {
			return nil, fmt.Errorf("attaching metrics recorder: %w", err)
		}
	}

	// creating dispatcher context
	dispatcherCtx, dispatcherCancel := context.WithCancel(context.Background())

	p := &pool{
		config:           config,
		state:            state,
		metrics:          metrics,
		logger:           newLogger(config),
		dispatcherCtx:    dispatcherCtx,
		dispatcherCancel: dispatcherCancel,
	}

	if config.retry != nil {
		p.retrier = newRetrier(*config.retry, p.logger, state.forceStop)
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

// newLogger resolves the configured logger and binds the pool name to it
func newLogger(c *Config) *slog.Logger {
	l := c.logger
	if l == nil {
		l = slog.Default()
	}
	if c.name != "" {
		l = l.With(slog.String("pool", c.name))
	}
	return l
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

	for {
		select {
		case <-p.dispatcherCtx.Done():
			// Shutdown initiated
			return

		case <-scaleTicker.C:
			// Check if we need to scale
			p.checkScaling()
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
		case <-p.state.forceStop:
			// Forced shutdown: stop without draining the queue
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
	p.metrics.recordScaleUp(toAdd)
	p.logger.Info("scaled up", slog.Int("added", toAdd), slog.Int("workers", p.state.getWorkerCount()))
}
