package logstream

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestBinarySearch(t *testing.T) {
	ctx := context.Background()
	targetHead := uint64(42)
	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= targetHead, nil
	}

	head, probes, err := binarySearch(ctx, 40, 50, probe)
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

func TestBinarySearchAbortsOnContextCancel(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	probe := func(ctx context.Context, seq uint64) (bool, error) {
		return seq <= 42, nil
	}

	_, probes, err := binarySearch(cancelCtx, 40, 50, probe)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if probes != 0 {
		t.Fatalf("probes = %d, want 0", probes)
	}
}

func TestBinarySearchAbortsOnProbeError(t *testing.T) {
	ctx := context.Background()
	probeErr := errors.New("backend connection reset")
	probesCount := 0

	probe := func(ctx context.Context, seq uint64) (bool, error) {
		probesCount++
		if probesCount == 1 {
			return false, probeErr
		}
		return true, nil
	}

	_, probes, err := binarySearch(ctx, 10, 1000, probe)
	if !errors.Is(err, probeErr) {
		t.Fatalf("err = %v, want %v", err, probeErr)
	}
	// Once perr is set, subsequent calls inside sort.Search immediately return true without probing.
	if probes != 1 {
		t.Fatalf("probes executed = %d, want 1", probes)
	}
}

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
