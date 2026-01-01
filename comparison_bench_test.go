package adaptivepool_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iyashjayesh/go-adaptive-pool"
)

// BenchmarkNaiveGoroutines benchmarks spawning a goroutine for each job
func BenchmarkNaiveGoroutines(b *testing.B) {
	job := func() {
		time.Sleep(100 * time.Microsecond)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			go job()
		}
	})

	// Note: This doesn't wait for goroutines to complete,
	// which is one of the problems with naive spawning
}

// BenchmarkFixedWorkerPool benchmarks a simple fixed-size worker pool
func BenchmarkFixedWorkerPool(b *testing.B) {
	numWorkers := 8
	jobs := make(chan func(), 1000)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				job()
			}
		}()
	}

	job := func() {
		time.Sleep(100 * time.Microsecond)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jobs <- job
	}

	close(jobs)
	wg.Wait()
}

// BenchmarkAdaptivePool benchmarks our adaptive pool
func BenchmarkAdaptivePool(b *testing.B) {
	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(2),
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
}

// BenchmarkHighLoadNaiveGoroutines benchmarks naive goroutines under high load
func BenchmarkHighLoadNaiveGoroutines(b *testing.B) {
	numJobs := 10000
	for i := 0; i < b.N; i++ {
		var completed atomic.Int32
		for j := 0; j < numJobs; j++ {
			go func() {
				time.Sleep(100 * time.Microsecond)
				completed.Add(1)
			}()
		}
		// Wait for completion (naive approach)
		for completed.Load() < int32(numJobs) {
			time.Sleep(1 * time.Millisecond)
		}
	}
}

// BenchmarkHighLoadFixedPool benchmarks fixed pool under high load
func BenchmarkHighLoadFixedPool(b *testing.B) {
	numJobs := 10000
	for i := 0; i < b.N; i++ {
		jobs := make(chan func(), 1000)
		var wg sync.WaitGroup

		// Start 8 workers
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					job()
				}
			}()
		}

		// Submit jobs
		for j := 0; j < numJobs; j++ {
			jobs <- func() {
				time.Sleep(100 * time.Microsecond)
			}
		}

		close(jobs)
		wg.Wait()
	}
}

// BenchmarkHighLoadAdaptivePool benchmarks adaptive pool under high load
func BenchmarkHighLoadAdaptivePool(b *testing.B) {
	numJobs := 10000
	pool, err := adaptivepool.New(
		adaptivepool.WithMinWorkers(2),
		adaptivepool.WithMaxWorkers(16),
		adaptivepool.WithQueueSize(5000),
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
	}()

	for i := 0; i < b.N; i++ {
		for j := 0; j < numJobs; j++ {
			ctx := context.Background()
			job := func(_ context.Context) error {
				time.Sleep(100 * time.Microsecond)
				return nil
			}
			if err := pool.Submit(ctx, job); err != nil {
				b.Fatal(err)
			}
		}

		// Wait for queue to drain
		for pool.Metrics().QueueDepth() > 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}
}

// BenchmarkMemoryComparison compares memory usage
func BenchmarkMemoryComparison(b *testing.B) {
	b.Run("NaiveGoroutines", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			go func() {
				time.Sleep(1 * time.Microsecond)
			}()
		}
	})

	b.Run("AdaptivePool", func(b *testing.B) {
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
			time.Sleep(1 * time.Microsecond)
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
	})
}

// BenchmarkLatencyComparison compares latency characteristics
func BenchmarkLatencyComparison(b *testing.B) {
	b.Run("FixedPool", func(b *testing.B) {
		jobs := make(chan func(), 100)
		var wg sync.WaitGroup

		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					job()
				}
			}()
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			done := make(chan struct{})
			jobs <- func() {
				time.Sleep(1 * time.Millisecond)
				close(done)
			}
			<-done
		}

		close(jobs)
		wg.Wait()
	})

	b.Run("AdaptivePool", func(b *testing.B) {
		pool, err := adaptivepool.New(
			adaptivepool.WithMinWorkers(8),
			adaptivepool.WithMaxWorkers(16),
			adaptivepool.WithQueueSize(100),
		)
		if err != nil {
			b.Fatal(err)
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = pool.Shutdown(ctx)
		}()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			done := make(chan struct{})
			job := func(_ context.Context) error {
				time.Sleep(1 * time.Millisecond)
				close(done)
				return nil
			}

			ctx := context.Background()
			if err := pool.Submit(ctx, job); err != nil {
				b.Fatal(err)
			}
			<-done
		}
	})
}
