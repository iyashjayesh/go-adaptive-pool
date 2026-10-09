package adaptivepool_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	adaptivepool "github.com/iyashjayesh/go-adaptive-pool"
)

func ExampleWithRetry() {
	done := make(chan adaptivepool.TaskResult, 1)
	pool, err := adaptivepool.New(
		adaptivepool.WithLogger(slog.New(slog.DiscardHandler)),
		adaptivepool.WithRetry(adaptivepool.RetryPolicy{
			MaxAttempts: 5,
			Backoff:     adaptivepool.ExponentialBackoff{Base: time.Millisecond},
			MaxDelay:    10 * time.Millisecond,
			Jitter:      true,
		}),
		adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) { done <- r }),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	calls := 0
	_ = pool.Submit(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("temporarily unavailable")
		}
		return nil
	})

	r := <-done
	fmt.Println(r.Status, "after", r.Attempts, "attempts")
	// Output: success after 3 attempts
}

func ExamplePermanent() {
	done := make(chan adaptivepool.TaskResult, 1)
	pool, err := adaptivepool.New(
		adaptivepool.WithLogger(slog.New(slog.DiscardHandler)),
		adaptivepool.WithRetry(adaptivepool.RetryPolicy{MaxAttempts: 5}),
		adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) { done <- r }),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	errInvalid := errors.New("invalid payload")
	_ = pool.Submit(context.Background(), func(context.Context) error {
		return adaptivepool.Permanent(errInvalid)
	})

	r := <-done
	fmt.Println(r.Status, r.Attempts)
	fmt.Println(errors.Is(r.Err, adaptivepool.ErrPermanentFailure), errors.Is(r.Err, errInvalid))
	// Output:
	// permanent 1
	// true true
}

func ExampleWithOnTaskDone() {
	done := make(chan adaptivepool.TaskResult, 1)
	pool, err := adaptivepool.New(
		adaptivepool.WithLogger(slog.New(slog.DiscardHandler)),
		adaptivepool.WithOnTaskDone(func(r adaptivepool.TaskResult) { done <- r }),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	_ = pool.SubmitTask(context.Background(), adaptivepool.Task{
		ID:   "order-8812",
		Kind: "send_email",
		Run:  func(context.Context) error { return errors.New("smtp: connection refused") },
	})

	r := <-done
	fmt.Printf("%s %s %s: %v\n", r.Kind, r.ID, r.Status, r.Err)
	// Output: send_email order-8812 error: smtp: connection refused
}

func ExamplePool_SubmitTask() {
	done := make(chan error, 1)
	pool, err := adaptivepool.New(adaptivepool.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		panic(err)
	}
	defer func() { _ = pool.Shutdown(context.Background()) }()

	// Wait at most 50ms for queue space, but let the job run on its own
	// context so the wait deadline doesn't cancel it.
	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = pool.SubmitTask(waitCtx, adaptivepool.Task{
		Kind:    "send_email",
		Context: context.Background(),
		Run: func(ctx context.Context) error {
			time.Sleep(100 * time.Millisecond) // outlives waitCtx
			done <- ctx.Err()
			return nil
		},
	})
	cancel()

	fmt.Println("submitted:", err)
	fmt.Println("job context error:", <-done)
	// Output:
	// submitted: <nil>
	// job context error: <nil>
}
