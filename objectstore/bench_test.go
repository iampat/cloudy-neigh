package objectstore_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
)

var nonces atomic.Int64

// A live bucket keeps objects between runs, so delete what an earlier run
// left under the prefix.
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

	// Every mutation benchmark uses a distinct key per iteration. GCS caps
	// mutations of one object at about one per second, so reusing a key
	// measures that limit instead of the write latency.
	b.Run("Put", func(b *testing.B) {
		s := open(b)
		nonce := nonces.Add(1)
		for i := 0; i < b.N; i++ {
			if _, err := s.Put(ctx, fmt.Sprintf("%sput/%d/%d", prefix, nonce, i), bytes.NewReader(payload), nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Get", func(b *testing.B) {
		s := open(b)
		if _, err := s.Put(ctx, prefix+"get", bytes.NewReader(payload), nil); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
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
	b.Run("Get", func(b *testing.B) {
		s := open(b)
		if _, err := s.Put(ctx, prefix+"getgen", bytes.NewReader(payload), nil); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r, _, err := s.Get(ctx, prefix+"getgen")
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
		nonce := nonces.Add(1)
		for i := 0; i < b.N; i++ {
			key := fmt.Sprintf("%sabsent/%d/%d", prefix, nonce, i)
			if _, err := s.Put(ctx, key, bytes.NewReader(payload), &objectstore.Condition{Absent: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Put(GenerationMatch)", func(b *testing.B) {
		s := open(b)
		nonce := nonces.Add(1)
		keys := make([]string, b.N)
		gens := make([]string, b.N)
		for i := range keys {
			keys[i] = fmt.Sprintf("%scas/%d/%d", prefix, nonce, i)
			gen, err := s.Put(ctx, keys[i], bytes.NewReader(payload), nil)
			if err != nil {
				b.Fatal(err)
			}
			gens[i] = gen
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := s.Put(ctx, keys[i], bytes.NewReader(payload), &objectstore.Condition{GenerationMatch: gens[i]}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// SameKeyMutationRate reports the sustained mutation rate of one key, which
// is the rate a branch ref sees. It is a rate, not a latency: GCS answers a
// mutation of the same object at about one per second.
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
		b.ResetTimer()
		start := time.Now()
		done := 0
		for i := 0; i < b.N; i++ {
			gen, err = s.Put(ctx, key, bytes.NewReader(payload), &objectstore.Condition{GenerationMatch: gen})
			if err != nil {
				b.Logf("stopped after %d mutations: %v", done, err)
				break
			}
			done++
		}
		b.StopTimer()
		if done > 0 {
			b.ReportMetric(float64(done)/time.Since(start).Seconds(), "mutations/sec")
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
