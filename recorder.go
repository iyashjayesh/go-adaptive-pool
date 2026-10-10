package adaptivepool

import "time"

const defaultKind = "default"

// rejectReason explains why a submission was rejected
type rejectReason string

const (
	rejectShutdown rejectReason = "shutdown"
	rejectTimeout  rejectReason = "timeout"
	rejectCanceled rejectReason = "canceled"
)

// poolInfo describes the pool a recorder is attached to
type poolInfo struct {
	Name       string
	MinWorkers int
	MaxWorkers int
	QueueSize  int
}

// recorder receives pool events. Kinds are already normalized (an empty
// kind is reported as "default"). Methods run on submitting and worker
// goroutines, so implementations must be concurrency-safe and fast.
type recorder interface {
	JobSubmitted(kind string)
	JobRejected(kind string, reason rejectReason)
	JobCompleted(kind string, status Status, attempts int, queueWait, duration time.Duration)
	ScaledUp(added int)
}

// attacher is implemented by recorders that export gauges or need
// registration. Attach is called by New (an error fails New) and Detach at
// the end of Shutdown.
type attacher interface {
	Attach(info poolInfo, stats Metrics) error
	Detach()
}

func normalizeKind(kind string) string {
	if kind == "" {
		return defaultKind
	}
	return kind
}
