# Examples

This folder has some practical ways to use the adaptive pool.

### 1. Auto-Configuration (/autoconfig)
Demonstrates intelligent auto-configuration based on system resources.
- Profile-based configuration for common workload types (API server, CPU intensive, batch processing).
- System-aware configuration with memory constraints and latency targets.
- Configuration suggestions for different workload types.
- Combining auto-config with manual overrides.

### 2. HTTP Server (/http_server)
Shows how to handle background jobs in a web server.
- Uses backpressure to return a 503 instead of crashing when overloaded.
- Includes a /metrics endpoint for Prometheus.
- Handles graceful shutdown so no jobs are lost.

### 3. Batch Processor (/batch_processor)
Good for data processing scripts or background workers.
- Processes 100,000 tasks using adaptive scaling.
- Prints out throughput and latency as it goes.

### 4. Stress Test Simulator (/one_million_simulator)
The heavy-duty simulation used for the 1M RPS benchmarks.
- with-pool: runs with protection enabled.
- without-pool: runs the naive version (caution: uses lots of RAM).

---

## Running the examples

You can use the Makefile in the root directory to run these:

```bash
make run-autoconfig
make run-http
make run-batch
make run-comparison
```

Or run them directly:

```bash
cd autoconfig && go run main.go
cd http_server && go run main.go
cd batch_processor && go run main.go
cd one_million_simulator/with_pool && go run main.go
```