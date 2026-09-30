package adaptivepool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogsTaskFailure(t *testing.T) {
	var rec logRecorder
	p := newTestPool(t, WithName("emails"), WithLogger(rec.logger()))

	err := p.SubmitTask(context.Background(), Task{
		ID:   "order-42",
		Kind: "send_email",
		Run:  func(context.Context) error { return errors.New("smtp down") },
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(rec.records("task failed")) == 1 }, 2*time.Second, 10*time.Millisecond)
	r := rec.records("task failed")[0]
	assert.Equal(t, "ERROR", r["level"])
	assert.Equal(t, "emails", r["pool"])
	assert.Equal(t, "order-42", r["task_id"])
	assert.Equal(t, "send_email", r["kind"])
	assert.Equal(t, "error", r["status"])
	assert.Equal(t, "smtp down", r["err"])
}

func TestLogsTaskPanicWithStack(t *testing.T) {
	var rec logRecorder
	p := newTestPool(t, WithLogger(rec.logger()))

	require.NoError(t, p.Submit(context.Background(), func(context.Context) error { panic("boom") }))

	require.Eventually(t, func() bool { return len(rec.records("task failed")) == 1 }, 2*time.Second, 10*time.Millisecond)
	r := rec.records("task failed")[0]
	assert.Equal(t, "panic", r["status"])
	assert.Equal(t, "job panicked: boom", r["err"])
	assert.Contains(t, r["stack"], "runJob")
}

func TestLogsShutdownWithPoolName(t *testing.T) {
	var rec logRecorder
	p, err := New(WithName("emails"), WithLogger(rec.logger()))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, p.Shutdown(ctx))

	records := rec.records("initiating pool shutdown")
	require.Len(t, records, 1)
	assert.Equal(t, "emails", records[0]["pool"])
}
