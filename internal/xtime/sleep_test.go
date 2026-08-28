package xtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/internal/xtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSleep(t *testing.T) {
	t.Run("ZeroOrNegativeDurationReturnsImmediately", func(t *testing.T) {
		ctx := context.Background()
		require.NoError(t, xtime.Sleep(ctx, 0))
		require.NoError(t, xtime.Sleep(ctx, -5*time.Millisecond))
	})

	t.Run("SleepsForDuration", func(t *testing.T) {
		ctx := context.Background()
		t0 := time.Now()
		require.NoError(t, xtime.Sleep(ctx, 5*time.Millisecond))
		assert.GreaterOrEqual(t, time.Since(t0), 5*time.Millisecond)
	})

	t.Run("ContextCancelledAbortsSleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := xtime.Sleep(ctx, 100*time.Millisecond)
		assert.ErrorIs(t, err, context.Canceled)
	})
}
