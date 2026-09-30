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
