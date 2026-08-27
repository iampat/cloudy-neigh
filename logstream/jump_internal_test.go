package logstream

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
)

func TestJump(t *testing.T) {
	ctx := context.Background()
	targetHead := uint64(1000)
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := jump(ctx, 1, probe)
	if err != nil {
		t.Fatal(err)
	}
	if head != targetHead {
		t.Fatalf("head = %d, want %d", head, targetHead)
	}
	if probes == 0 {
		t.Fatal("expected probes > 0")
	}
}

func TestJumpAbortsOnContextCancel(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= 42, nil
	}

	_, probes, err := jump(cancelCtx, 1, probe)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if probes != 0 {
		t.Fatalf("probes = %d, want 0", probes)
	}
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
	if !errors.Is(err, probeErr) {
		t.Fatalf("err = %v, want %v", err, probeErr)
	}
	if probes != 1 {
		t.Fatalf("probes executed = %d, want 1", probes)
	}
}

func TestJumpUint64AboveMaxInt64(t *testing.T) {
	ctx := context.Background()
	base := uint64(math.MaxInt64) + 100
	targetHead := base + 4
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := jump(ctx, base, probe)
	if err != nil {
		t.Fatal(err)
	}
	if head != targetHead {
		t.Fatalf("head = %d, want %d", head, targetHead)
	}
	if probes == 0 {
		t.Fatal("expected probes > 0")
	}
}

func TestJumpUint64LargeSpan(t *testing.T) {
	ctx := context.Background()
	targetHead := uint64(math.MaxInt64) + 100
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := jump(ctx, 1, probe)
	if err != nil {
		t.Fatal(err)
	}
	if head != targetHead {
		t.Fatalf("head = %d, want %d", head, targetHead)
	}
	if probes == 0 {
		t.Fatal("expected probes > 0")
	}
}

func TestAppendCancelWhileWaitingForLock(t *testing.T) {
	s, err := objectstore.Open(context.Background(), "mem://")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	log := New(s)
	st := log.stream("lock-stream")
	st.ch <- struct{}{}
	t.Cleanup(func() { <-st.ch })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)

	_, err = log.Append(ctx, "lock-stream", []Record{[]byte("waiter")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Append err = %v, want DeadlineExceeded", err)
	}
}

func TestExists(t *testing.T) {
	s, err := objectstore.Open(context.Background(), "mem://")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	k := "wal/test/00000000000000000001.recordio"
	ok, err := exists(ctx, s, k)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("exists = true, want false")
	}
	if _, err := s.Put(ctx, k, strings.NewReader("val"), nil); err != nil {
		t.Fatal(err)
	}
	ok, err = exists(ctx, s, k)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("exists = false, want true")
	}
}
