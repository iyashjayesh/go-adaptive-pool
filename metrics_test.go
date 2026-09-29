package adaptivepool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	p, err := New(WithName("emails"), WithMinWorkers(2), WithMaxWorkers(4), WithQueueSize(10), WithPrometheusRegisterer(reg))
	require.NoError(t, err)

	expected := `
# HELP adaptivepool_active_workers Current number of active workers
# TYPE adaptivepool_active_workers gauge
adaptivepool_active_workers{pool="emails"} 2
# HELP adaptivepool_max_workers Configured maximum number of workers
# TYPE adaptivepool_max_workers gauge
adaptivepool_max_workers{pool="emails"} 4
# HELP adaptivepool_queue_capacity Capacity of the job queue
# TYPE adaptivepool_queue_capacity gauge
adaptivepool_queue_capacity{pool="emails"} 10
`
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"adaptivepool_active_workers", "adaptivepool_max_workers", "adaptivepool_queue_capacity"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, p.Shutdown(ctx))

	families, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, families, "metrics should be unregistered on shutdown")
}

func TestPrometheusDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	newTestPool(t, WithName("a"), WithPrometheusRegisterer(reg))

	_, err := New(WithName("a"), WithPrometheusRegisterer(reg))
	require.Error(t, err)

	// A distinct name can share the registry
	newTestPool(t, WithName("b"), WithPrometheusRegisterer(reg))
}

func TestMetricsDisabledRegistersNothing(t *testing.T) {
	reg := prometheus.NewRegistry()
	newTestPool(t, WithMetricsEnabled(false), WithPrometheusRegisterer(reg))

	families, err := reg.Gather()
	require.NoError(t, err)
	assert.Empty(t, families)
}
