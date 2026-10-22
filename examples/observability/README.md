# Observability example

Runs a pool named `orders` under synthetic, bursty load with retries enabled,
and serves its Prometheus metrics on `:2112/metrics`. A Prometheus + Grafana
stack with a pre-built dashboard is included.

```bash
# 1. Start the app (from the repo root)
make run-observability

# 2. Start Prometheus and Grafana
cd examples/observability && docker compose up
```

Open Grafana at http://localhost:3000 (anonymous access is enabled) and pick
the **Adaptive Pool** dashboard. Prometheus is on http://localhost:9090.

Metrics are exported with `promrecorder`. If port 2112 is taken by another
process, pick another port and point Prometheus at it:

```bash
make run-observability ARGS="-metrics-addr=:2113"
# then change the target in prometheus.yml to host.docker.internal:2113
```

## What the load does

| Kind | Behaviour | Shows up as |
|---|---|---|
| `send_email` | 10% transient `upstream unavailable` errors, limited to 2 attempts via `ErrorLimits` | retries, `retries_exhausted` |
| `resize_image` | 2% `Permanent` invalid payload errors | `permanent` (never retried) |
| `charge_card` | 15% rate limiting with a `RetryAfter` hint of 200ms | retries honouring the hint |

A second pool, `previews` (4 workers, queue of 8, no retries), receives
interactive and best-effort work, so the rejection panels have data:

| Kind | Behaviour | Shows up as |
|---|---|---|
| `render_preview` | Request-scoped work with a 250ms deadline; 5% of clients disconnect early | rejected `canceled`, completed `canceled` when the deadline passes in the queue |
| `track_event` | Best-effort telemetry, dropped if no slot frees within 5ms | rejected `timeout` during bursts |

Use the dashboard's **Pool** selector to compare the two pools.

Load on `orders` alternates between 50 and 400 tasks/s every 30 seconds, so you can watch
the pool scale up, the queue fill and drain, and queue wait rise during bursts.

## Logs

The app writes JSON logs to stdout. Only failures are logged at the default
level, each with `pool`, `task_id`, `kind`, `status`, `attempts`, `duration`
and `err`. Ship them to Loki (for example with Grafana Alloy) to drill into a
failure spike seen on the dashboard:

```logql
{app="orders"} | json | status != "success" | line_format "{{.task_id}} {{.status}} {{.err}}"
```
