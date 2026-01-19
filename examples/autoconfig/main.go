package main

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
)

func main() {
	fmt.Println("=== Auto-Configuration Examples ===")

	// Example 1: Simple profile-based configuration
	example1ProfileBased()

	// Example 2: System-aware configuration
	example2SystemAware()

	// Example 3: Suggest configuration
	example3SuggestConfig()

	// Example 4: Manual override with auto-config
	example4ManualOverride()
}

func example1ProfileBased() {
	fmt.Println("Example 1: Profile-Based Auto-Configuration")
	fmt.Println("-------------------------------------------")

	// Create pool with API Server profile
	pool, err := adaptivepool.New(
		adaptivepool.WithAutoConfig(adaptivepool.ProfileAPIServer),
	)
	if err != nil {
		log.Fatalf("Failed to create pool: %v", err)
	}

	metrics := pool.Metrics()
	fmt.Printf("Profile: API Server (I/O Bound)\n")
	fmt.Printf("Initial Workers: %d\n", metrics.ActiveWorkers())
	fmt.Printf("Queue Capacity: %d\n", metrics.QueueCapacity())
	fmt.Printf("System CPUs: %d\n\n", runtime.NumCPU())

	// Submit some jobs
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		err := pool.Submit(ctx, func(_ context.Context) error {
			time.Sleep(100 * time.Millisecond)
			fmt.Printf("  Job %d completed\n", i)
			return nil
		})
		if err != nil {
			log.Printf("Failed to submit job: %v", err)
		}
	}

	// Wait for jobs to complete
	time.Sleep(500 * time.Millisecond)

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}

	fmt.Printf("Jobs Completed: %d\n", metrics.JobsProcessed())
	fmt.Println()
}

func example2SystemAware() {
	fmt.Println("Example 2: System-Aware Configuration")
	fmt.Println("--------------------------------------")

	// Create pool with system-aware configuration
	pool, err := adaptivepool.New(
		adaptivepool.WithSystemAwareConfig(adaptivepool.SystemAwareConfig{
			WorkloadType:       adaptivepool.IOBound,
			TargetLatencyMs:    500,
			AvgJobMemoryBytes:  50 * 1024, // 50KB per job
			MemoryLimitPercent: 0.2,       // Use 20% of available memory
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create pool: %v", err)
	}

	metrics := pool.Metrics()
	fmt.Printf("Workload Type: I/O Bound\n")
	fmt.Printf("Target Latency: 500ms\n")
	fmt.Printf("Initial Workers: %d\n", metrics.ActiveWorkers())
	fmt.Printf("Queue Capacity: %d\n", metrics.QueueCapacity())
	fmt.Printf("System CPUs: %d\n\n", runtime.NumCPU())

	// Submit jobs
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		err := pool.Submit(ctx, func(_ context.Context) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		if err != nil {
			log.Printf("Failed to submit job: %v", err)
		}
	}

	// Wait and observe scaling
	time.Sleep(1 * time.Second)
	fmt.Printf("Workers after load: %d\n", metrics.ActiveWorkers())

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}

	fmt.Printf("Jobs Completed: %d\n", metrics.JobsProcessed())
	fmt.Println()
}

func example3SuggestConfig() {
	fmt.Println("Example 3: Configuration Suggestions")
	fmt.Println("-------------------------------------")

	workloadTypes := []struct {
		name string
		wt   adaptivepool.WorkloadType
	}{
		{"I/O Bound", adaptivepool.IOBound},
		{"CPU Bound", adaptivepool.CPUBound},
		{"Mixed", adaptivepool.Mixed},
	}

	numCPU := runtime.NumCPU()
	fmt.Printf("System CPUs: %d\n\n", numCPU)

	for _, wt := range workloadTypes {
		config := adaptivepool.SuggestConfig(wt.wt)
		fmt.Printf("Suggested config for %s:\n", wt.name)
		fmt.Printf("  Min Workers: %d (%.1fx CPU)\n", config.MinWorkers(), float64(config.MinWorkers())/float64(numCPU))
		fmt.Printf("  Max Workers: %d (%.1fx CPU)\n", config.MaxWorkers(), float64(config.MaxWorkers())/float64(numCPU))
		fmt.Printf("  Queue Size: %d (%.1fx Max Workers)\n", config.QueueSize(), float64(config.QueueSize())/float64(config.MaxWorkers()))
		fmt.Println()
	}
}

func example4ManualOverride() {
	fmt.Println("Example 4: Manual Override with Auto-Config")
	fmt.Println("--------------------------------------------")

	// Start with CPU intensive profile, then override specific settings
	pool, err := adaptivepool.New(
		adaptivepool.WithAutoConfig(adaptivepool.ProfileCPUIntensive),
		adaptivepool.WithMinWorkers(4),  // Override min workers
		adaptivepool.WithQueueSize(500), // Override queue size
	)
	if err != nil {
		log.Fatalf("Failed to create pool: %v", err)
	}

	metrics := pool.Metrics()
	fmt.Printf("Base Profile: CPU Intensive\n")
	fmt.Printf("Overridden Min Workers: %d\n", metrics.ActiveWorkers())
	fmt.Printf("Overridden Queue Capacity: %d\n", metrics.QueueCapacity())
	fmt.Printf("System CPUs: %d\n\n", runtime.NumCPU())

	// Shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}

	fmt.Println("Pool shutdown successfully")
	fmt.Println()
}
