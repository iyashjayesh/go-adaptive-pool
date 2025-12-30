package adaptivepool

import (
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
	metricsEnabled        bool
	scaleCooldown         time.Duration
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

// WithMetricsEnabled enables or disables metrics collection
// Default: true
func WithMetricsEnabled(enabled bool) Option {
	return func(c *Config) {
		c.metricsEnabled = enabled
	}
}

// WithScaleCooldown sets the minimum duration between scaling operations
// Default: 5s
func WithScaleCooldown(d time.Duration) Option {
	return func(c *Config) {
		c.scaleCooldown = d
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
		metricsEnabled:        true,
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
	return nil
}
