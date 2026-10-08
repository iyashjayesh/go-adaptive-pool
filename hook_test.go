package adaptivepool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOnTaskDone(t *testing.T) {
	results := make(chan TaskResult, 3)
	p := newTestPool(t,
		WithMinWorkers(1), WithMaxWorkers(1),
		WithLogger(discardLogger()),
		WithOnTaskDone(func(r TaskResult) { results <- r }),
	)

	errBoom := errors.New("boom")
	ctx := context.Background()
	require.NoError(t, p.SubmitTask(ctx, Task{ID: "ok", Kind: "k", Run: func(context.Context) error { return nil }}))
	require.NoError(t, p.SubmitTask(ctx, Task{ID: "fail", Kind: "k", Run: func(context.Context) error { return errBoom }}))
	require.NoError(t, p.SubmitTask(ctx, Task{ID: "panic", Kind: "k", Run: func(context.Context) error { panic("bad") }}))

	got := map[string]TaskResult{}
	for range 3 {
		select {
		case r := <-results:
			got[r.ID] = r
		case <-time.After(2 * time.Second):
			t.Fatal("hook not called")
		}
	}

	assert.Equal(t, StatusSuccess, got["ok"].Status)
	assert.NoError(t, got["ok"].Err)
	assert.Equal(t, 1, got["ok"].Attempts)
	assert.Equal(t, "k", got["ok"].Kind)

	assert.Equal(t, StatusError, got["fail"].Status)
	assert.ErrorIs(t, got["fail"].Err, errBoom)

	assert.Equal(t, StatusPanic, got["panic"].Status)
	var pe *PanicError
	require.ErrorAs(t, got["panic"].Err, &pe)
	assert.Equal(t, "bad", pe.Value)
}

func TestOnTaskDonePanicDoesNotKillWorker(t *testing.T) {
	var rec logRecorder
	p := newTestPool(t,
		WithMinWorkers(1), WithMaxWorkers(1),
		WithLogger(rec.logger()),
		WithOnTaskDone(func(TaskResult) { panic("hook bug") }),
	)

	for range 2 {
		require.NoError(t, p.Submit(context.Background(), func(context.Context) error { return nil }))
	}
	require.Eventually(t, func() bool { return len(rec.records("OnTaskDone hook panicked")) == 2 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, p.Metrics().ActiveWorkers())
}

func TestStatusCanceledWithoutRetries(t *testing.T) {
	results := make(chan TaskResult, 2)
	p := newTestPool(t,
		WithMinWorkers(1), WithMaxWorkers(1),
		WithLogger(discardLogger()),
		WithOnTaskDone(func(r TaskResult) { results <- r }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, p.SubmitTask(ctx, Task{ID: "canceled", Run: func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}}))
	require.NoError(t, p.SubmitTask(context.Background(), Task{ID: "error", Run: func(context.Context) error {
		return context.Canceled // an inner operation's error while the job context is live
	}}))

	got := map[string]Status{}
	for range 2 {
		r := <-results
		got[r.ID] = r.Status
	}
	assert.Equal(t, StatusCanceled, got["canceled"])
	assert.Equal(t, StatusError, got["error"])
}
