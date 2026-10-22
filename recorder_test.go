package adaptivepool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorderReceivesEvents(t *testing.T) {
	rec := &fakeRecorder{}
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1), WithLogger(discardLogger()), WithRecorder(rec))

	ctx := context.Background()
	require.NoError(t, p.SubmitTask(ctx, Task{Kind: "email", Run: func(context.Context) error { return nil }}))
	require.NoError(t, p.Submit(ctx, func(context.Context) error { return errors.New("x") }))
	require.Eventually(t, func() bool { return len(rec.completedEvents()) == 2 }, 2*time.Second, time.Millisecond)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Equal(t, []string{"email", "default"}, rec.submitted, "empty kinds are normalized")
	assert.ElementsMatch(t, []completedEvent{
		{kind: "email", status: StatusSuccess, attempts: 1},
		{kind: "default", status: StatusError, attempts: 1},
	}, rec.completed)
}

func TestRecorderReceivesRejections(t *testing.T) {
	rec := &fakeRecorder{}
	p, err := New(WithMinWorkers(1), WithMaxWorkers(1), WithQueueSize(1), WithLogger(discardLogger()), WithRecorder(rec))
	require.NoError(t, err)

	release := make(chan struct{})
	task := Task{Kind: "email", Run: func(context.Context) error { <-release; return nil }}
	ctx := context.Background()
	require.NoError(t, p.TrySubmit(ctx, task))
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, p.TrySubmit(ctx, task)) // fills the queue
	assert.ErrorIs(t, p.TrySubmit(ctx, task), ErrQueueFull)

	// A Submit blocked on the full queue is rejected when Shutdown starts
	blocked := make(chan error, 1)
	go func() { blocked <- p.SubmitTask(ctx, task) }()
	time.Sleep(20 * time.Millisecond)
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- p.Shutdown(ctx) }()
	assert.ErrorIs(t, <-blocked, ErrPoolShutdown)
	close(release)
	require.NoError(t, <-shutdownDone)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Equal(t, []string{"email/queue_full", "email/shutdown"}, rec.rejected)
}

// Detach is final: a pool that has shut down no longer reports, so late
// Submit calls can't reach a recorder that has been attached to another pool.
func TestNoEventsAfterDetach(t *testing.T) {
	rec := &fakeRecorder{}
	p, err := New(WithLogger(discardLogger()), WithRecorder(rec))
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))

	assert.ErrorIs(t, p.SubmitTask(context.Background(), Task{Kind: "email", Run: func(context.Context) error { return nil }}), ErrPoolShutdown)
	assert.Equal(t, int64(1), p.Metrics().JobsRejected(), "in-process counters keep counting")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.rejected)
}

func TestAttacherLifecycle(t *testing.T) {
	rec := &fakeRecorder{}
	p, err := New(WithName("orders"), WithMinWorkers(2), WithMaxWorkers(8), WithQueueSize(64),
		WithLogger(discardLogger()), WithRecorder(rec))
	require.NoError(t, err)

	rec.mu.Lock()
	require.NotNil(t, rec.info, "Attach must be called by New")
	assert.Equal(t, PoolInfo{Name: "orders", MinWorkers: 2, MaxWorkers: 8, QueueSize: 64}, *rec.info)
	assert.Equal(t, 2, rec.stats.ActiveWorkers(), "stats reflect the live pool")
	assert.False(t, rec.detached)
	rec.mu.Unlock()

	require.NoError(t, p.Shutdown(context.Background()))
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.True(t, rec.detached, "Shutdown must call Detach")
}

func TestAttachErrorFailsNew(t *testing.T) {
	errRegister := errors.New("duplicate metrics")
	_, err := New(WithRecorder(&fakeRecorder{attachErr: errRegister}))
	assert.ErrorIs(t, err, errRegister)
}

// panicRecorder panics on every event
type panicRecorder struct{ calls atomic.Int64 }

func (r *panicRecorder) JobSubmitted(string) { r.calls.Add(1); panic("JobSubmitted") }
func (r *panicRecorder) JobRejected(string, RejectReason) {
	r.calls.Add(1)
	panic("JobRejected")
}

func (r *panicRecorder) JobCompleted(string, Status, int, time.Duration, time.Duration) {
	r.calls.Add(1)
	panic("JobCompleted")
}
func (r *panicRecorder) JobDropped(string) { r.calls.Add(1); panic("JobDropped") }
func (r *panicRecorder) ScaledUp(int)      { r.calls.Add(1); panic("ScaledUp") }

type panicDetacher struct{ panicRecorder }

func (*panicDetacher) Attach(PoolInfo, Metrics) error { return nil }
func (*panicDetacher) Detach()                        { panic("Detach") }

// A recorder is user code: its panics must not kill workers or callers, and
// only the first one is logged.
func TestRecorderPanicsAreContained(t *testing.T) {
	logs := &logRecorder{}
	rec := &panicRecorder{}
	done := make(chan TaskResult, 2)
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1), WithLogger(logs.logger()), WithRecorder(rec),
		WithOnTaskDone(func(r TaskResult) { done <- r }))

	ctx := context.Background()
	for range 2 {
		require.NoError(t, p.Submit(ctx, func(context.Context) error { return nil }), "JobSubmitted panicked into the caller")
	}
	for range 2 {
		select {
		case r := <-done:
			assert.Equal(t, StatusSuccess, r.Status, "the hook must still run after JobCompleted panics")
		case <-time.After(2 * time.Second):
			t.Fatal("worker died after a recorder panic")
		}
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	assert.ErrorIs(t, p.Submit(canceled, func(context.Context) error { return nil }), context.Canceled)
	p.(*pool).metrics.recordScaleUp(1)

	assert.Equal(t, int64(2), p.Metrics().JobsProcessed())
	assert.Equal(t, int64(6), rec.calls.Load(), "every event still reaches the recorder")
	assert.Len(t, logs.records("recorder panicked; later panics are ignored without logging"), 1)
}

func TestRecorderPanicInDetachDoesNotHangShutdown(t *testing.T) {
	logs := &logRecorder{}
	p, err := New(WithLogger(logs.logger()), WithRecorder(&panicDetacher{}))
	require.NoError(t, err)

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- p.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown hung after Detach panicked")
	}
	require.NoError(t, p.Shutdown(context.Background()), "a second Shutdown must not hang either")
	records := logs.records("recorder panicked; later panics are ignored without logging")
	require.Len(t, records, 1)
	assert.Equal(t, "Detach", records[0]["method"])
}
