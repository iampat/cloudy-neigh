package logstream

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
)

func TestGallopAbortsOnContextCancel(t *testing.T) {
	ctx := context.Background()
	s, err := objectstore.Open(ctx, "mem://")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	log := New(s)
	stream := "abort-stream"

	for i := 1; i <= 35; i++ {
		_, err := log.Append(ctx, stream, []Record{[]byte("data")})
		if err != nil {
			t.Fatal(err)
		}
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	_, probes, err := log.gallop(cancelCtx, stream, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gallop err = %v, want context.Canceled", err)
	}
	if probes != 0 {
		t.Fatalf("gallop probes = %d on canceled context, want 0", probes)
	}
}

func TestGallopUint64AboveMaxInt64(t *testing.T) {
	ctx := context.Background()
	s, err := objectstore.Open(ctx, "mem://")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	log := New(s)
	stream := "large-seq"

	base := uint64(math.MaxInt64) + 100
	for i := uint64(0); i < 5; i++ {
		seq := base + i
		key := fmt.Sprintf("wal/%s/%020d.recordio", stream, seq)
		_, err := s.Put(ctx, key, strings.NewReader("stub"), nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	head, probes, err := log.gallop(ctx, stream, base)
	if err != nil {
		t.Fatalf("gallop err = %v", err)
	}
	expectedHead := base + 4
	if head != expectedHead {
		t.Fatalf("head = %d, want %d", head, expectedHead)
	}
	if probes == 0 {
		t.Fatal("expected probes > 0")
	}
}
