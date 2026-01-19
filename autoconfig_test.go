package adaptivepool

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectSystemResources(t *testing.T) {
	resources := detectSystemResources()

	assert.Greater(t, resources.numCPU, 0, "should detect at least 1 CPU")
	assert.Greater(t, resources.totalMemory, uint64(0), "should detect total memory")
	assert.GreaterOrEqual(t, resources.totalMemory, resources.availableMemory, "total memory should be >= available memory")
}

func TestApplyProfile_APIServer(t *testing.T) {
	config := defaultConfig()
	applyProfile(config, ProfileAPIServer)

	numCPU := runtime.NumCPU()

	assert.Equal(t, numCPU*2, config.minWorkers, "API server should have 2x CPU min workers")
	assert.Equal(t, numCPU*10, config.maxWorkers, "API server should have 10x CPU max workers")
	assert.Equal(t, config.maxWorkers*20, config.queueSize, "API server should have 20x max workers queue size")
	assert.Equal(t, 0.6, config.scaleUpThreshold, "API server should have 0.6 scale up threshold")

	err := config.validate()
	assert.NoError(t, err, "profile config should be valid")
}

func TestApplyProfile_CPUIntensive(t *testing.T) {
	config := defaultConfig()
	applyProfile(config, ProfileCPUIntensive)

	numCPU := runtime.NumCPU()

	assert.Equal(t, numCPU, config.minWorkers, "CPU intensive should have 1x CPU min workers")
	assert.Equal(t, numCPU*2, config.maxWorkers, "CPU intensive should have 2x CPU max workers")
	assert.Equal(t, config.maxWorkers*5, config.queueSize, "CPU intensive should have 5x max workers queue size")
	assert.Equal(t, 0.8, config.scaleUpThreshold, "CPU intensive should have 0.8 scale up threshold")

	err := config.validate()
	assert.NoError(t, err, "profile config should be valid")
}

func TestApplyProfile_BatchProcessor(t *testing.T) {
	config := defaultConfig()
	applyProfile(config, ProfileBatchProcessor)

	numCPU := runtime.NumCPU()

	assert.Equal(t, numCPU*2, config.minWorkers, "Batch processor should have 2x CPU min workers")
	assert.Equal(t, numCPU*4, config.maxWorkers, "Batch processor should have 4x CPU max workers")
	assert.Equal(t, config.maxWorkers*50, config.queueSize, "Batch processor should have 50x max workers queue size")
	assert.Equal(t, 0.7, config.scaleUpThreshold, "Batch processor should have 0.7 scale up threshold")

	err := config.validate()
	assert.NoError(t, err, "profile config should be valid")
}

func TestApplySystemAwareConfig_IOBound(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		TargetLatencyMs:    500,
		AvgJobMemoryBytes:  50 * 1024, // 50KB
		MemoryLimitPercent: 0.2,
	}

	applySystemAwareConfig(config, sysConfig)

	numCPU := runtime.NumCPU()

	assert.Equal(t, numCPU*2, config.minWorkers, "IO bound should have 2x CPU min workers")
	assert.Equal(t, numCPU*8, config.maxWorkers, "IO bound should have 8x CPU max workers")
	assert.Equal(t, 0.6, config.scaleUpThreshold, "IO bound should have 0.6 scale up threshold")
	assert.Greater(t, config.queueSize, 0, "queue size should be positive")

	err := config.validate()
	assert.NoError(t, err, "system aware config should be valid")
}

func TestApplySystemAwareConfig_CPUBound(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       CPUBound,
		TargetLatencyMs:    1000,
		MemoryLimitPercent: 0.3,
	}

	applySystemAwareConfig(config, sysConfig)

	numCPU := runtime.NumCPU()

	assert.Equal(t, numCPU, config.minWorkers, "CPU bound should have 1x CPU min workers")
	assert.Equal(t, numCPU*2, config.maxWorkers, "CPU bound should have 2x CPU max workers")
	assert.Equal(t, 0.8, config.scaleUpThreshold, "CPU bound should have 0.8 scale up threshold")
	assert.Equal(t, config.maxWorkers*5, config.queueSize, "CPU bound should have 5x max workers queue size")

	err := config.validate()
	assert.NoError(t, err, "system aware config should be valid")
}

func TestApplySystemAwareConfig_Mixed(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       Mixed,
		MemoryLimitPercent: 0.25,
	}

	applySystemAwareConfig(config, sysConfig)

	numCPU := runtime.NumCPU()

	assert.Equal(t, numCPU, config.minWorkers, "Mixed should have 1x CPU min workers")
	assert.Equal(t, numCPU*4, config.maxWorkers, "Mixed should have 4x CPU max workers")
	assert.Equal(t, 0.7, config.scaleUpThreshold, "Mixed should have 0.7 scale up threshold")
	assert.Equal(t, config.maxWorkers*10, config.queueSize, "Mixed should have 10x max workers queue size")

	err := config.validate()
	assert.NoError(t, err, "system aware config should be valid")
}

func TestApplySystemAwareConfig_MemoryConstraints(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		AvgJobMemoryBytes:  1024 * 1024, // 1MB per job
		MemoryLimitPercent: 0.1,
	}

	applySystemAwareConfig(config, sysConfig)

	// Queue size should be calculated based on memory
	assert.Greater(t, config.queueSize, 0, "queue size should be positive")
	assert.GreaterOrEqual(t, config.queueSize, config.maxWorkers*5, "queue size should be at least 5x max workers")

	err := config.validate()
	assert.NoError(t, err, "memory-constrained config should be valid")
}

func TestApplySystemAwareConfig_LowLatencyTarget(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		TargetLatencyMs:    50, // Low latency target
		MemoryLimitPercent: 0.2,
	}

	applySystemAwareConfig(config, sysConfig)

	assert.Equal(t, 0.5, config.scaleUpThreshold, "low latency should have aggressive 0.5 scale up threshold")

	err := config.validate()
	assert.NoError(t, err, "low latency config should be valid")
}

func TestApplySystemAwareConfig_MediumLatencyTarget(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		TargetLatencyMs:    200, // Medium latency target
		MemoryLimitPercent: 0.2,
	}

	applySystemAwareConfig(config, sysConfig)

	assert.Equal(t, 0.6, config.scaleUpThreshold, "medium latency should have 0.6 scale up threshold")

	err := config.validate()
	assert.NoError(t, err, "medium latency config should be valid")
}

func TestApplySystemAwareConfig_DefaultMemoryLimit(t *testing.T) {
	config := defaultConfig()
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		MemoryLimitPercent: 0, // Should use default
	}

	applySystemAwareConfig(config, sysConfig)

	// Should not panic and should produce valid config
	err := config.validate()
	assert.NoError(t, err, "config with default memory limit should be valid")
}

func TestSuggestConfig(t *testing.T) {
	tests := []struct {
		name         string
		workloadType WorkloadType
	}{
		{"IOBound", IOBound},
		{"CPUBound", CPUBound},
		{"Mixed", Mixed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := SuggestConfig(tt.workloadType)

			require.NotNil(t, config, "suggested config should not be nil")
			assert.Greater(t, config.minWorkers, 0, "min workers should be positive")
			assert.Greater(t, config.maxWorkers, 0, "max workers should be positive")
			assert.Greater(t, config.queueSize, 0, "queue size should be positive")
			assert.GreaterOrEqual(t, config.maxWorkers, config.minWorkers, "max workers should be >= min workers")

			err := config.validate()
			assert.NoError(t, err, "suggested config should be valid")
		})
	}
}

func TestWithAutoConfig(t *testing.T) {
	tests := []struct {
		name    string
		profile WorkloadProfile
	}{
		{"APIServer", ProfileAPIServer},
		{"CPUIntensive", ProfileCPUIntensive},
		{"BatchProcessor", ProfileBatchProcessor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, err := New(WithAutoConfig(tt.profile))

			require.NoError(t, err, "should create pool with auto config")
			require.NotNil(t, pool, "pool should not be nil")

			metrics := pool.Metrics()
			assert.Greater(t, metrics.ActiveWorkers(), 0, "should have workers")

			// Cleanup
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = pool.Shutdown(ctx)
			assert.NoError(t, err, "should shutdown cleanly")
		})
	}
}

func TestWithSystemAwareConfig(t *testing.T) {
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		TargetLatencyMs:    500,
		AvgJobMemoryBytes:  50 * 1024,
		MemoryLimitPercent: 0.2,
	}

	pool, err := New(WithSystemAwareConfig(sysConfig))

	require.NoError(t, err, "should create pool with system aware config")
	require.NotNil(t, pool, "pool should not be nil")

	metrics := pool.Metrics()
	assert.Greater(t, metrics.ActiveWorkers(), 0, "should have workers")

	// Cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = pool.Shutdown(ctx)
	assert.NoError(t, err, "should shutdown cleanly")
}

func TestAutoConfig_CanOverrideWithOtherOptions(t *testing.T) {
	// Auto config followed by manual override
	pool, err := New(
		WithAutoConfig(ProfileAPIServer),
		WithMinWorkers(5), // Override min workers
	)

	require.NoError(t, err, "should create pool with overridden config")
	require.NotNil(t, pool, "pool should not be nil")

	metrics := pool.Metrics()
	assert.Equal(t, 5, metrics.ActiveWorkers(), "should respect manual override")

	// Cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = pool.Shutdown(ctx)
	assert.NoError(t, err, "should shutdown cleanly")
}

func TestSystemAwareConfig_QueueSizeBounds(t *testing.T) {
	config := defaultConfig()

	// Test with very large memory per job (should hit minimum bound)
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		AvgJobMemoryBytes:  100 * 1024 * 1024, // 100MB per job
		MemoryLimitPercent: 0.1,
	}

	applySystemAwareConfig(config, sysConfig)

	// Should be at minimum bound
	minBound := config.maxWorkers * 5
	assert.GreaterOrEqual(t, config.queueSize, minBound, "should respect minimum queue size bound")

	err := config.validate()
	assert.NoError(t, err, "config should be valid")
}

func BenchmarkApplyProfile(b *testing.B) {
	for i := 0; i < b.N; i++ {
		config := defaultConfig()
		applyProfile(config, ProfileAPIServer)
	}
}

func BenchmarkApplySystemAwareConfig(b *testing.B) {
	sysConfig := SystemAwareConfig{
		WorkloadType:       IOBound,
		TargetLatencyMs:    500,
		AvgJobMemoryBytes:  50 * 1024,
		MemoryLimitPercent: 0.2,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		config := defaultConfig()
		applySystemAwareConfig(config, sysConfig)
	}
}

func BenchmarkDetectSystemResources(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = detectSystemResources()
	}
}
