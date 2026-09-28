package adaptivepool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmitTask(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(1), WithMaxWorkers(1))

	done := make(chan struct{})
	err := p.SubmitTask(context.Background(), Task{
		ID:   "order-1",
		Kind: "send_email",
		Run: func(context.Context) error {
			close(done)
			return nil
		},
	})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not run")
	}
}

func TestSubmitNilJob(t *testing.T) {
	p := newTestPool(t)

	assert.ErrorIs(t, p.Submit(context.Background(), nil), ErrNilJob)
	assert.ErrorIs(t, p.SubmitTask(context.Background(), Task{ID: "x"}), ErrNilJob)
}
