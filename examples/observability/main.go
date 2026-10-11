// Package main runs an adaptive pool under synthetic load and exposes its
// Prometheus metrics, for use with the bundled Prometheus + Grafana stack.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
	"github.com/iyashjayesh/go-adaptive-pool/promrecorder"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	errUpstream    = errors.New("upstream unavailable")
	errRateLimited = errors.New("rate limited")
	errBadPayload  = errors.New("invalid payload")
)

// kind describes one type of synthetic task
type kind struct {
	name    string
	latency time.Duration
	run     func(ctx context.Context, latency time.Duration) error
}

func main() {
	metricsAddr := flag.String("metrics-addr", ":2112", "address to serve Prometheus metrics on; keep prometheus.yml's target in sync")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var deadLetters atomic.Int64
	pool, err := adaptivepool.New(
		adaptivepool.WithName("orders"),
		adaptivepool.WithMinWorkers(4),
		adaptivepool.WithMaxWorkers(32),
		adaptivepool.WithQueueSize(500),
		adaptivepool.WithScaleDownIdleDuration(10*time.Second),
		adaptivepool.WithScaleCooldown(time.Second), // react quickly to the 30s bursts
		adaptivepool.WithLogger(logger),
		adaptivepool.WithRecorder(promrecorder.New(prometheus.DefaultRegisterer)),
		adaptivepool.WithRetry(adaptivepool.RetryPolicy{
			MaxAttempts:    4,
			Backoff:        adaptivepool.ExponentialBackoff{Base: 50 * time.Millisecond, Multiplier: 2},
			MaxDelay:       time.Second,
			MaxElapsedTime: 3 * time.Second,
			Jitter:         true,
			ErrorLimits:    []adaptivepool.ErrorLimit{{Err: errUpstream, MaxAttempts: 2}},
		}),
		adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) {
			if r.Err != nil {
				deadLetters.Add(1) // a real service would publish r to a dead-letter queue
			}
		}),
	)
	if err != nil {
		logger.Error("creating pool", slog.Any("err", err))
		os.Exit(1)
	}

	// A small pool for interactive work. It has no retries: a request has a
	// deadline, so failing fast beats queueing work nobody will wait for.
	previews, err := adaptivepool.New(
		adaptivepool.WithName("previews"),
		adaptivepool.WithMinWorkers(2),
		adaptivepool.WithMaxWorkers(4),
		adaptivepool.WithQueueSize(8), // ~80ms of work: queued requests still meet their deadline
		adaptivepool.WithScaleCooldown(time.Second),
		adaptivepool.WithLogger(logger),
		adaptivepool.WithRecorder(promrecorder.New(prometheus.DefaultRegisterer)),
	)
	if err != nil {
		logger.Error("creating pool", slog.Any("err", err))
		os.Exit(1)
	}

	http.Handle("/metrics", promhttp.Handler())
	server := &http.Server{Addr: *metricsAddr, ReadHeaderTimeout: 3 * time.Second}
	go func() {
		logger.Info("serving metrics", slog.String("addr", *metricsAddr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server", slog.Any("err", err))
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Producers stop before the pools shut down
	var producers sync.WaitGroup
	producers.Go(func() { generateLoad(ctx, pool) })
	producers.Go(func() { serveRequests(ctx, previews) })
	producers.Go(func() { trackEvents(ctx, previews) })
	producers.Wait()

	logger.Info("shutting down", slog.Int64("dead_letters", deadLetters.Load()))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = pool.Shutdown(shutdownCtx)
	_ = previews.Shutdown(shutdownCtx)
	_ = server.Shutdown(shutdownCtx)
}

// generateLoad submits tasks in waves so scaling, queueing and retries are
// all visible on the dashboard.
func generateLoad(ctx context.Context, pool adaptivepool.Pool) {
	kinds := []kind{
		{"send_email", 20 * time.Millisecond, flaky(0.10, errUpstream)},
		{"resize_image", 80 * time.Millisecond, flaky(0.02, errBadPayload)},
		{"charge_card", 40 * time.Millisecond, rateLimited(0.15)},
	}

	var seq int
	for {
		rate := 50
		if burst() {
			rate = 400
		}
		tick := time.Second / time.Duration(rate)

		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}

		k := kinds[rand.IntN(len(kinds))]
		seq++
		// The submit context is also the task's execution context, so it must
		// outlive the task. Background lets queued tasks drain on shutdown.
		_ = pool.SubmitTask(context.Background(), adaptivepool.Task{
			ID:   fmt.Sprintf("%s-%d", k.name, seq),
			Kind: k.name,
			Run: func(ctx context.Context) error {
				return k.run(ctx, k.latency)
			},
		})
	}
}

// serveRequests simulates interactive requests, such as rendering a preview
// for an HTTP response, that must finish within the request's deadline.
//
// Here the submit context should also be the job context: if the request
// times out or the client goes away, both waiting for a queue slot and the
// work itself should stop. When the pool is saturated, Submit returns
// ErrTimeout (rejected, reason="timeout") or context.Canceled
// (reason="canceled") and a real handler would respond with 503.
func serveRequests(ctx context.Context, pool adaptivepool.Pool) {
	var requests sync.WaitGroup
	defer requests.Wait()

	for id := 1; ; id++ {
		rate := 40
		if burst() {
			rate = 150 // more than 4 workers can serve
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second / time.Duration(rate)):
		}
		requests.Go(func() { handleRequest(ctx, pool, id) })
	}
}

func handleRequest(ctx context.Context, pool adaptivepool.Pool, id int) {
	reqCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if rand.Float64() < 0.05 {
		time.AfterFunc(rand.N(100*time.Millisecond), cancel) // client disconnects
	}

	done := make(chan struct{})
	err := pool.SubmitTask(reqCtx, adaptivepool.Task{
		ID:   fmt.Sprintf("preview-%d", id),
		Kind: "render_preview",
		Run: func(ctx context.Context) error {
			defer close(done)
			return work(ctx, 40*time.Millisecond)
		},
	})
	if err != nil {
		return // rejected: respond 503 Service Unavailable
	}
	select {
	case <-done:
	case <-reqCtx.Done():
	}
}

// trackEvents submits best-effort telemetry: if no worker slot frees up
// within 5ms the event is dropped (rejected, reason="timeout").
//
// Task.Context gives the job its own context, so the 5ms deadline only
// bounds the wait for a queue slot and doesn't cancel accepted events.
func trackEvents(ctx context.Context, pool adaptivepool.Pool) {
	for id := 1; ; id++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
		submitCtx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
		_ = pool.SubmitTask(submitCtx, adaptivepool.Task{
			ID:      fmt.Sprintf("event-%d", id),
			Kind:    "track_event",
			Context: context.Background(),
			Run: func(ctx context.Context) error {
				return work(ctx, 5*time.Millisecond)
			},
		})
		cancel()
	}
}

// burst reports whether load is in a burst phase; phases alternate every 30s
func burst() bool {
	return time.Now().Unix()/30%2 == 0
}

// work simulates a task taking roughly latency
func work(ctx context.Context, latency time.Duration) error {
	select {
	case <-time.After(latency/2 + rand.N(latency)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flaky fails with err at the given rate. Payload errors are permanent.
func flaky(rate float64, err error) func(context.Context, time.Duration) error {
	return func(ctx context.Context, latency time.Duration) error {
		if werr := work(ctx, latency); werr != nil {
			return werr
		}
		if rand.Float64() < rate {
			if errors.Is(err, errBadPayload) {
				return adaptivepool.Permanent(err)
			}
			return err
		}
		return nil
	}
}

// rateLimited simulates a downstream API that returns a Retry-After hint
func rateLimited(rate float64) func(context.Context, time.Duration) error {
	return func(ctx context.Context, latency time.Duration) error {
		if err := work(ctx, latency); err != nil {
			return err
		}
		if rand.Float64() < rate {
			return adaptivepool.RetryAfter(errRateLimited, 200*time.Millisecond)
		}
		return nil
	}
}
