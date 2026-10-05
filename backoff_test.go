package adaptivepool

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBackoffSequences(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name    string
		backoff Backoff
		want    []time.Duration
	}{
		{"constant", ConstantBackoff{Delay: 50 * ms}, []time.Duration{50 * ms, 50 * ms, 50 * ms}},
		{"linear", LinearBackoff{Base: 100 * ms}, []time.Duration{100 * ms, 200 * ms, 300 * ms}},
		{"exponential default multiplier", ExponentialBackoff{Base: 100 * ms}, []time.Duration{100 * ms, 150 * ms, 225 * ms}},
		{"exponential x2", ExponentialBackoff{Base: 100 * ms, Multiplier: 2}, []time.Duration{100 * ms, 200 * ms, 400 * ms}},
		{"pointer receiver", &LinearBackoff{Base: ms}, []time.Duration{ms, 2 * ms, 3 * ms}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, want := range tt.want {
				assert.Equal(t, want, tt.backoff.Next(i+1), "retry %d", i+1)
			}
		})
	}
}

func TestBackoffOverflowClamps(t *testing.T) {
	assert.Equal(t, time.Duration(math.MaxInt64), ExponentialBackoff{Base: time.Second, Multiplier: 10}.Next(100))
	assert.Equal(t, time.Duration(math.MaxInt64), LinearBackoff{Base: time.Hour}.Next(math.MaxInt32))
}

func TestBackoffValidation(t *testing.T) {
	invalid := []Backoff{
		ConstantBackoff{Delay: -1},
		LinearBackoff{Base: -1},
		ExponentialBackoff{Base: -1},
		ExponentialBackoff{Base: time.Second, Multiplier: 0.5},
		&ExponentialBackoff{Base: time.Second, Multiplier: 0.5},
		ExponentialBackoff{Base: time.Second, Multiplier: math.NaN()},
		ExponentialBackoff{Base: time.Second, Multiplier: math.Inf(1)},
	}
	for _, b := range invalid {
		_, err := New(WithRetry(RetryPolicy{MaxAttempts: 2, Backoff: b}))
		assert.ErrorIs(t, err, ErrInvalidConfig, "%#v", b)
	}
}
