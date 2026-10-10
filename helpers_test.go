package adaptivepool

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestPool creates a pool that is shut down when the test ends.
func newTestPool(t *testing.T, opts ...Option) Pool {
	t.Helper()
	p, err := New(opts...)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	return p
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// logRecorder captures JSON log records written by the pool.
type logRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *logRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}

func (r *logRecorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(r, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// records returns all captured records with the given message.
func (r *logRecorder) records(msg string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(r.buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// fakeRecorder captures recorder events in memory.
type fakeRecorder struct {
	mu        sync.Mutex
	submitted []string
	rejected  []string // kind/reason
	completed []completedEvent
	scaledUp  []int

	attachErr error
	info      *PoolInfo
	stats     Metrics
	detached  bool
}

type completedEvent struct {
	kind     string
	status   Status
	attempts int
}

func (r *fakeRecorder) JobSubmitted(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.submitted = append(r.submitted, kind)
}

func (r *fakeRecorder) JobRejected(kind string, reason RejectReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejected = append(r.rejected, kind+"/"+string(reason))
}

func (r *fakeRecorder) JobCompleted(kind string, status Status, attempts int, _, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, completedEvent{kind, status, attempts})
}

func (r *fakeRecorder) ScaledUp(added int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scaledUp = append(r.scaledUp, added)
}

func (r *fakeRecorder) Attach(info PoolInfo, stats Metrics) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info, r.stats = &info, stats
	return r.attachErr
}

func (r *fakeRecorder) Detach() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detached = true
}

func (r *fakeRecorder) completedEvents() []completedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]completedEvent(nil), r.completed...)
}
