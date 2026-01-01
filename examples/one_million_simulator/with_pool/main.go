// Package main provides a 1 Million RPS simulator to demonstrate the adaptive pool's behavior.
package main

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	fmt.Println("=== 1 Million RPS Simulator (WITH POOL) ===")
	fmt.Printf("CPU Cores: %d\n", runtime.NumCPU())
	fmt.Println("Target: 1,000,000 RPS for 30 Seconds")
	fmt.Println("Work: 500KB RAM + 20ms processing")

	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(100),
		adaptivepool.WithMaxWorkers(5000), // Increased worker cap for higher throughput
		adaptivepool.WithQueueSize(100000),
		adaptivepool.WithScaleUpThreshold(0.7),               // Scale up when queue is 70% full
		adaptivepool.WithScaleCooldown(500*time.Millisecond), // Wait 500ms between scaling events
	)
	if err != nil {
		log.Fatal(err)
	}

	var (
		Submitted    atomic.Uint64
		completed    atomic.Uint64
		rejected     atomic.Uint64
		totalLatency atomic.Int64
		peakRSS      atomic.Uint64
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stats reporter
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		start := time.Now()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				elapsed := time.Since(start).Seconds()
				sub := Submitted.Load()
				comp := completed.Load()
				reje := rejected.Load()

				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				rss := m.Sys
				if rss > peakRSS.Load() {
					peakRSS.Store(rss)
				}

				metrics := pool.Metrics()
				avgLat := "0ms"
				if comp > 0 {
					avgLat = fmt.Sprintf("%.2fms", float64(totalLatency.Load())/float64(comp)/1e6)
				}

				fmt.Printf("[%2.0fs] Goro: %5d | Workers: %4d | RAM: %5.2fGB | RPS (Sub): %8.0f | Completed: %8d | Rejected: %8d | Latency: %s\n",
					elapsed, runtime.NumGoroutine(), metrics.ActiveWorkers(), float64(rss)/1e9, float64(sub)/elapsed, comp, reje, avgLat)
			}
		}
	}()

	work := func(_ context.Context) error { //nolint:unparam
		start := time.Now()
		leak := make([]byte, 500*1024)
		for i := 0; i < len(leak); i += 4096 {
			leak[i] = 1
		}
		time.Sleep(20 * time.Millisecond)
		totalLatency.Add(time.Since(start).Nanoseconds())
		completed.Add(1)
		_ = leak[0]
		return nil
	}

	fmt.Println("\nStarting high-pressure submission...")

	// Massive number of generators to saturate 1M RPS
	numGenerators := 1000
	var wg sync.WaitGroup
	wg.Add(numGenerators)

	for i := 0; i < numGenerators; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					Submitted.Add(1)
					// Tiny timeout to force extremely fast backpressure
					sCtx, c := context.WithTimeout(context.Background(), 1*time.Microsecond)
					if err := pool.Submit(sCtx, work); err != nil {
						rejected.Add(1)
					}
					c()
				}
			}
		}()
	}

	wg.Wait()

	fmt.Println("\n--- BENCHMARK SUMMARY (ADAPTIVE POOL) ---")
	fmt.Printf("Total Tasks Completed: %d\n", completed.Load())
	fmt.Printf("Total Tasks Rejected:  %d\n", rejected.Load())
	fmt.Printf("Peak RAM Usage:        %.2f GB\n", float64(peakRSS.Load())/1e9)
	fmt.Printf("Peak Goroutines:       %d\n", runtime.NumGoroutine())
	fmt.Printf("Average Latency:       %.2f ms\n", float64(totalLatency.Load())/float64(completed.Load())/1e6)
	_ = pool.Shutdown(context.Background())
}
