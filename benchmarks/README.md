# Benchmarks

Benchmarks for testing throughput, latency, and comparing the pool against other approaches.

## Running benchmarks

To run the standard benchmarks:
```bash
go test -bench=. -benchmem
```

## Comparisons

The `comparison_bench_test.go` file contains tests that specifically check how the adaptive pool performs against a naive "go func()" approach under different types of load (CPU vs Memory focus).

For detailed data-driven logs and side-by-side tables, use the root command instead:
```bash
make run-comparison
```