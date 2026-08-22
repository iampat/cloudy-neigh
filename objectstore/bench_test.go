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
	"golang.org/x/oauth2"
)

func benchStore(b *testing.B, open func(b *testing.B) objectstore.Store) {
	ctx := context.Background()
	payload := bytes.Repeat([]byte("x"), 1024)
	prefix := fmt.Sprintf("bench%d/", time.Now().UnixNano())

	// Every mutation benchmark uses a distinct key per iteration. GCS caps
	// mutations of one object at about one per second, so reusing a key
	// measures that limit instead of the write latency.
	b.Run("Put", func(b *testing.B) {
		s := open(b)
		nonce := time.Now().UnixNano()
		for i := 0; i < b.N; i++ {
			if _, err := s.Put(ctx, fmt.Sprintf("%sput/%d/%d", prefix, nonce, i), bytes.NewReader(payload)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Get", func(b *testing.B) {
		s := open(b)
		if _, err := s.Put(ctx, prefix+"get", bytes.NewReader(payload)); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r, err := s.Get(ctx, prefix+"get")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, r); err != nil {
				b.Fatal(err)
			}
			r.Close()
		}
	})
	b.Run("GetWithGeneration", func(b *testing.B) {
		s := open(b)
		if _, err := s.Put(ctx, prefix+"getgen", bytes.NewReader(payload)); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r, _, err := s.GetWithGeneration(ctx, prefix+"getgen")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, r); err != nil {
				b.Fatal(err)
			}
			r.Close()
		}
	})
	b.Run("PutIfAbsent", func(b *testing.B) {
		s := open(b)
		nonce := time.Now().UnixNano()
		for i := 0; i < b.N; i++ {
			key := fmt.Sprintf("%sabsent/%d/%d", prefix, nonce, i)
			if _, err := s.PutIfAbsent(ctx, key, bytes.NewReader(payload)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("PutIfGenerationMatch", func(b *testing.B) {
		s := open(b)
		nonce := time.Now().UnixNano()
		keys := make([]string, b.N)
		gens := make([]string, b.N)
		for i := range keys {
			keys[i] = fmt.Sprintf("%scas/%d/%d", prefix, nonce, i)
			gen, err := s.Put(ctx, keys[i], bytes.NewReader(payload))
			if err != nil {
				b.Fatal(err)
			}
			gens[i] = gen
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := s.PutIfGenerationMatch(ctx, keys[i], bytes.NewReader(payload), gens[i]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// SameKeyMutationRate reports the sustained mutation rate of one key, which
// is the rate a branch ref sees. It is a rate, not a latency: GCS answers a
// mutation of the same object at about one per second.
func benchSameKeyRate(b *testing.B, open func(b *testing.B) objectstore.Store) {
	b.Run("SameKeyMutationRate", func(b *testing.B) {
		s := open(b)
		ctx := context.Background()
		payload := bytes.Repeat([]byte("x"), 1024)
		key := fmt.Sprintf("hot%d", time.Now().UnixNano())
		gen, err := s.Put(ctx, key, bytes.NewReader(payload))
		if err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		start := time.Now()
		done := 0
		for i := 0; i < b.N; i++ {
			gen, err = s.PutIfGenerationMatch(ctx, key, bytes.NewReader(payload), gen)
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
	benchStore(b, func(b *testing.B) objectstore.Store {
		s := objectstore.OpenMem()
		b.Cleanup(func() { s.Close() })
		return s
	})
}

func BenchmarkDisk(b *testing.B) {
	benchStore(b, func(b *testing.B) objectstore.Store {
		s, err := objectstore.OpenDisk(b.TempDir() + "/bucket")
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { s.Close() })
		return s
	})
}

func BenchmarkGCSSameKeyRate(b *testing.B) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		b.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	var ts oauth2.TokenSource
	if tok := os.Getenv("OBJECTSTORE_TEST_GCS_TOKEN"); tok != "" {
		ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})
	}
	benchSameKeyRate(b, func(b *testing.B) objectstore.Store {
		s, err := objectstore.OpenGCS(context.Background(), bucket, ts)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { s.Close() })
		return s
	})
}

func BenchmarkGCS(b *testing.B) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		b.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	var ts oauth2.TokenSource
	if tok := os.Getenv("OBJECTSTORE_TEST_GCS_TOKEN"); tok != "" {
		ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})
	}
	benchStore(b, func(b *testing.B) objectstore.Store {
		s, err := objectstore.OpenGCS(context.Background(), bucket, ts)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { s.Close() })
		return s
	})
}
