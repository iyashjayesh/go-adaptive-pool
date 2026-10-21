# Retries and Observability

This guide covers task identity, structured logging, metrics (Prometheus,
OpenTelemetry or your own backend), the `OnTaskDone` hook, and the retry engine. Each feature comes with its use case
and a code sample.

- [Design goals](#design-goals)
- [Task identity: `SubmitTask`](#task-identity-submittask)
- [Structured logging](#structured-logging)
- [Metrics](#metrics)
- [In-process metrics](#in-process-metrics)
- [The `OnTaskDone` hook](#the-ontaskdone-hook)
- [Retries](#retries)
- [Status reference](#status-reference)
- [Grafana dashboard](#grafana-dashboard)
- [Performance](#performance)

---

## Design goals

The pool is meant to be lightweight, so every feature here follows a few rules:

- **Everything runs inline on the worker.** Retries don't re-queue jobs and
  don't start goroutines or timers in the background.
- **You only pay for what you enable.** With retries, the hook and debug
  logging all turned off, the execution path costs what it did before.
- **The hot path doesn't allocate.** Successful and failed jobs run with zero
  heap allocations, and so does the retry logic itself. There are two
  exceptions: the `*RetryError` returned when retrying gives up, and each
  backoff wait longer than zero, which creates a timer (3 allocations, 248
  bytes) through `time.After`. That wait then lasts milliseconds, so the
  allocation is negligible next to it. A test enforces the zero-allocation
  paths.
- **Observability can't blow up memory.** Metric labels only use bounded
  values (pool name, task kind, status, rejection reason). Unique IDs only
  appear in logs and in the hook.

---

## Task identity: `SubmitTask`

`Submit(ctx, job)` still works. `SubmitTask` lets you attach identity to a job:

```go
err := pool.SubmitTask(ctx, adaptivepool.Task{
    ID:   "order-8812",   // unique per task: appears in logs and TaskResult only
    Kind: "send_email",   // task type: used as a metric label
    Run:  sendEmail,
})
```

| Field | Use it for | Where it shows up |
|---|---|---|
| `ID` | Finding one specific task: order ID, request ID, message ID | Logs (`task_id`), `TaskResult.ID` |
| `Kind` | Grouping tasks of the same type | Metric label `kind`, logs, `TaskResult.Kind` |
| `Context` | The context the job runs with, if different from the submit context | Passed to `Run`; drives retry cancellation |

> **Keep `Kind` bounded.** Every distinct kind creates new series in your
> metrics backend.
> Use a fixed set like `"send_email"` or `"resize_image"`, never a user ID or
> URL. Tasks without a kind are reported as `kind="default"`.

### Submit context vs execution context

The `ctx` passed to `Submit`/`SubmitTask` bounds **how long to wait for queue
space** under backpressure. By default it is also **the context the job runs
with**. Those are often different lifetimes:

| You want | Use |
|---|---|
| The job to stop when the caller gives up (request-scoped work) | `Submit(ctx, job)`: one context for both |
| A bounded wait for a slot, without that deadline cancelling the job | `Task.Context` |
| The job to outlive the request that submitted it | `Task.Context: context.WithoutCancel(r.Context())` |

```go
func handler(w http.ResponseWriter, r *http.Request) {
    // Wait at most 50ms for a slot; respond 503 if the pool is saturated
    submitCtx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
    defer cancel()

    err := pool.SubmitTask(submitCtx, adaptivepool.Task{
        Kind:    "send_email",
        Context: context.WithoutCancel(r.Context()), // keeps trace IDs, not cancellation
        Run:     sendEmail,
    })
    if errors.Is(err, adaptivepool.ErrTimeout) {
        http.Error(w, "busy, retry later", http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusAccepted)
}
```

Without `Task.Context`, `defer cancel()` here would cancel the job as soon as
the handler returns, and the job would finish as `canceled`.

`WithoutCancel` keeps the values (trace and request IDs) but drops the
deadline. To bound the job itself, apply the timeout inside `Run`, where the
`cancel` can be deferred:

```go
Run: func(ctx context.Context) error {
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    return sendEmail(ctx)
},
```

A `ctx` that is already done is rejected up front when it is also the job's
context (`Task.Context` is nil), since the job would be cancelled before it
ran. With `Task.Context` set, `ctx` only bounds the wait, so the task is still
queued when there is room.

To never wait, use `TrySubmit`. It returns `ErrQueueFull` when the queue has
no space, which suits best-effort work such as telemetry:

```go
err := pool.TrySubmit(ctx, adaptivepool.Task{Kind: "track_event", Run: track})
if errors.Is(err, adaptivepool.ErrQueueFull) {
    droppedEvents.Add(1)
}
```

Its `ctx` is only the job's context (when `Task.Context` is nil), since there
is no wait to bound; a done one is rejected up front.

### Submit errors

A nil error means the task was queued; how it ends is reported later through
logs, metrics and `OnTaskDone`. Otherwise `Submit`, `SubmitTask` and
`TrySubmit` return:

| Error | When | Rejection reason |
|---|---|---|
| `ErrNilJob` | `Run` is nil | not counted |
| `ErrPoolShutdown` | `Shutdown` has started, before or while waiting for space | `shutdown` |
| `ErrTimeout` | The submit context's deadline passed: while waiting for space, or before the call when it is also the job's context | `timeout` |
| `context.Canceled` | The submit context was cancelled: while waiting for space, or before the call when it is also the job's context | `canceled` |
| `ErrQueueFull` | `TrySubmit` only: the queue was full | `queue_full` |

`ErrTimeout` wraps `context.DeadlineExceeded`, so `errors.Is` matches either one,
and `err == adaptivepool.ErrTimeout` still works. `Submit` and `SubmitTask` wait
for space while the queue is full, so give them a context with a deadline to
bound the wait.

---

## Structured logging

The pool logs through `log/slog`. By default it uses `slog.Default()`, which
keeps the previous behaviour of writing wherever the standard logger goes.

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

pool, _ := adaptivepool.New(
    adaptivepool.WithName("orders"),   // adds pool="orders" to every record
    adaptivepool.WithLogger(logger),
)
```

| Event | Level | Message |
|---|---|---|
| Task finished with an error or panic | `ERROR` | `task failed` |
| A retry is about to wait | `DEBUG` | `retrying task` |
| `OnTaskDone` hook panicked | `ERROR` | `OnTaskDone hook panicked` |
| Scale-up | `INFO` | `scaled up` |
| Shutdown progress | `INFO`, or `WARN` when forced | `initiating pool shutdown`, ... |

`task failed` records include `pool`, `worker`, `task_id`, `kind`, `status`,
`attempts`, `duration` and `err`, plus `stack` for panics:

```json
{"level":"ERROR","msg":"task failed","pool":"orders","worker":4,"task_id":"send_email-42",
 "kind":"send_email","status":"retries_exhausted","attempts":2,"duration":54238958,
 "err":"retries exhausted after 2 attempt(s): upstream unavailable"}
```

**Use cases**

- **Turn on per-retry visibility while debugging:** set the handler level to
  `slog.LevelDebug`. At the default level the retry log is skipped before any
  attributes are built, so it costs nothing.
- **Correlate with traces:** records are logged with the job's context, so a
  handler that pulls trace or request IDs from the context will tag pool logs
  automatically.
- **Silence the pool:** `adaptivepool.WithLogger(slog.New(slog.DiscardHandler))`.
- **Find failures in Grafana:** ship the logs to Loki and filter, for example
  `{app="orders"} | json | status="retries_exhausted"`.

Successful tasks are deliberately **not** logged. At high throughput, a log
line per task costs far more than a counter. Use metrics for rates and
latencies, and logs for the details of failures.

---

## Metrics

The pool doesn't depend on any metrics backend. It reports events (job
submitted, rejected, completed, workers added) to a **`Recorder`**, and you
pick how they're exported:

| You use | Recorder | Import |
|---|---|---|
| Prometheus | `promrecorder.New(registerer)` | `github.com/iyashjayesh/go-adaptive-pool/promrecorder` |
| OpenTelemetry (and any vendor it exports to: Datadog, New Relic, Grafana Cloud, ...) | `otelrecorder.New(meterProvider)` | `github.com/iyashjayesh/go-adaptive-pool/otelrecorder` |
| Something else (StatsD, CloudWatch, expvar, ...) | your own `Recorder` | - |
| Nothing | none: the default | - |

Without a recorder no metrics backend runs at all. The pool's own counters
behind [`Metrics()`](#in-process-metrics) are always kept.

`WithName` is passed to the recorder, so every metric carries the pool name.
A recorder serves one pool at a time; create one per pool.

### Prometheus

```bash
go get github.com/iyashjayesh/go-adaptive-pool/promrecorder
```

```go
reg := prometheus.NewRegistry() // or prometheus.DefaultRegisterer
pool, err := adaptivepool.New(
    adaptivepool.WithName("orders"),
    adaptivepool.WithRecorder(promrecorder.New(reg)),
)

http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
```

- Collectors are registered when the pool is created and unregistered on
  `Shutdown`. Pools sharing a registry need distinct names; a duplicate
  makes `New` return an error.
- Gauges are evaluated when Prometheus scrapes, so they cost nothing between
  scrapes.

| Metric | Type | Labels | Answers |
|---|---|---|---|
| `adaptivepool_active_workers` | gauge | `pool` | How many workers are running? |
| `adaptivepool_busy_workers` | gauge | `pool` | How many are executing a task right now? |
| `adaptivepool_max_workers` | gauge | `pool` | What is the ceiling? (`busy ≈ max` means saturated) |
| `adaptivepool_queue_depth` | gauge | `pool` | How many tasks are waiting? |
| `adaptivepool_queue_capacity` | gauge | `pool` | How large is the queue? |
| `adaptivepool_jobs_submitted_total` | counter | `pool`, `kind` | Incoming rate by task type |
| `adaptivepool_jobs_rejected_total` | counter | `pool`, `kind`, `reason` | Overload: `timeout`, `canceled`, `queue_full` or `shutdown` |
| `adaptivepool_jobs_completed_total` | counter | `pool`, `kind`, `status` | Throughput, and why tasks failed |
| `adaptivepool_job_duration_seconds` | histogram | `pool`, `kind` | How long tasks take, including retries |
| `adaptivepool_job_queue_wait_seconds` | histogram | `pool`, `kind` | How long tasks wait before starting |
| `adaptivepool_jobs_retried_total` | counter | `pool`, `kind` | Tasks that needed at least one retry |
| `adaptivepool_retry_attempts_total` | counter | `pool`, `kind` | Total retries |
| `adaptivepool_scale_up_events_total` | counter | `pool` | How often the pool grew |

Names follow the Prometheus naming conventions (`promlint` reports no
problems). Once a kind has been seen, its retry counters are exported at
zero rather than appearing on the first retry.

**Useful queries**

```promql
# Throughput by task type
sum by (kind) (rate(adaptivepool_jobs_completed_total[1m]))

# Failure reasons
sum by (kind, status) (rate(adaptivepool_jobs_completed_total{status!="success"}[5m]))

# p95 execution time and queue wait
histogram_quantile(0.95, sum by (le, kind) (rate(adaptivepool_job_duration_seconds_bucket[5m])))
histogram_quantile(0.95, sum by (le, kind) (rate(adaptivepool_job_queue_wait_seconds_bucket[5m])))

# Saturation (alert when this stays near 1)
adaptivepool_busy_workers / adaptivepool_max_workers
```

### OpenTelemetry

```bash
go get github.com/iyashjayesh/go-adaptive-pool/otelrecorder
```

```go
pool, err := adaptivepool.New(
    adaptivepool.WithName("orders"),
    adaptivepool.WithRecorder(otelrecorder.New(meterProvider)), // nil uses otel.GetMeterProvider()
)
```

Names, units and attributes follow the OpenTelemetry semantic conventions,
modelled on the database connection pool metrics (`db.client.connection.*`).
Every metric carries `adaptivepool.pool.name`.

| Metric | Instrument | Unit | Attributes |
|---|---|---|---|
| `adaptivepool.worker.count` | UpDownCounter | `{worker}` | `adaptivepool.worker.state` = `idle` or `busy` |
| `adaptivepool.worker.limit` | UpDownCounter | `{worker}` | |
| `adaptivepool.worker.min` | UpDownCounter | `{worker}` | |
| `adaptivepool.queue.usage` | UpDownCounter | `{job}` | |
| `adaptivepool.queue.limit` | UpDownCounter | `{job}` | |
| `adaptivepool.job.submissions` | Counter | `{submission}` | `adaptivepool.job.kind` |
| `adaptivepool.job.rejections` | Counter | `{rejection}` | `adaptivepool.job.kind`, `adaptivepool.rejection.reason` |
| `adaptivepool.job.duration` | Histogram | `s` | `adaptivepool.job.kind`, `adaptivepool.job.status` |
| `adaptivepool.job.wait_time` | Histogram | `s` | `adaptivepool.job.kind` |
| `adaptivepool.job.retries` | Counter | `{retry}` | `adaptivepool.job.kind` |
| `adaptivepool.worker.scale_ups` | Counter | `{scale_up}` | |

- Completed jobs are counted by the `job.duration` histogram (its count, by
  status), as `http.server.request.duration` counts requests. There is no
  separate completions counter.
- There is no equivalent of Prometheus's `jobs_retried_total`; use
  `job.retries`.
- Duration histograms use the bucket boundaries the conventions recommend for
  seconds (5ms to 10s).
- Recording costs about 200ns per completed job, mostly inside the
  OpenTelemetry SDK, versus about 30ns for the Prometheus recorder. Neither
  allocates.

### Writing your own recorder

Implement four methods. They run on submitting and worker goroutines, so they
must be safe for concurrent use and fast:

```go
type statsdRecorder struct{ c *statsd.Client }

func (r statsdRecorder) JobSubmitted(kind string) {
    r.c.Incr("pool.submitted", []string{"kind:" + kind}, 1)
}
func (r statsdRecorder) JobRejected(kind string, reason adaptivepool.RejectReason) {
    r.c.Incr("pool.rejected", []string{"kind:" + kind, "reason:" + string(reason)}, 1)
}
func (r statsdRecorder) JobCompleted(kind string, status adaptivepool.Status, attempts int, wait, d time.Duration) {
    r.c.Timing("pool.duration", d, []string{"kind:" + kind, "status:" + string(status)}, 1)
}
func (r statsdRecorder) ScaledUp(added int) {
    r.c.Count("pool.scale_up", int64(added), nil, 1)
}

pool, err := adaptivepool.New(adaptivepool.WithRecorder(statsdRecorder{client}))
```

Kinds arrive normalized: an empty kind is reported as `"default"`. A panic in
a method is recovered and logged once (later ones are ignored without logging),
so a faulty recorder can't take down a worker or a caller.

To export gauges (queue depth, workers) or register with a backend, also
implement `Attacher`. `New` calls `Attach` with the pool's `PoolInfo` (name,
worker limits, queue size) and its live `Metrics()`, which you can read
whenever your backend collects; an error from `Attach` fails `New`.
`Shutdown` calls `Detach` once every worker has exited; the pool reports no
events after that, so the recorder can be attached to a new pool.

### Exposing metrics over HTTP without a backend

`pool.Metrics()` needs no recorder. To serve it as JSON, or through the
standard library's `/debug/vars`:

```go
http.HandleFunc("/pool", func(w http.ResponseWriter, _ *http.Request) {
    m := pool.Metrics()
    _ = json.NewEncoder(w).Encode(map[string]any{
        "queue_depth":    m.QueueDepth(),
        "active_workers": m.ActiveWorkers(),
        "busy_workers":   m.BusyWorkers(),
        "jobs_processed": m.JobsProcessed(),
        "jobs_failed":    m.JobsFailed(),
        "avg_latency":    m.AvgJobLatency().String(),
    })
})
```

These are pool-wide totals and averages. For per-kind breakdowns or
histograms, use a recorder.

---

## In-process metrics

`pool.Metrics()` is always available, with or without a recorder, so
`WithMetricsEnabled` is deprecated and does nothing: exporting is opt-in through
`WithRecorder`. It's handy for health endpoints, tests and logs:

```go
m := pool.Metrics()
m.QueueDepth()     // queued tasks
m.ActiveWorkers()  // running workers
m.BusyWorkers()    // workers executing a task
m.JobsProcessed()  // finished tasks
m.JobsFailed()     // finished with any non-success status
m.JobsRejected()   // rejected at submission
m.JobsRetried()    // needed at least one retry
m.RetryAttempts()  // total retries
m.AvgJobLatency()  // mean execution time, including retries
m.AvgQueueWait()   // mean time spent queued
```

---

## The `OnTaskDone` hook

`Submit` returns as soon as a task is queued, so your code otherwise has no way
to learn how a task ended. `WithOnTaskDone` is called **exactly once per
executed task**, whatever the outcome:

```go
pool, _ := adaptivepool.New(
    adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) {
        if r.Err != nil {
            deadLetters.Publish(r.ID, r.Kind, r.Status, r.Err) // hand off, don't block
        }
    }),
)
```

`TaskResult` contains `ID`, `Kind`, `QueueWait`, `Duration`, `Attempts`,
`Status` and `Err`.

**Use cases**

- Sending failed tasks to a dead-letter queue, or marking a record as failed.
- Notifying whoever submitted the task, for example by closing a per-task
  channel or completing a future.
- Ending a tracing span or recording custom business metrics.

**Rules**

- The hook runs synchronously on the worker goroutine, and that worker can't
  take another task until the hook returns. Keep it fast and hand off anything
  slow.
- A panic inside the hook is recovered and logged. It never kills the worker.
- When the hook isn't set, it costs a single nil check.

To inspect the error:

```go
var re *adaptivepool.RetryError
if errors.As(r.Err, &re) {
    log.Printf("gave up after %d attempts: %v", re.Attempts, re.Reason)
}
var pe *adaptivepool.PanicError
if errors.As(r.Err, &pe) {
    log.Printf("panic: %v\n%s", pe.Value, pe.Stack)
}
```

---

## Retries

Retries are enabled per pool with a reusable policy value:

```go
pool, err := adaptivepool.New(
    adaptivepool.WithRetry(adaptivepool.RetryPolicy{
        MaxAttempts:    5,
        Backoff:        adaptivepool.ExponentialBackoff{Base: 100 * time.Millisecond},
        MaxDelay:       2 * time.Second,
        MaxElapsedTime: 10 * time.Second,
        Jitter:         true,
    }),
)
```

**How it works**

- A failed job is retried **on the same worker**: it waits, then runs again.
  Nothing is re-queued and no goroutines or timers are left behind.
- A retrying job keeps its worker busy, so retries consume pool capacity.
  Bound them with `MaxAttempts`, `MaxDelay` and `MaxElapsedTime`.
- **Every error is retried by default.** The exceptions are errors wrapped with
  `Permanent`, a done job context, and panics (which are never retried).
- Only retry jobs that are **idempotent**: an attempt may have partly
  succeeded before it failed.
- An invalid policy makes `New` return an error wrapping `ErrInvalidConfig`.

### MaxAttempts (required)

This is the total number of runs, including the first. It must be at least 1:
infinite retries aren't supported, because a permanently failing job would
hold a worker forever.

```go
adaptivepool.RetryPolicy{MaxAttempts: 3} // first try + 2 retries
```

Use case: you want a predictable upper bound on how much work and worker time
a failing job can consume.

### Backoff strategies

`Backoff` decides how long to wait before each retry. `retry` starts at 1.

| Strategy | Formula | Example (`100ms` base) | Use when |
|---|---|---|---|
| `ConstantBackoff{Delay}` | `Delay` | 100ms, 100ms, 100ms | Polling for a short-lived condition, such as a lock or a file appearing |
| `LinearBackoff{Base}` | `Base × retry` | 100ms, 200ms, 300ms | Contention that clears gradually |
| `ExponentialBackoff{Base, Multiplier}` | `Base × Multiplier^(retry-1)` | 100ms, 150ms, 225ms (×1.5) | Overloaded or unavailable downstream services. **This is the default** (`Base: 100ms`, `Multiplier: 1.5`) |

```go
adaptivepool.ExponentialBackoff{Base: 50 * time.Millisecond, Multiplier: 2} // 50ms, 100ms, 200ms...
```

All three are stateless values: one instance is shared by every worker without
locking. Values too large for a `time.Duration` are clamped to the maximum
instead of overflowing.

**Custom backoff.** Implement `Next(retry int) time.Duration`. It must be safe
for concurrent use. Wrapping is the way to compose strategies:

```go
// An explicit schedule; the last step repeats
type stepped struct{ steps []time.Duration }

func (s stepped) Next(retry int) time.Duration {
    return s.steps[min(retry, len(s.steps))-1]
}

adaptivepool.RetryPolicy{
    MaxAttempts: 6,
    Backoff: stepped{[]time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 500 * time.Millisecond}},
}
```

### MaxDelay

`MaxDelay` caps any single wait, including `RetryAfter` hints. Zero means no
cap.

```go
adaptivepool.RetryPolicy{
    MaxAttempts: 10,
    Backoff:     adaptivepool.ExponentialBackoff{Base: 100 * time.Millisecond, Multiplier: 2},
    MaxDelay:    5 * time.Second, // 100ms, 200ms, ... 3.2s, 5s, 5s
}
```

Use case: you want exponential growth for the first few retries without one
late retry parking a worker for minutes.

### Jitter (full jitter)

With `Jitter: true`, each wait is drawn uniformly from `[0, delay)`, where
`delay` is the backoff value after `MaxDelay` is applied. This is the "full
jitter" scheme AWS recommends.

```go
adaptivepool.RetryPolicy{MaxAttempts: 5, Jitter: true}
```

Use case: a downstream outage fails hundreds of jobs at the same moment.
Without jitter they all retry in lockstep and hit the recovering service
together (a thundering herd). Jitter spreads them out.

`RetryAfter` hints are never jittered. Randomness comes from `math/rand/v2`,
which is lock-free.

### MaxElapsedTime

`MaxElapsedTime` is a total time budget for one job, covering all of its
attempts and waits. It works alongside `MaxAttempts`, and whichever limit is
hit first wins.

```go
adaptivepool.RetryPolicy{
    MaxAttempts:    20,
    MaxElapsedTime: 30 * time.Second,
}
```

- Retrying stops **as soon as the next wait would exceed the budget**, rather
  than sleeping first and then giving up. That frees the worker sooner.
- An attempt that's already running is never interrupted. Use the job's
  context if you need a hard deadline per attempt.
- The stop reason is `ErrRetryBudgetExceeded` and the status is
  `budget_exceeded`.

Use case: attempt latency varies a lot (a fast failure in one case, a 10s
timeout in another), so an attempt count alone can't bound the total cost.

### Context cancellation

You don't configure this; it always applies:

- If the job's context (`Task.Context` when set, otherwise the submit
  context) is done after a failure, retrying stops immediately and the
  reason is `ctx.Err()`.
- A backoff wait ends the moment the context is cancelled.
- Context errors are therefore never retried pointlessly. The status is
  `canceled`.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
pool.Submit(ctx, job) // retries for at most ~5s, and the job sees the same deadline
```

### Permanent errors

Every error is retried unless you mark it as permanent:

```go
func chargeCard(ctx context.Context) error {
    if err := validate(req); err != nil {
        return adaptivepool.Permanent(err) // invalid input will never succeed
    }
    return gateway.Charge(ctx, req) // network errors: retried
}
```

- `Permanent` is transparent: `Error()` returns the original message, and
  `errors.Is`/`errors.As` still match the original error.
- It works even when wrapped further:
  `fmt.Errorf("charge: %w", adaptivepool.Permanent(err))`.
- `Permanent(nil)` returns `nil`.
- The stop reason is `ErrPermanentFailure` and the status is `permanent`. Without
  `WithRetry` the status is still `permanent`, but the error is your own
  (still wrapped by `Permanent`) rather than a `*RetryError`.

Use case: bad input, authorisation failures, "not found". Retrying these only
wastes attempts and worker time.

### RetryAfter (delay override)

A job can tell the engine exactly how long to wait before the next attempt:

```go
func callAPI(ctx context.Context) error {
    resp, err := client.Do(req.WithContext(ctx))
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusTooManyRequests {
        wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
        return adaptivepool.RetryAfter(errRateLimited, time.Duration(wait)*time.Second)
    }
    return nil
}
```

- The hint replaces the backoff value for that one retry and isn't jittered.
- It's still capped by `MaxDelay`, so a hostile or huge hint can't block a
  worker indefinitely.
- It doesn't depend on HTTP: use it for any downstream that tells you when to
  come back, such as rate limiters, queue visibility timeouts or lease
  expiries.

### Per-error attempt limits

Allow fewer attempts for specific errors:

```go
var (
    errRateLimited = errors.New("rate limited")
    errTimeout     = errors.New("timeout")
)

adaptivepool.RetryPolicy{
    MaxAttempts: 6,
    ErrorLimits: []adaptivepool.ErrorLimit{
        {Err: errTimeout, MaxAttempts: 2},     // timeouts: retry once
        {Err: errRateLimited, MaxAttempts: 5}, // rate limits: be patient
    },
}
```

- Each failure is matched with `errors.Is` against the limits in order. The
  first match counts, and wrapped errors match too.
- The count is how many attempts failed with *that* error. Retrying stops when
  it reaches the limit, and `MaxAttempts` still bounds the job overall.
- The counters live on the stack for up to 8 limits, so typical policies add
  no allocations.

Use case: some failures deserve patience (rate limits) and others a quick
second chance (timeouts that usually mean the dependency is down).

### Why retrying stopped: `RetryError`

When retrying gives up, the final error is a `*RetryError`:

```go
type RetryError struct {
    Reason   error // why retrying stopped
    Attempts int   // how many times the job ran
    Err      error // the last attempt's error
}
```

`errors.Is` matches **both** the reason and the job's own error:

```go
errors.Is(err, adaptivepool.ErrRetriesExhausted) // true
errors.Is(err, errUpstream)                      // also true
```

| Reason | Status | Cause |
|---|---|---|
| `ErrRetriesExhausted` | `retries_exhausted` | `MaxAttempts` or an `ErrorLimit` was reached |
| `ErrRetryBudgetExceeded` | `budget_exceeded` | The next wait would exceed `MaxElapsedTime` |
| `ErrPermanentFailure` | `permanent` | The job returned a `Permanent` error |
| `context.Canceled` / `context.DeadlineExceeded` | `canceled` | The job failed after its context was done, with or without retries |
| `ErrPoolShutdown` | `shutdown` | A forced shutdown aborted a backoff wait |

Only the last attempt's error is kept, rather than the whole history: it's
simpler to handle and doesn't grow with the number of attempts. The `retrying
task` debug logs show the earlier errors when you need them.

Panics are never wrapped. They surface as `*PanicError` with status `panic`.

### Shutdown behaviour

- **Graceful** (`Shutdown` finishes within its context): in-flight retries run
  to completion. They're already bounded by your policy.
- **Forced** (`Shutdown`'s context expires): backoff waits, including
  zero-length ones, are aborted and the task finishes with status `shutdown`.
  A running attempt isn't interrupted. Each worker exits after its current
  job without starting another from the queue, and the recorder is detached
  once the last worker has exited.
- **Repeated calls:** the first call does the work. A later call returns the
  first call's result (for example `context.DeadlineExceeded` if it timed out),
  or its own `ctx` error if its `ctx` ends before the shutdown does.

### Testing retry timing with `Clock`

Inject a `Clock` so tests don't actually sleep:

```go
type fakeClock struct {
    mu    sync.Mutex
    now   time.Time
    waits []time.Duration
}

func (c *fakeClock) Now() time.Time {
    c.mu.Lock(); defer c.mu.Unlock()
    return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
    c.mu.Lock(); defer c.mu.Unlock()
    c.now = c.now.Add(d)
    c.waits = append(c.waits, d)
    ch := make(chan time.Time, 1)
    ch <- c.now
    return ch
}

clock := &fakeClock{}
pool, _ := adaptivepool.New(adaptivepool.WithRetry(adaptivepool.RetryPolicy{
    MaxAttempts: 4,
    Backoff:     adaptivepool.LinearBackoff{Base: time.Second},
    Clock:       clock,
}))
// ... submit a failing job, then assert clock.waits == [1s 2s 3s] instantly
```

---

## Status reference

Every finished task has exactly one `Status`. The same value appears in the
`status` metric label, the `status` log attribute and `TaskResult.Status`.

| Status | Meaning |
|---|---|
| `success` | The job returned nil (possibly after retries) |
| `error` | The job returned an error and retries are disabled (its context was still active) |
| `panic` | The job panicked (`*PanicError`, never retried) |
| `retries_exhausted` | All allowed attempts failed |
| `budget_exceeded` | `MaxElapsedTime` would have been exceeded |
| `permanent` | The job returned a `Permanent` error, with or without retries |
| `canceled` | The job failed after its context was done, with or without retries |
| `shutdown` | A forced shutdown aborted retrying |

---

## Grafana dashboard

[`examples/observability`](../examples/observability) contains a runnable app,
plus a Prometheus and Grafana stack with a provisioned dashboard:

```bash
make run-observability                                   # app with metrics on :2112
cd examples/observability && docker compose up           # Prometheus :9090, Grafana :3000
```

The dashboard has a pool selector and panels for:
- Workers (active, busy, max) and queue (depth, capacity)
- Throughput by kind, and failures by kind and status
- p95 execution time and p95 queue wait by kind
- Retries, retried jobs, and rejections by reason

---

## Performance

`executeJob` measured in isolation (`go test -bench BenchmarkExecuteJob -benchmem`, Apple M-series):

| Path | ns/op | allocs/op |
|---|---|---|
| No recorder | ~50 | 0 |
| Recorder set (dispatch only, no-op recorder) | ~51 | 0 |
| Retry enabled, job succeeds | ~54 | 0 |
| One retry (zero delay), then success | ~65 | 0 |
| One retry with a real wait, then success | the wait itself | 3 (248 B, the `time.After` timer) |
| `OnTaskDone` hook set | ~58 | 0 |
| Job fails, retries disabled | ~56 | 0 |
| Retries exhausted | ~94 | 1 (the returned `*RetryError`) |

A recorder adds its own cost on top: about 30ns per completed job for
`promrecorder` and about 200ns for `otelrecorder`, both without allocations
(`BenchmarkJobCompleted` in each package).

`TestExecuteJobAllocations` fails the build if any zero-allocation path starts
allocating.

**Why the timer allocation is left alone.** It could be removed by keeping one
reusable timer per worker and calling `Reset` before each wait. That would need
the `Clock` interface to expose timers instead of `After`, plus extra plumbing
from each worker into the retry engine. A real backoff wait lasts at least
milliseconds, so 248 bytes per retry costs nothing measurable, and the extra
complexity isn't worth it.
