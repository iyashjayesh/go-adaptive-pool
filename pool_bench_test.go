package adaptivepool_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
)

// BenchmarkPoolThroughput benchmarks the pool's throughput with different worker counts
func BenchmarkPoolThroughput(b *testing.B) {
	workerCounts := []int{1, 2, 4, 8, 16, 32}

	for _, workers := range workerCounts {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			pool, err := adaptivepool.New(
				adaptivepool.WithMinWorkers(workers),
				adaptivepool.WithMaxWorkers(workers),
				adaptivepool.WithQueueSize(10000),
			)
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = pool.Shutdown(ctx)
			}()

			job := func(_ context.Context) error {
				// Simulate minimal work
				time.Sleep(100 * time.Microsecond)
				return nil
			}

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					ctx := context.Background()
					if err := pool.Submit(ctx, job); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkPoolLatency benchmarks job latency
func BenchmarkPoolLatency(b *testing.B) {
	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(8),
		adaptivepool.WithMaxWorkers(16),
		adaptivepool.WithQueueSize(1000),
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	job := func(_ context.Context) error {
		time.Sleep(1 * time.Millisecond)
		return nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		if err := pool.Submit(ctx, job); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPoolWithDifferentQueueSizes benchmarks different queue sizes
func BenchmarkPoolWithDifferentQueueSizes(b *testing.B) {
	queueSizes := []int{10, 100, 1000, 10000}

	for _, queueSize := range queueSizes {
		b.Run(fmt.Sprintf("queue=%d", queueSize), func(b *testing.B) {
			pool, err := adaptivepool.New(
				adaptivepool.WithMinWorkers(4),
				adaptivepool.WithMaxWorkers(16),
				adaptivepool.WithQueueSize(queueSize),
			)
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = pool.Shutdown(ctx)
			}()

			job := func(_ context.Context) error {
				time.Sleep(100 * time.Microsecond)
				return nil
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				if err := pool.Submit(ctx, job); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAdaptiveScaling benchmarks the adaptive scaling behavior
func BenchmarkAdaptiveScaling(b *testing.B) {
	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(2),
		adaptivepool.WithMaxWorkers(32),
		adaptivepool.WithQueueSize(5000),
		adaptivepool.WithScaleUpThreshold(0.5),
		adaptivepool.WithScaleCooldown(100*time.Millisecond),
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	job := func(_ context.Context) error {
		time.Sleep(1 * time.Millisecond)
		return nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		if err := pool.Submit(ctx, job); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemoryAllocation benchmarks memory allocations
func BenchmarkMemoryAllocation(b *testing.B) {
	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(8),
		adaptivepool.WithMaxWorkers(16),
		adaptivepool.WithQueueSize(1000),
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	job := func(_ context.Context) error {
		return nil
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		if err := pool.Submit(ctx, job); err != nil {
			b.Fatal(err)
		}
	}
}
