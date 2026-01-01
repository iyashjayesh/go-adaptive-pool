package adaptivepool

import "errors"

var (
	// ErrPoolShutdown is returned when attempting to Submit a job to a shutdown pool
	ErrPoolShutdown = errors.New("pool is shutdown")

	// ErrQueueFull is returned when the job queue is at capacity and cannot accept new jobs
	ErrQueueFull = errors.New("queue is full")

	// ErrTimeout is returned when a job submission times out
	ErrTimeout = errors.New("submission timeout")

	// ErrInvalidConfig is returned when pool configuration is invalid
	ErrInvalidConfig = errors.New("invalid pool configuration")
)
