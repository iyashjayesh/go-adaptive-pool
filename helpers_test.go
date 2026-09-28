package adaptivepool

import (
	"context"
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
