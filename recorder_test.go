package adaptivepool

import (
	"context"
	"errors"
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
	p, err := New(WithLogger(discardLogger()), WithRecorder(rec))
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))

	assert.ErrorIs(t, p.SubmitTask(context.Background(), Task{Kind: "email", Run: func(context.Context) error { return nil }}), ErrPoolShutdown)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Equal(t, []string{"email/shutdown"}, rec.rejected)
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
