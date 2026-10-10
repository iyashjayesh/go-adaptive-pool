package adaptivepool

import (
	"context"
	"errors"
	"testing"
	"time"
)

// benchPool returns the concrete pool so executeJob can be measured in
// isolation from queueing and scheduling.
func benchPool(tb testing.TB, opts ...Option) *pool {
	tb.Helper()
	opts = append([]Option{WithLogger(discardLogger())}, opts...)
	p, err := New(opts...)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	return p.(*pool)
}

var errBench = errors.New("bench")

// noopRecorder measures the cost of dispatching events to a recorder.
type noopRecorder struct{}

func (noopRecorder) JobSubmitted(string)                                            {}
func (noopRecorder) JobRejected(string, RejectReason)                               {}
func (noopRecorder) JobCompleted(string, Status, int, time.Duration, time.Duration) {}
func (noopRecorder) ScaledUp(int)                                                   {}

func executeCases() []struct {
	name string
	opts []Option
	job  Job
} {
	ok := func(context.Context) error { return nil }
	failOnce := func() Job {
		failed := false
		return func(context.Context) error {
			if failed = !failed; failed {
				return errBench
			}
			return nil
		}
	}
	noWait := WithRetry(RetryPolicy{MaxAttempts: 3, Backoff: ConstantBackoff{}})
	return []struct {
		name string
		opts []Option
		job  Job
	}{
		{"no-recorder", nil, ok},
		{"recorder", []Option{WithRecorder(noopRecorder{})}, ok},
		{"retry-success", []Option{noWait}, ok},
		{"retry-one-failure", []Option{noWait}, failOnce()},
		{"hook", []Option{WithOnTaskDone(func(TaskResult) {})}, ok},
		{"error", nil, func(context.Context) error { return errBench }},
		{"retry-exhausted", []Option{noWait}, func(context.Context) error { return errBench }},
	}
}

func BenchmarkExecuteJob(b *testing.B) {
	for _, c := range executeCases() {
		b.Run(c.name, func(b *testing.B) {
			p := benchPool(b, c.opts...)
			w := jobWrapper{job: c.job, ctx: context.Background(), kind: "bench", SubmittedAt: time.Now()}
			b.ReportAllocs()
			for b.Loop() {
				p.executeJob(0, w)
			}
		})
	}
}

// TestExecuteJobAllocations guards the hot path: apart from the final
// *RetryError when retrying gives up, executing a job must not allocate.
// Retry cases use zero-length waits; a real wait adds the timer allocated
// by Clock.After, which is outside what this test covers.
func TestExecuteJobAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector adds allocations")
	}
	for _, c := range executeCases() {
		t.Run(c.name, func(t *testing.T) {
			p := benchPool(t, c.opts...)
			w := jobWrapper{job: c.job, ctx: context.Background(), kind: "bench", SubmittedAt: time.Now()}
			want := 0.0
			if c.name == "retry-exhausted" {
				want = 1
			}
			allocs := testing.AllocsPerRun(1000, func() { p.executeJob(0, w) })
			if allocs != want {
				t.Fatalf("got %v allocs per job, want %v", allocs, want)
			}
		})
	}
}
