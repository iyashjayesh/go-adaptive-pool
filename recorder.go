package adaptivepool

import "time"

const defaultKind = "default"

// RejectReason explains why a submission was rejected
type RejectReason string

const (
	// RejectShutdown means the pool was shutting down
	RejectShutdown RejectReason = "shutdown"
	// RejectTimeout means the submit context's deadline passed while waiting for queue space
	RejectTimeout RejectReason = "timeout"
	// RejectCanceled means the submit context was canceled while waiting for queue space
	RejectCanceled RejectReason = "canceled"
)

// PoolInfo describes the pool a recorder is attached to
type PoolInfo struct {
	Name       string
	MinWorkers int
	MaxWorkers int
	QueueSize  int
}

// Recorder receives pool events, decoupling the pool from any metrics
// backend. See the promrecorder and otelrecorder packages for Prometheus
// and OpenTelemetry implementations.
//
// Kinds are normalized before being passed in: an empty kind is reported as
// "default". Methods run on submitting and worker goroutines, so
// implementations must be safe for concurrent use and fast.
type Recorder interface {
	// JobSubmitted is called when a job is accepted into the queue
	JobSubmitted(kind string)
	// JobRejected is called when a submission is rejected
	JobRejected(kind string, reason RejectReason)
	// JobCompleted is called once per executed job with its final status,
	// number of attempts, time spent queued and execution time
	JobCompleted(kind string, status Status, attempts int, queueWait, duration time.Duration)
	// ScaledUp is called when the pool adds workers
	ScaledUp(added int)
}

// Attacher is implemented by recorders that export gauges or need
// registration. New calls Attach before starting the pool, and an error
// fails New. Shutdown calls Detach when it completes. stats can be read at
// any time to report current values such as queue depth.
type Attacher interface {
	Attach(info PoolInfo, stats Metrics) error
	Detach()
}

// WithRecorder sends pool events to r, e.g. a Prometheus or OpenTelemetry
// recorder. The pool's own counters behind Metrics() are always kept.
// Default: none
func WithRecorder(r Recorder) Option {
	return func(c *Config) {
		c.recorder = r
	}
}

func normalizeKind(kind string) string {
	if kind == "" {
		return defaultKind
	}
	return kind
}
