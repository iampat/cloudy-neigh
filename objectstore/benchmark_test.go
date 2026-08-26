package objectstore_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
)

func clearPrefix(b *testing.B, s *objectstore.Store, prefix string) {
	b.Helper()
	ctx := context.Background()
	objs, err := s.List(ctx, prefix, "", 0)
	if err != nil {
		b.Fatal(err)
	}
	for _, o := range objs {
		if err := s.Delete(ctx, o.Key); err != nil {
			b.Fatal(err)
		}
	}
}

func benchStore(b *testing.B, open func(b *testing.B) *objectstore.Store) {
	ctx := context.Background()
	payload := bytes.Repeat([]byte("x"), 1024)
	prefix := b.Name() + "/"
	clearPrefix(b, open(b), prefix)

	b.Run("Put", func(b *testing.B) {
		s := open(b)
		i := 0
		for b.Loop() {
			i++
			if _, err := s.Put(ctx, fmt.Sprintf("%sput/%d", prefix, i), bytes.NewReader(payload), nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Get", func(b *testing.B) {
		s := open(b)
		if _, err := s.Put(ctx, prefix+"get", bytes.NewReader(payload), nil); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			r, _, err := s.Get(ctx, prefix+"get")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, r); err != nil {
				b.Fatal(err)
			}
			r.Close()
		}
	})
	b.Run("Put(Absent)", func(b *testing.B) {
		s := open(b)
		i := 0
		for b.Loop() {
			i++
			key := fmt.Sprintf("%sabsent/%d", prefix, i)
			if _, err := s.Put(ctx, key, bytes.NewReader(payload), &objectstore.Condition{Absent: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Put(GenerationMatch)", func(b *testing.B) {
		s := open(b)
		key := prefix + "cas"
		gen, err := s.Put(ctx, key, bytes.NewReader(payload), nil)
		if err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			gen, err = s.Put(ctx, key, bytes.NewReader(payload), &objectstore.Condition{GenerationMatch: gen})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchSameKeyRate(b *testing.B, open func(b *testing.B) *objectstore.Store) {
	b.Run("SameKeyMutationRate", func(b *testing.B) {
		s := open(b)
		ctx := context.Background()
		payload := bytes.Repeat([]byte("x"), 1024)
		key := b.Name() + "/hot"
		clearPrefix(b, s, b.Name()+"/")
		gen, err := s.Put(ctx, key, bytes.NewReader(payload), nil)
		if err != nil {
			b.Fatal(err)
		}
		done := 0
		var busy time.Duration
		failed := false
		for b.Loop() {
			if failed {
				continue
			}
			start := time.Now()
			gen, err = s.Put(ctx, key, bytes.NewReader(payload), &objectstore.Condition{GenerationMatch: gen})
			if err != nil {
				b.Logf("stopped after %d mutations: %v", done, err)
				failed = true
				continue
			}
			busy += time.Since(start)
			done++
		}
		if done > 0 {
			b.ReportMetric(float64(done)/busy.Seconds(), "mutations/sec")
		}
	})
}

func BenchmarkMem(b *testing.B) {
	benchStore(b, func(b *testing.B) *objectstore.Store {
		s := openURL(b, "mem://")
		b.Cleanup(func() { s.Close() })
		return s
	})
}

func BenchmarkDisk(b *testing.B) {
	benchStore(b, func(b *testing.B) *objectstore.Store {
		s := openURL(b, "file://"+b.TempDir()+"/bucket?create_dir=true")
		b.Cleanup(func() { s.Close() })
		return s
	})
}

func BenchmarkGCSSameKeyRate(b *testing.B) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		b.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	benchSameKeyRate(b, func(b *testing.B) *objectstore.Store {
		s := openURL(b, "gs://"+bucket)
		b.Cleanup(func() { s.Close() })
		return s
	})
}

func BenchmarkGCS(b *testing.B) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		b.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	benchStore(b, func(b *testing.B) *objectstore.Store {
		s := openURL(b, "gs://"+bucket)
		b.Cleanup(func() { s.Close() })
		return s
	})
}
