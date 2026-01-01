// Package main provides a naive simulator that spawns a goroutine for every task.
package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	printBanner()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newStats()

	go startReporter(ctx, cancel, s)

	runSubmission(ctx, s)

	printSummary(s)
}

func printBanner() {
	fmt.Println("=== 1 Million RPS Simulator (NAIVE / NO POOL) ===")
	fmt.Printf("CPU Cores: %d\n", runtime.NumCPU())
	fmt.Println("Target: 1,000,000 RPS for 30 Seconds")
	fmt.Println("Warning: This WILL hit RAM limits immediately!")
}

func printSummary(s *stats) {
	fmt.Println("\n--- BENCHMARK SUMMARY (NAIVE) ---")
	fmt.Printf("Total Tasks Completed: %d\n", s.completed.Load())
	fmt.Printf("Total Tasks Rejected:  0\n")
	fmt.Printf("Peak RAM Usage:        %.2f GB\n", float64(s.peakRSS.Load())/1e9)
	fmt.Printf("Peak Goroutines:       %d\n", s.peakGoro.Load())

	if s.completed.Load() > 0 {
		fmt.Printf(
			"Average Latency:       %.2f ms\n",
			float64(s.totalLatency.Load())/float64(s.completed.Load())/1e6,
		)
	}
}

type stats struct {
	Submitted    atomic.Uint64
	completed    atomic.Uint64
	activeJobs   atomic.Int64
	totalLatency atomic.Int64
	peakRSS      atomic.Uint64
	peakGoro     atomic.Int64
}

func newStats() *stats {
	return &stats{}
}

func startReporter(ctx context.Context, cancel context.CancelFunc, s *stats) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	start := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reportOnce(start, cancel, s)
		}
	}
}

func reportOnce(start time.Time, cancel context.CancelFunc, s *stats) {
	elapsed := time.Since(start).Seconds()
	completed := s.completed.Load()
	active := s.activeJobs.Load()
	numGoro := int64(runtime.NumGoroutine())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	updatePeaks(s, mem.Sys, numGoro)

	avgLat := "0ms"
	if completed > 0 {
		avgLat = fmt.Sprintf(
			"%.2fms",
			float64(s.totalLatency.Load())/float64(completed)/1e6,
		)
	}

	fmt.Printf(
		"[%2.0fs] Goro: %5d | Workers: %4d | RAM: %5.2fGB | RPS (Sub): %8.0f | Completed: %8d | Latency: %s\n",
		elapsed,
		numGoro,
		active,
		float64(mem.Sys)/1e9,
		float64(s.Submitted.Load())/elapsed,
		completed,
		avgLat,
	)

	enforceRAMLimit(mem.Sys, cancel)
}

func updatePeaks(s *stats, rss uint64, goro int64) {
	if rss > s.peakRSS.Load() {
		s.peakRSS.Store(rss)
	}
	if goro > s.peakGoro.Load() {
		s.peakGoro.Store(goro)
	}
}

func enforceRAMLimit(rss uint64, cancel context.CancelFunc) {
	if rss > 12e9 {
		fmt.Println("\n!!! CRITICAL: RAM Usage > 12GB. Stopping to prevent system crash !!!")
		cancel()
	}
}

func runSubmission(ctx context.Context, s *stats) {
	fmt.Println("\nStarting high-pressure submission...")

	const generators = 1000
	var wg sync.WaitGroup
	wg.Add(generators)

	for range generators {
		go SubmitLoop(ctx, s, &wg)
	}

	wg.Wait()
}

func SubmitLoop(ctx context.Context, s *stats, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			s.Submitted.Add(1)
			go work(s)
			time.Sleep(10 * time.Microsecond)
		}
	}
}

func work(s *stats) {
	s.activeJobs.Add(1)
	defer s.activeJobs.Add(-1)

	start := time.Now()

	leak := make([]byte, 500*1024)
	for i := 0; i < len(leak); i += 4096 {
		leak[i] = 1
	}

	time.Sleep(20 * time.Millisecond)

	s.totalLatency.Add(time.Since(start).Nanoseconds())
	s.completed.Add(1)

	_ = leak[0]
}
