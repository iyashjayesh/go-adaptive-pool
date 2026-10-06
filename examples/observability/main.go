// Package main runs an adaptive pool under synthetic load and exposes its
// Prometheus metrics, for use with the bundled Prometheus + Grafana stack.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var deadLetters atomic.Int64
	pool, err := adaptivepool.New(
		adaptivepool.WithName("orders"),
		adaptivepool.WithMinWorkers(4),
		adaptivepool.WithMaxWorkers(32),
		adaptivepool.WithQueueSize(500),
		adaptivepool.WithScaleDownIdleDuration(10*time.Second),
		adaptivepool.WithLogger(logger),
		adaptivepool.WithPrometheusRegisterer(prometheus.DefaultRegisterer),
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

	http.Handle("/metrics", promhttp.Handler())
	server := &http.Server{Addr: ":2112", ReadHeaderTimeout: 3 * time.Second}
	go func() {
		logger.Info("serving metrics", slog.String("addr", "http://localhost:2112/metrics"))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server", slog.Any("err", err))
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	generateLoad(ctx, pool)

	logger.Info("shutting down", slog.Int64("dead_letters", deadLetters.Load()))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = pool.Shutdown(shutdownCtx)
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
		// Alternate between quiet and burst periods every 30s
		rate := 50
		if time.Now().Unix()/30%2 == 0 {
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
