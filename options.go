package adaptivepool

import (
	"log/slog"
	"runtime"
	"time"
)

// Config holds the configuration for the pool
type Config struct {
	minWorkers            int
	maxWorkers            int
	queueSize             int
	scaleUpThreshold      float64
	scaleDownIdleDuration time.Duration
	scaleCooldown         time.Duration
	name                  string
	logger                *slog.Logger
	recorder              Recorder
	onTaskDone            func(TaskResult)
	retry                 *RetryPolicy
}

// Option is a functional option for configuring the pool
type Option func(*Config)

// WithMinWorkers sets the minimum number of workers
// Default: 1
func WithMinWorkers(n int) Option {
	return func(c *Config) {
		c.minWorkers = n
	}
}

// WithMaxWorkers sets the maximum number of workers
// Default: runtime.NumCPU()
func WithMaxWorkers(n int) Option {
	return func(c *Config) {
		c.maxWorkers = n
	}
}

// WithQueueSize sets the bounded queue capacity
// Default: 1000
func WithQueueSize(n int) Option {
	return func(c *Config) {
		c.queueSize = n
	}
}

// WithScaleUpThreshold sets the queue utilization percentage (0.0 to 1.0) that triggers scale-up
// Default: 0.7 (70%)
func WithScaleUpThreshold(pct float64) Option {
	return func(c *Config) {
		c.scaleUpThreshold = pct
	}
}

// WithScaleDownIdleDuration sets the idle duration before scaling down workers
// Default: 30s
func WithScaleDownIdleDuration(d time.Duration) Option {
	return func(c *Config) {
		c.scaleDownIdleDuration = d
	}
}

// WithMetricsEnabled has no effect.
//
// Deprecated: the pool always keeps in-process metrics, available through
// Metrics(). To export them, use WithRecorder, e.g.
// WithRecorder(promrecorder.New(reg)).
func WithMetricsEnabled(bool) Option {
	return func(*Config) {}
}

// WithScaleCooldown sets the minimum duration between scaling operations
// Default: 5s
func WithScaleCooldown(d time.Duration) Option {
	return func(c *Config) {
		c.scaleCooldown = d
	}
}

// WithName names the pool. The name is attached to every log record and
// passed to recorders in PoolInfo, so multiple pools in one process can be
// told apart.
// Default: "" (unnamed)
func WithName(name string) Option {
	return func(c *Config) {
		c.name = name
	}
}

// WithOnTaskDone sets a hook called exactly once per executed task, whatever
// the outcome, and once per queued task dropped by a forced shutdown. It lets application code react to results (dead-letter queues,
// status updates, tracing) since Submit returns before the task runs.
//
// The hook runs synchronously on the worker goroutine, so keep it fast and
// hand off heavy work. A panic in the hook is recovered and logged.
func WithOnTaskDone(fn func(TaskResult)) Option {
	return func(c *Config) {
		c.onTaskDone = fn
	}
}

// WithLogger sets the structured logger used by the pool.
// Default: slog.Default()
func WithLogger(l *slog.Logger) Option {
	return func(c *Config) {
		c.logger = l
	}
}

// defaultConfig returns the default configuration
func defaultConfig() *Config {
	return &Config{
		minWorkers:            1,
		maxWorkers:            runtime.NumCPU(),
		queueSize:             1000,
		scaleUpThreshold:      0.7,
		scaleDownIdleDuration: 30 * time.Second,
		scaleCooldown:         5 * time.Second,
	}
}

// validate validates the configuration
func (c *Config) validate() error {
	if c.minWorkers < 1 {
		return ErrInvalidConfig
	}
	if c.maxWorkers < c.minWorkers {
		return ErrInvalidConfig
	}
	if c.queueSize < 1 {
		return ErrInvalidConfig
	}
	if c.scaleUpThreshold < 0.0 || c.scaleUpThreshold > 1.0 {
		return ErrInvalidConfig
	}
	if c.scaleDownIdleDuration < 0 {
		return ErrInvalidConfig
	}
	if c.scaleCooldown < 0 {
		return ErrInvalidConfig
	}
	if c.retry != nil {
		return c.retry.validate()
	}
	return nil
}
