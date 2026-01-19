# go-adaptive-pool

[![CI](https://github.com/iyashjayesh/go-adaptive-pool/actions/workflows/ci.yml/badge.svg)](https://github.com/iyashjayesh/go-adaptive-pool/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/iyashjayesh/go-adaptive-pool)](https://goreportcard.com/report/github.com/iyashjayesh/go-adaptive-pool)
[![GoDoc](https://godoc.org/github.com/iyashjayesh/go-adaptive-pool?status.svg)](https://godoc.org/github.com/iyashjayesh/go-adaptive-pool)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
![Visitors](https://api.visitorbadge.io/api/visitors?path=iyashjayesh%2Fgo-adaptive-pool%20&countColor=%23263759&style=flat)
![GitHub last commit](  https://img.shields.io/github/last-commit/iyashjayesh/go-adaptive-pool)

> `go-adaptive-pool` is a bounded worker pool for Go with an adaptive worker lifecycle and explicit backpressure, designed to keep systems stable under bursty load.

> The goal is not to maximize throughput at all costs, but to prevent unbounded goroutine growth, avoid OOMs, and force overload to be handled explicitly instead of crashing later.

## What this library does

This pool focuses on controlling concurrency and memory usage when job submission can outpace processing.

It provides:

- Bounded concurrency via a fixed-size queue
- Adaptive worker lifecycle. Workers scale up and down based on queue pressure
- Explicit backpressure. When the queue is full, submissions block or fail fast
- Observability via built-in Prometheus metrics
- Safe shutdown with graceful draining and no goroutine leaks

The `adaptive` behavior here is worker lifecycle adaptation, not request-level concurrency control.

## Key features

- *Bounded queue*
    - Fixed queue size to prevent unbounded memory growth.
- *Adaptive worker lifecycle*
    - Workers scale up and down based on queue utilization, within configured limits.
- *Explicit backpressure*
    - When overloaded, submissions block or are rejected. The caller must handle it.
- *Observability*
    - Built-in Prometheus metrics:
        - queue depth
        - throughput
        - latency
- *Safe shutdown*
    - Graceful draining of queued jobs and clean worker shutdown with no leaks.

## Installation

```bash
go get github.com/iyashjayesh/go-adaptive-pool
```

## Quick Start

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/iyashjayesh/go-adaptive-pool"
)

func main() {
    // creating pool
    pool, err := adaptivepool.New(
        adaptivepool.WithMinWorkers(4),
        adaptivepool.WithMaxWorkers(32),
        adaptivepool.WithQueueSize(1000),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer pool.Shutdown(context.Background())

    // Submit job
    job := func(ctx context.Context) error {
        // Your work here
        time.Sleep(100 * time.Millisecond)
        return nil
    }

    if err := pool.Submit(context.Background(), job); err != nil {
        log.Printf("Failed to Submit: %v", err)
    }
}
```

## Configuration Options

### Auto-Configuration (Recommended)

The pool provides intelligent auto-configuration based on system resources and workload profiles:

```go
// Simple profile-based configuration
pool, err := adaptivepool.New(
    adaptivepool.WithAutoConfig(adaptivepool.ProfileAPIServer),
)

// Advanced system-aware configuration
pool, err := adaptivepool.New(
    adaptivepool.WithSystemAwareConfig(adaptivepool.SystemAwareConfig{
        WorkloadType:       adaptivepool.IOBound,
        TargetLatencyMs:    500,
        AvgJobMemoryBytes:  50 * 1024, // 50KB per job
        MemoryLimitPercent: 0.2,       // Use 20% of available memory
    }),
)

// Get configuration suggestions
config := adaptivepool.SuggestConfig(adaptivepool.IOBound)
fmt.Printf("Suggested min workers: %d\n", config.MinWorkers())
fmt.Printf("Suggested max workers: %d\n", config.MaxWorkers())
fmt.Printf("Suggested queue size: %d\n", config.QueueSize())
```

**Available Workload Profiles:**

| Profile | Use Case | Min Workers | Max Workers | Queue Size |
|---------|----------|-------------|-------------|------------|
| `ProfileAPIServer` | I/O bound API servers | 2x CPU | 10x CPU | 20x Max Workers |
| `ProfileCPUIntensive` | CPU-bound tasks | 1x CPU | 2x CPU | 5x Max Workers |
| `ProfileBatchProcessor` | Batch processing | 2x CPU | 4x CPU | 50x Max Workers |

**Workload Types for System-Aware Config:**

- `IOBound` - Tasks that spend most time waiting on I/O (network, disk, databases)
- `CPUBound` - Compute-intensive tasks that max out CPU
- `Mixed` - Workloads with both I/O and CPU characteristics

### Manual Configuration

For fine-grained control, configure parameters manually:

```go
pool, err := adaptivepool.New(
    // Minimum workers (default: 1)
    adaptivepool.WithMinWorkers(4),
    
    // Maximum workers (default: runtime.NumCPU())
    adaptivepool.WithMaxWorkers(32),
    
    // Queue capacity (default: 1000)
    adaptivepool.WithQueueSize(5000),
    
    // Queue % to trigger scale-up (default: 0.7)
    adaptivepool.WithScaleUpThreshold(0.6),
    
    // Idle time before scale-down (default: 30s)
    adaptivepool.WithScaleDownIdleDuration(20*time.Second),
    
    // Cooldown between scaling operations (default: 5s)
    adaptivepool.WithScaleCooldown(3*time.Second),
    
    // Enable/disable metrics (default: true)
    adaptivepool.WithMetricsEnabled(true),
)
```

### Combining Auto-Config with Manual Overrides

You can start with a profile and override specific settings:

```go
pool, err := adaptivepool.New(
    adaptivepool.WithAutoConfig(adaptivepool.ProfileAPIServer),
    adaptivepool.WithMinWorkers(8), // Override min workers
    adaptivepool.WithQueueSize(10000), // Override queue size
)
```

## Backpressure Handling

The pool enforces backpressure when the queue is full:

```go
// Block until capacity is available
ctx := context.Background()
err := pool.Submit(ctx, job)

// Timeout after 5 seconds
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
err := pool.Submit(ctx, job)
if err == adaptivepool.ErrTimeout {
    // Handle overload
}

// Return immediately if full
ctx, cancel := context.WithTimeout(context.Background(), 0)
defer cancel()
err := pool.Submit(ctx, job)
```

## Metrics

Access pool metrics for observability:

```go
metrics := pool.Metrics()

fmt.Printf("Queue Depth: %d\n", metrics.QueueDepth())
fmt.Printf("Active Workers: %d\n", metrics.ActiveWorkers())
fmt.Printf("Jobs Processed: %d\n", metrics.JobsProcessed())
fmt.Printf("Jobs Rejected: %d\n", metrics.JobsRejected())
fmt.Printf("Avg Latency: %v\n", metrics.AvgJobLatency())
```

## Graceful Shutdown

```go
// Shutdown with timeout
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

if err := pool.Shutdown(ctx); err != nil {
    log.Printf("Shutdown error: %v", err)
}
```

Shutdown behavior:
1. Stops accepting new jobs (returns `ErrPoolShutdown`)
2. Drains in-flight jobs within timeout
3. Terminates all workers deterministically
4. Returns error if jobs were dropped

## Examples

### Auto-Configuration

See [examples/autoconfig](examples/autoconfig/main.go) for demonstrations of:
- Profile-based auto-configuration
- System-aware configuration with memory constraints
- Configuration suggestions for different workload types
- Combining auto-config with manual overrides

```bash
cd examples/autoconfig
go run main.go
```

### HTTP Server with Backpressure

See [examples/http_server](examples/http_server/main.go) for a complete HTTP server that:
- Processes background jobs via the pool
- Returns 503 when overloaded
- Exposes metrics endpoint
- Handles graceful shutdown

```bash
cd examples/http_server
go run main.go
```

### Batch Processing

See [examples/batch_processor](examples/batch_processor/main.go) for processing large batches with:
- Adaptive worker scaling
- Real-time progress tracking
- Throughput metrics

```bash
cd examples/batch_processor
go run main.go
```

## Scope and non-goals

This library is intentionally narrow in scope.

It does NOT:

- Perform latency- or error-driven adaptive concurrency control
- Implement AIMD-style or feedback-loop–based limiters
- Replace adaptive limiters like failsafe-go or go-adaptive-limiter

It DOES:

- Enforce hard limits on concurrency and memory usage
- Prevent goroutine explosions under burst load
- Apply backpressure when the system is overloaded
- Make overload visible and explicit instead of failing implicitly

If you already have a well-tuned adaptive limiter controlling request concurrency, a fixed-size worker pool may be sufficient.

## Why this exists

In many real systems, goroutines are cheap individually but unbounded submission is not.

Under traffic spikes, naive patterns often lead to: 
- Runaway goroutine creation
- Unbounded queues
- Memory pressure and OOMs
- Failure modes that only appear under load

This pool enforces limits and predictable behavior by design.

## 1 Million RPS Stress Test

We performed an extreme pressure test (1M RPS target for 30s with 500KB tasks) to compare the adaptive pool against naive goroutine spawning.

| Metric | Naive (No Pool) | Adaptive Pool |
| :--- | :--- | :--- |
| **Peak RAM Usage** | **50.86 GB** | **1.41 GB** |
| **Average Latency** | **2,063 ms** | **161 ms** |
| **Peak Goroutines** | 104,473 | 5,002 |
| **Reliability** | Fails Under Load | Rock Solid |

**Why the Pool Wins:**
Under extreme load, the Naive approach causes a "Goroutine Explosion" and "Memory Bomb" that forces the Go runtime into constant Garbage Collection, leading to unusable 2-second latencies. The `go-adaptive-pool` shields your system by enforcing backpressure and resource caps.

For a detailed deep-dive into this test and the mechanics of the pool, check out the blog post: [Scaling to 1 Million RPS](https://iyashjayesh.github.io/go-adaptive-pool-website/blog/scaling-to-1m-rps/).

**Run the comparison yourself:**
```bash
make run-comparison
```

## Benchmarks

Run standard micro-benchmarks:

```bash
go test -bench=. -benchmem -benchtime=10s
```

Sample micro-benchmark results:
```
BenchmarkPoolThroughput/workers=8-10     500000    2341 ns/op    128 B/op    3 allocs/op
BenchmarkAdaptivePool-10                 300000    3892 ns/op    256 B/op    5 allocs/op
BenchmarkNaiveGoroutines-10              200000    8234 ns/op    512 B/op   12 allocs/op
```

## Design Principles

- **Bounded > Unbounded**: Fixed limits prevent cascading failures
- **Explicit > Implicit**: Backpressure forces correct overload handling
- **Simple APIs > Clever Internals**: Easy to use, hard to misuse
- **Correct Shutdown > Fast Shutdown**: No goroutine leaks, ever
- **Metrics are Mandatory**: Observability is not optional

## Architecture

```
Producer → Submit(ctx) → Bounded Queue → Dispatcher → Adaptive Workers → Job Execution
                              ↓
                         Backpressure
```

Key components:
- **Bounded Queue**: Buffered channel with fixed capacity
- **Dispatcher**: Routes jobs to workers and monitors scaling
- **Workers**: Execute jobs with panic recovery and metrics tracking
- **Scaling Logic**: Adjusts worker count based on queue utilization

## Comparison with Other Libraries

| Feature | go-adaptive-pool | ants | pond | errgroup |
|---------|-----------------|------|------|----------|
| Adaptive Scaling | Yes | No | Yes | No |
| Explicit Backpressure | Yes | Partial | Partial | No |
| Prometheus Metrics | Yes | No | No | No |
| Graceful Shutdown | Yes | Yes | Yes | Yes |
| Context Support | Yes | Partial | Yes | Yes |
| Zero Global State | Yes | No | Yes | Yes |

## Testing

Run tests with race detector:

```bash
go test -race -v ./...
```

Run with coverage:

```bash
go test -race -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Acknowledgments

Inspired by:
- [ants](https://github.com/panjf2000/ants) - High-performance goroutine pool
- [pond](https://github.com/alitto/pond) - Minimalistic worker pool
- [asynq](https://github.com/hibiken/asynq) - Distributed task queue

## Related Documentation

- [Examples](examples/) - Complete working examples
- [Benchmarks](.) - Performance comparisons

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=iyashjayesh/go-adaptive-pool&type=Date)](https://star-history.com/#iyashjayesh/go-adaptive-pool&Date) 
