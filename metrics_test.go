package adaptivepool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBusyWorkers(t *testing.T) {
	p := newTestPool(t, WithMinWorkers(2), WithMaxWorkers(2))

	release := make(chan struct{})
	for range 2 {
		require.NoError(t, p.Submit(context.Background(), func(context.Context) error {
			<-release
			return nil
		}))
	}
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 2 }, 2*time.Second, 10*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return p.Metrics().BusyWorkers() == 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Positive(t, p.Metrics().AvgQueueWait()+p.Metrics().AvgJobLatency())
}

// WithMetricsEnabled is kept as a deprecated no-op so existing callers keep
// compiling; in-process metrics are always on.
func TestWithMetricsEnabledIsANoOp(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		p := newTestPool(t, WithLogger(discardLogger()), WithMetricsEnabled(enabled))
		require.NoError(t, p.Submit(context.Background(), func(context.Context) error { return nil }))
		require.Eventually(t, func() bool { return p.Metrics().JobsProcessed() == 1 }, 2*time.Second, time.Millisecond)
	}
}
