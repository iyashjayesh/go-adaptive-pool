package adaptivepool

import (
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Metrics provides observability into pool behavior
type Metrics interface {
	// QueueDepth returns the current number of jobs in the queue
	QueueDepth() int

	// QueueCapacity returns the maximum capacity of the queue
	QueueCapacity() int

	// ActiveWorkers returns the current number of active workers
	ActiveWorkers() int

	// BusyWorkers returns the number of workers currently executing a job
	BusyWorkers() int

	// JobsProcessed returns the total number of jobs processed
	JobsProcessed() int64

	// JobsFailed returns the number of processed jobs that ended with an error or panic
	JobsFailed() int64

	// JobsRejected returns the total number of jobs rejected
	JobsRejected() int64

	// JobsDropped returns the number of queued jobs a forced shutdown discarded without running
	JobsDropped() int64

	// JobsRetried returns the number of processed jobs that needed at least one retry
	JobsRetried() int64

	// RetryAttempts returns the total number of retries across all jobs
	RetryAttempts() int64

	// AvgJobLatency returns the average job execution time
	AvgJobLatency() time.Duration

	// AvgQueueWait returns the average time jobs spent queued before execution
	AvgQueueWait() time.Duration
}

// metrics implements Metrics with always-on atomic counters and forwards
// events to an optional recorder
type metrics struct {
	jobsProcessed  atomic.Int64
	jobsFailed     atomic.Int64
	jobsRejected   atomic.Int64
	jobsDropped    atomic.Int64
	jobsRetried    atomic.Int64
	retryAttempts  atomic.Int64
	totalLatency   atomic.Int64 // in nanoseconds
	totalQueueWait atomic.Int64 // in nanoseconds

	// Reference to pool state for current values
	state *poolState

	rec      Recorder    // nil when no recorder is configured
	detached atomic.Bool // set when the recorder is detached; later events are dropped

	logger        *slog.Logger
	panicReported atomic.Bool // only the first recorder panic is logged
}

func newMetrics(state *poolState, rec Recorder, logger *slog.Logger) *metrics {
	return &metrics{state: state, rec: rec, logger: logger}
}

// QueueDepth returns the current queue depth
func (m *metrics) QueueDepth() int {
	return m.state.getQueueDepth()
}

// QueueCapacity returns the maximum capacity of the queue
func (m *metrics) QueueCapacity() int {
	return m.state.getQueueCapacity()
}

// ActiveWorkers returns the current number of active workers
func (m *metrics) ActiveWorkers() int {
	return m.state.getWorkerCount()
}

// BusyWorkers returns the number of workers currently executing a job
func (m *metrics) BusyWorkers() int {
	return int(m.state.busyWorkers.Load())
}

// JobsProcessed returns the total number of jobs processed
func (m *metrics) JobsProcessed() int64 {
	return m.jobsProcessed.Load()
}

// JobsFailed returns the number of processed jobs that did not succeed
func (m *metrics) JobsFailed() int64 {
	return m.jobsFailed.Load()
}

// JobsRejected returns the total number of jobs rejected
func (m *metrics) JobsRejected() int64 {
	return m.jobsRejected.Load()
}

// JobsRetried returns the number of jobs that needed at least one retry
func (m *metrics) JobsRetried() int64 {
	return m.jobsRetried.Load()
}

// RetryAttempts returns the total number of retries across all jobs
func (m *metrics) RetryAttempts() int64 {
	return m.retryAttempts.Load()
}

// JobsDropped returns the number of queued jobs dropped by a forced shutdown
func (m *metrics) JobsDropped() int64 {
	return m.jobsDropped.Load()
}

// AvgJobLatency returns the average job execution time
func (m *metrics) AvgJobLatency() time.Duration {
	return average(m.totalLatency.Load(), m.jobsProcessed.Load())
}

// AvgQueueWait returns the average time jobs spent queued
func (m *metrics) AvgQueueWait() time.Duration {
	return average(m.totalQueueWait.Load(), m.jobsProcessed.Load())
}

func average(totalNs, n int64) time.Duration {
	if n == 0 {
		return 0
	}
	return time.Duration(totalNs / n)
}

// recoverRecorder is deferred around every recorder call: a recorder that
// panics must not kill a worker or the caller, or leave Shutdown hanging.
// Only the first panic is logged, so a recorder that always panics doesn't
// flood the log.
func (m *metrics) recoverRecorder(method string) {
	if r := recover(); r != nil && m.panicReported.CompareAndSwap(false, true) {
		m.logger.Error("recorder panicked; later panics are ignored without logging",
			slog.String("method", method),
			slog.Any("panic", r),
			slog.String("stack", string(debug.Stack())),
		)
	}
}

// detach stops reporting to the recorder and detaches it
func (m *metrics) detach() {
	m.detached.Store(true)
	if a, ok := m.rec.(Attacher); ok {
		m.detachRecorder(a)
	}
}

func (m *metrics) detachRecorder(a Attacher) {
	defer m.recoverRecorder("Detach")
	a.Detach()
}

// recordJobSubmitted records a job accepted into the queue
func (m *metrics) recordJobSubmitted(kind string) {
	if m.rec != nil && !m.detached.Load() {
		m.reportSubmitted(kind)
	}
}

func (m *metrics) reportSubmitted(kind string) {
	defer m.recoverRecorder("JobSubmitted")
	m.rec.JobSubmitted(normalizeKind(kind))
}

// recordJobRejected records a rejected job
func (m *metrics) recordJobRejected(kind string, reason RejectReason) {
	m.jobsRejected.Add(1)
	if m.rec != nil && !m.detached.Load() {
		m.reportRejected(kind, reason)
	}
}

func (m *metrics) reportRejected(kind string, reason RejectReason) {
	defer m.recoverRecorder("JobRejected")
	m.rec.JobRejected(normalizeKind(kind), reason)
}

// recordJobDone records a finished job. Retry counters are derived from the
// attempt count here rather than per retry, keeping the retry loop free of
// metric updates.
func (m *metrics) recordJobDone(kind string, status Status, attempts int, queueWait, duration time.Duration) {
	m.jobsProcessed.Add(1)
	m.totalLatency.Add(int64(duration))
	m.totalQueueWait.Add(int64(queueWait))
	if status != StatusSuccess {
		m.jobsFailed.Add(1)
	}
	if retries := attempts - 1; retries > 0 {
		m.jobsRetried.Add(1)
		m.retryAttempts.Add(int64(retries))
	}
	if m.rec != nil && !m.detached.Load() {
		m.reportCompleted(kind, status, attempts, queueWait, duration)
	}
}

func (m *metrics) reportCompleted(kind string, status Status, attempts int, queueWait, duration time.Duration) {
	defer m.recoverRecorder("JobCompleted")
	m.rec.JobCompleted(normalizeKind(kind), status, attempts, queueWait, duration)
}

// recordJobDropped records a queued job discarded by a forced shutdown. It
// isn't counted as processed, so it doesn't skew the latency averages.
func (m *metrics) recordJobDropped(kind string) {
	m.jobsDropped.Add(1)
	if m.rec != nil && !m.detached.Load() {
		m.reportDropped(kind)
	}
}

func (m *metrics) reportDropped(kind string) {
	defer m.recoverRecorder("JobDropped")
	m.rec.JobDropped(normalizeKind(kind))
}

// recordScaleUp records a scale-up event
func (m *metrics) recordScaleUp(added int) {
	if m.rec != nil && !m.detached.Load() {
		m.reportScaleUp(added)
	}
}

func (m *metrics) reportScaleUp(added int) {
	defer m.recoverRecorder("ScaledUp")
	m.rec.ScaledUp(added)
}
