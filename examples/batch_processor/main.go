// Package main provides a batch processing example using the adaptive pool.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iyashjayesh/go-adaptive-pool/adaptivepool"
)

func main() {
	// creating the adaptive pool
	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(4),
		adaptivepool.WithMaxWorkers(64),
		adaptivepool.WithQueueSize(5000),
		adaptivepool.WithScaleUpThreshold(0.6),
		adaptivepool.WithScaleDownIdleDuration(10*time.Second),
		adaptivepool.WithScaleCooldown(2*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to Create pool: %v", err)
	}

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\nReceived interrupt signal, shutting down...")
		cancel()
	}()

	// Generate tasks
	numTasks := 100000
	log.Printf("Processing %d tasks with adaptive worker pool...\n", numTasks)
	log.Println("Watch the worker count adapt to load!")
	log.Println()

	var completed atomic.Int64
	var failed atomic.Int64
	startTime := time.Now()

	// Metrics reporter
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				metrics := pool.Metrics()
				completedCount := completed.Load()
				failedCount := failed.Load()
				elapsed := time.Since(startTime)

				throughput := float64(completedCount) / elapsed.Seconds()

				fmt.Printf("\r[%6.1fs] Workers: %2d | Queue: %4d | Completed: %6d | Failed: %4d | Throughput: %8.0f jobs/sec | Avg Latency: %v",
					elapsed.Seconds(),
					metrics.ActiveWorkers(),
					metrics.QueueDepth(),
					completedCount,
					failedCount,
					throughput,
					metrics.AvgJobLatency(),
				)
			}
		}
	}()

	// Submit tasks
	SubmitStart := time.Now()
	for i := 0; i < numTasks; i++ {
		if ctx.Err() != nil {
			break
		}

		taskID := i
		job := func(ctx context.Context) error {
			// Simulate variable workload
			workDuration := time.Duration(10+rand.Intn(40)) * time.Millisecond //nolint:gosec

			select {
			case <-time.After(workDuration):
				completed.Add(1)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Submit with short timeout to demonstrate backpressure
		SubmitCtx, SubmitCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		if err := pool.Submit(SubmitCtx, job); err != nil {
			failed.Add(1)
			if err != context.Canceled {
				log.Printf("Task %d failed to Submit: %v", taskID, err)
			}
		}
		SubmitCancel()

		// Add slight delay to simulate realistic submission rate
		if i%100 == 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}
	SubmitDuration := time.Since(SubmitStart)

	log.Printf("\n\nAll tasks Submitted in %v", SubmitDuration)
	log.Println("Waiting for tasks to complete...")

	// Wait for completion or timeout
	waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
	defer waitCancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			log.Println("\nTimeout waiting for tasks to complete")
			goto shutdown
		case <-ticker.C:
			metrics := pool.Metrics()
			if metrics.QueueDepth() == 0 && completed.Load()+failed.Load() >= int64(numTasks) {
				goto shutdown
			}
		}
	}

shutdown:
	// Final metrics
	finalMetrics := pool.Metrics()
	totalDuration := time.Since(startTime)

	fmt.Println()
	log.Println("=== Final Statistics ===")
	log.Printf("Total Duration:     %v", totalDuration)
	log.Printf("Tasks Completed:    %d", completed.Load())
	log.Printf("Tasks Failed:       %d", failed.Load())
	log.Printf("Jobs Processed:     %d", finalMetrics.JobsProcessed())
	log.Printf("Jobs Rejected:      %d", finalMetrics.JobsRejected())
	log.Printf("Active Workers:     %d", finalMetrics.ActiveWorkers())
	log.Printf("Queue Depth:        %d", finalMetrics.QueueDepth())
	log.Printf("Avg Job Latency:    %v", finalMetrics.AvgJobLatency())
	log.Printf("Overall Throughput: %.0f jobs/sec", float64(completed.Load())/totalDuration.Seconds())

	// Shutdown pool
	log.Println("\nShutting down pool...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := pool.Shutdown(shutdownCtx); err != nil {
		log.Printf("Pool shutdown error: %v", err)
	} else {
		log.Println("Pool shutdown complete")
	}
}
