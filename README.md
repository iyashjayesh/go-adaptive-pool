# go-adaptive-pool

[![CI](https://github.com/iyashjayesh/go-adaptive-pool/actions/workflows/ci.yml/badge.svg)](https://github.com/iyashjayesh/go-adaptive-pool/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/iyashjayesh/go-adaptive-pool)](https://goreportcard.com/report/github.com/iyashjayesh/go-adaptive-pool)
[![GoDoc](https://godoc.org/github.com/iyashjayesh/go-adaptive-pool/adaptivepool?status.svg)](https://godoc.org/github.com/iyashjayesh/go-adaptive-pool/adaptivepool)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A production-grade adaptive worker pool for Go that handles dynamic scaling, backpressure, metrics and safe shutdown under load. It's built to keep your system stable when traffic spikes by not letting goroutines grow out of control.

## Features

- **Bounded Concurrency**: Fixed queue size prevents unbounded memory growth
- **Explicit Backpressure**: Context-aware blocking when queue is full
- **Adaptive Scaling**: Workers scale up/down based on queue utilization
- **Safe Shutdown**: Graceful draining with deterministic worker cleanup
- **Prometheus Metrics**: Built-in observability for queue depth, throughput, and latency
- **Zero Global State**: Multiple pool instances with isolated metrics
- **Production Ready**: Comprehensive tests with race detector and goroutine leak detection

## Installation

```bash
go get github.com/iyashjayesh/go-adaptive-pool/adaptivepool
```

## Quick Start

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/iyashjayesh/go-adaptive-pool/adaptivepool"
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

**Run the comparison yourself:**
```bash
make run-comparison
```

## Benchmarks

Run standard micro-benchmarks:

```bash
cd benchmarks
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
go test -race -v ./adaptivepool/
```

Run with coverage:

```bash
go test -race -coverprofile=coverage.out ./adaptivepool/
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
- [Benchmarks](benchmarks/) - Performance comparisons

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=iyashjayesh/go-adaptive-pool&type=date&legend=top-left)](https://www.star-history.com/#iyashjayesh/go-adaptive-pool&type=date&legend=top-left)