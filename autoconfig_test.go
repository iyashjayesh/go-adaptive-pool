package adaptivepool

import (
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setGOMAXPROCS sets GOMAXPROCS for the test and restores it afterwards
func setGOMAXPROCS(t *testing.T, n int) {
	t.Helper()
	prev := runtime.GOMAXPROCS(n)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
}

func TestDetectSystemResourcesUsesGOMAXPROCSAndMemoryLimit(t *testing.T) {
	setGOMAXPROCS(t, 3)
	prev := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(prev)

	r := detectSystemResources()
	assert.Equal(t, 3, r.numCPU, "should follow GOMAXPROCS, which honours container CPU limits")
	assert.Equal(t, uint64(256<<20), r.memoryLimit, "should use GOMEMLIMIT when set")
}

func TestParseCgroupMemoryLimit(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
	}{
		{"max\n", 0},                 // v2, no limit
		{"9223372036854771712\n", 0}, // v1, no limit
		{"1073741824\n", 1 << 30},    // 1GiB
		{"536870912", 512 << 20},     // no trailing newline
		{"garbage", 0},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseCgroupMemoryLimit(tt.in), "input %q", tt.in)
	}
}

func TestApplyProfile(t *testing.T) {
	res := systemResources{numCPU: 4}
	tests := []struct {
		profile           WorkloadProfile
		minW, maxW, queue int
		threshold         float64
	}{
		{ProfileAPIServer, 8, 40, 800, 0.6},
		{ProfileCPUIntensive, 4, 8, 40, 0.8},
		{ProfileBatchProcessor, 8, 16, 800, 0.7},
	}
	for _, tt := range tests {
		c := defaultConfig()
		applyProfileFor(c, tt.profile, res)
		assert.Equal(t, tt.minW, c.minWorkers, "profile %d min workers", tt.profile)
		assert.Equal(t, tt.maxW, c.maxWorkers, "profile %d max workers", tt.profile)
		assert.Equal(t, tt.queue, c.queueSize, "profile %d queue size", tt.profile)
		assert.Equal(t, tt.threshold, c.scaleUpThreshold, "profile %d threshold", tt.profile)
		assert.NoError(t, c.validate())
	}
}

func TestApplySystemAwareConfigWorkloadTypes(t *testing.T) {
	res := systemResources{numCPU: 4} // no memory limit: default queue sizes
	tests := []struct {
		wt                WorkloadType
		minW, maxW, queue int
		threshold         float64
	}{
		{IOBound, 8, 32, 640, 0.6},
		{CPUBound, 4, 8, 40, 0.8},
		{Mixed, 4, 16, 160, 0.7},
	}
	for _, tt := range tests {
		c := defaultConfig()
		applySystemAwareConfigFor(c, SystemAwareConfig{WorkloadType: tt.wt}, res)
		assert.Equal(t, tt.minW, c.minWorkers, "type %d min workers", tt.wt)
		assert.Equal(t, tt.maxW, c.maxWorkers, "type %d max workers", tt.wt)
		assert.Equal(t, tt.queue, c.queueSize, "type %d queue size", tt.wt)
		assert.Equal(t, tt.threshold, c.scaleUpThreshold, "type %d threshold", tt.wt)
		assert.NoError(t, c.validate())
	}
}

func TestApplySystemAwareConfigSizesQueueFromMemoryLimit(t *testing.T) {
	sys := SystemAwareConfig{WorkloadType: IOBound, AvgJobMemoryBytes: 50 * 1024, MemoryLimitPercent: 0.2}
	// IOBound on 2 CPUs: 16 max workers, so the queue is bounded to [80, 1600]
	tests := []struct {
		name        string
		memoryLimit uint64
		want        int
	}{
		{"fits", 64 << 20, 262}, // 64MiB * 0.2 / 50KiB
		{"floor", 1 << 20, 80},  // 1MiB fits only 4 jobs
		{"cap", 64 << 30, 1600}, // 64GiB fits far more than 100x workers
		{"no limit", 0, 320},    // falls back to the IOBound default, 20x workers
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultConfig()
			applySystemAwareConfigFor(c, sys, systemResources{numCPU: 2, memoryLimit: tt.memoryLimit})
			assert.Equal(t, tt.want, c.queueSize)
		})
	}
}

func TestApplySystemAwareConfigDefaultsMemoryLimitPercent(t *testing.T) {
	res := systemResources{numCPU: 2, memoryLimit: 64 << 20}
	for _, pct := range []float64{0, -1, 1.5} {
		c := defaultConfig()
		applySystemAwareConfigFor(c, SystemAwareConfig{WorkloadType: IOBound, AvgJobMemoryBytes: 50 * 1024, MemoryLimitPercent: pct}, res)
		assert.Equal(t, 262, c.queueSize, "MemoryLimitPercent %v should fall back to 0.2", pct)
	}
}

func TestApplySystemAwareConfigLatencyTarget(t *testing.T) {
	res := systemResources{numCPU: 2}
	tests := []struct {
		latencyMs int
		want      float64
	}{
		{50, 0.5},   // below 100ms: most aggressive
		{300, 0.6},  // below 500ms
		{1000, 0.8}, // a relaxed target keeps the CPUBound default
		{0, 0.8},    // no target
	}
	for _, tt := range tests {
		c := defaultConfig()
		applySystemAwareConfigFor(c, SystemAwareConfig{WorkloadType: CPUBound, TargetLatencyMs: tt.latencyMs}, res)
		assert.Equal(t, tt.want, c.scaleUpThreshold, "latency target %dms", tt.latencyMs)
	}
}

func TestSuggestConfigIsValid(t *testing.T) {
	for _, wt := range []WorkloadType{IOBound, CPUBound, Mixed} {
		c := SuggestConfig(wt)
		require.NotNil(t, c)
		assert.NoError(t, c.validate(), "workload type %d", wt)
		assert.Equal(t, c.minWorkers, c.MinWorkers())
		assert.Equal(t, c.maxWorkers, c.MaxWorkers())
		assert.Equal(t, c.queueSize, c.QueueSize())
	}
}

func TestWithAutoConfigCreatesPool(t *testing.T) {
	for _, profile := range []WorkloadProfile{ProfileAPIServer, ProfileCPUIntensive, ProfileBatchProcessor} {
		p := newTestPool(t, WithLogger(discardLogger()), WithAutoConfig(profile))
		assert.Greater(t, p.Metrics().ActiveWorkers(), 0, "profile %d", profile)
	}
}

// Explicit options override auto-config whatever their order, so placing
// WithMaxWorkers before WithAutoConfig no longer loses it silently.
func TestExplicitOptionsOverrideAutoConfigInAnyOrder(t *testing.T) {
	setGOMAXPROCS(t, 2)
	orders := map[string][]Option{
		"before": {WithMaxWorkers(4), WithMinWorkers(2), WithAutoConfig(ProfileAPIServer)},
		"after":  {WithAutoConfig(ProfileAPIServer), WithMaxWorkers(4), WithMinWorkers(2)},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			p := newTestPool(t, append(opts, WithLogger(discardLogger()))...)
			c := p.(*pool).config
			assert.Equal(t, 4, c.maxWorkers, "explicit max workers")
			assert.Equal(t, 2, c.minWorkers, "explicit min workers")
			assert.Equal(t, 400, c.queueSize, "queue size still comes from the profile (20x 10x2 CPUs)")
			assert.Equal(t, 0.6, c.scaleUpThreshold, "threshold still comes from the profile")
		})
	}
}

func TestLastAutoConfigOptionWins(t *testing.T) {
	setGOMAXPROCS(t, 2)
	p := newTestPool(t, WithLogger(discardLogger()),
		WithAutoConfig(ProfileCPUIntensive),
		WithSystemAwareConfig(SystemAwareConfig{WorkloadType: IOBound}))
	assert.Equal(t, 16, p.(*pool).config.maxWorkers, "IOBound: 8x 2 CPUs")
}

func TestAutoConfigLogsResolvedValues(t *testing.T) {
	setGOMAXPROCS(t, 2)
	logs := &logRecorder{}
	newTestPool(t, WithLogger(logs.logger()), WithAutoConfig(ProfileCPUIntensive), WithQueueSize(100))

	recs := logs.records("auto-config applied")
	require.Len(t, recs, 1)
	assert.EqualValues(t, 2, recs[0]["min_workers"])
	assert.EqualValues(t, 4, recs[0]["max_workers"])
	assert.EqualValues(t, 100, recs[0]["queue_size"], "logs the final value, after explicit options")
	assert.EqualValues(t, 0.8, recs[0]["scale_up_threshold"])

	plain := &logRecorder{}
	newTestPool(t, WithLogger(plain.logger()))
	assert.Empty(t, plain.records("auto-config applied"), "nothing to log without auto-config")
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
