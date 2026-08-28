package logstream

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJump(t *testing.T) {
	ctx := context.Background()
	targetHead := uint64(1000)
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := jump(ctx, 1, probe)
	require.NoError(t, err)
	assert.Equal(t, targetHead, head)
	assert.Positive(t, probes)
}

func TestJumpAbortsOnContextCancel(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	probe := func(ctx context.Context, seq uint64) (bool, error) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		return seq <= 42, nil
	}

	_, probes, err := jump(cancelCtx, 1, probe)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, probes)
}

func TestJumpAbortsOnProbeError(t *testing.T) {
	ctx := context.Background()
	probeErr := errors.New("backend connection reset")

	probe := func(ctx context.Context, seq uint64) (bool, error) {
		if seq == 2 {
			return false, probeErr
		}
		return true, nil
	}

	_, probes, err := jump(ctx, 1, probe)
	assert.ErrorIs(t, err, probeErr)
	assert.Equal(t, 1, probes)
}

func TestJumpUint64AboveMaxInt64(t *testing.T) {
	ctx := context.Background()
	base := uint64(math.MaxInt64) + 100
	targetHead := base + 4
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := jump(ctx, base, probe)
	require.NoError(t, err)
	assert.Equal(t, targetHead, head)
	assert.Positive(t, probes)
}

func TestJumpUint64LargeSpan(t *testing.T) {
	ctx := context.Background()
	targetHead := uint64(math.MaxInt64) + 100
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := jump(ctx, 1, probe)
	require.NoError(t, err)
	assert.Equal(t, targetHead, head)
	assert.Positive(t, probes)
}

func TestAppendCancelWhileWaitingForLock(t *testing.T) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	log, err := New(s, "lock-stream")
	require.NoError(t, err)
	log.ch <- struct{}{}
	t.Cleanup(func() { <-log.ch })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)

	_, err = log.Append(ctx, []Record{[]byte("waiter")})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
