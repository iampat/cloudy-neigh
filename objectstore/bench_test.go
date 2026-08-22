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

	b.Run("Put", func(b *testing.B) {
		s := open(b)
		for i := 0; i < b.N; i++ {
			if _, err := s.Put(ctx, prefix+"put", bytes.NewReader(payload)); err != nil {
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
		gen, err := s.Put(ctx, prefix+"cas", bytes.NewReader(payload))
		if err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			gen, err = s.PutIfGenerationMatch(ctx, prefix+"cas", bytes.NewReader(payload), gen)
			if err != nil {
				b.Fatal(err)
			}
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
