package adaptivepool

import (
	"runtime"
)

// WorkloadProfile represents predefined workload types
type WorkloadProfile int

const (
	// ProfileAPIServer is optimized for I/O bound API servers
	ProfileAPIServer WorkloadProfile = iota
	// ProfileCPUIntensive is optimized for CPU-bound tasks
	ProfileCPUIntensive
	// ProfileBatchProcessor is optimized for batch processing workloads
	ProfileBatchProcessor
)

// WorkloadType represents the nature of the workload
type WorkloadType int

const (
	// IOBound workloads spend most time waiting on I/O
	IOBound WorkloadType = iota
	// CPUBound workloads are compute-intensive
	CPUBound
	// Mixed workloads have both I/O and CPU characteristics
	Mixed
)

// SystemAwareConfig provides advanced auto-configuration based on system resources
type SystemAwareConfig struct {
	// WorkloadType specifies the nature of the workload
	WorkloadType WorkloadType
	// TargetLatencyMs is the target latency in milliseconds
	TargetLatencyMs int
	// AvgJobMemoryBytes is the average memory consumed per job
	AvgJobMemoryBytes int64
	// MemoryLimitPercent is the percentage of available memory to use (0.0 to 1.0)
	MemoryLimitPercent float64
}

// systemResources holds detected system resource information
type systemResources struct {
	numCPU          int
	totalMemory     uint64
	availableMemory uint64
}

// detectSystemResources detects available system resources
func detectSystemResources() systemResources {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return systemResources{
		numCPU:          runtime.NumCPU(),
		totalMemory:     memStats.Sys,
		availableMemory: memStats.Sys - memStats.Alloc,
	}
}

// applyProfile applies a predefined workload profile to the config
func applyProfile(config *Config, profile WorkloadProfile) {
	resources := detectSystemResources()
	numCPU := resources.numCPU

	switch profile {
	case ProfileAPIServer:
		// I/O bound: more workers to handle concurrent requests
		config.minWorkers = numCPU * 2
		config.maxWorkers = numCPU * 10
		config.queueSize = config.maxWorkers * 20
		config.scaleUpThreshold = 0.6

	case ProfileCPUIntensive:
		// CPU bound: workers close to CPU count
		config.minWorkers = numCPU
		config.maxWorkers = numCPU * 2
		config.queueSize = config.maxWorkers * 5
		config.scaleUpThreshold = 0.8

	case ProfileBatchProcessor:
		// Batch processing: moderate workers, large queue
		config.minWorkers = numCPU * 2
		config.maxWorkers = numCPU * 4
		config.queueSize = config.maxWorkers * 50
		config.scaleUpThreshold = 0.7
	}
}

// applySystemAwareConfig applies system-aware configuration
func applySystemAwareConfig(config *Config, sysConfig SystemAwareConfig) {
	resources := detectSystemResources()
	numCPU := resources.numCPU

	// Set defaults if not provided
	if sysConfig.MemoryLimitPercent <= 0 || sysConfig.MemoryLimitPercent > 1.0 {
		sysConfig.MemoryLimitPercent = 0.2 // Default to 20% of available memory
	}

	// Calculate worker counts based on workload type
	switch sysConfig.WorkloadType {
	case IOBound:
		config.minWorkers = numCPU * 2
		config.maxWorkers = numCPU * 8
		config.scaleUpThreshold = 0.6

	case CPUBound:
		config.minWorkers = numCPU
		config.maxWorkers = numCPU * 2
		config.scaleUpThreshold = 0.8

	case Mixed:
		config.minWorkers = numCPU
		config.maxWorkers = numCPU * 4
		config.scaleUpThreshold = 0.7
	}

	// Calculate queue size based on memory constraints
	if sysConfig.AvgJobMemoryBytes > 0 {
		availableForQueue := float64(resources.availableMemory) * sysConfig.MemoryLimitPercent
		maxQueueByMemory := int(availableForQueue / float64(sysConfig.AvgJobMemoryBytes))

		// Use memory-based calculation but ensure reasonable bounds
		minQueueSize := config.maxWorkers * 5
		maxQueueSize := config.maxWorkers * 100

		if maxQueueByMemory < minQueueSize {
			config.queueSize = minQueueSize
		} else if maxQueueByMemory > maxQueueSize {
			config.queueSize = maxQueueSize
		} else {
			config.queueSize = maxQueueByMemory
		}
	} else {
		// Default queue sizing based on workload type
		switch sysConfig.WorkloadType {
		case IOBound:
			config.queueSize = config.maxWorkers * 20
		case CPUBound:
			config.queueSize = config.maxWorkers * 5
		case Mixed:
			config.queueSize = config.maxWorkers * 10
		}
	}

	// Adjust scaling behavior based on target latency
	if sysConfig.TargetLatencyMs > 0 {
		// Lower latency targets need more aggressive scaling
		if sysConfig.TargetLatencyMs < 100 {
			config.scaleUpThreshold = 0.5
		} else if sysConfig.TargetLatencyMs < 500 {
			config.scaleUpThreshold = 0.6
		}
	}
}

// SuggestConfig analyzes the system and returns suggested configuration values
func SuggestConfig(workloadType WorkloadType) *Config {
	config := defaultConfig()
	applySystemAwareConfig(config, SystemAwareConfig{
		WorkloadType:       workloadType,
		MemoryLimitPercent: 0.2,
	})
	return config
}
