package kvfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forEachBackend(t *testing.T, fn func(t *testing.T, s objectstore.Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		s, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := objectstore.Open(context.Background(), "file://"+dir+"?create_dir=true")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

func TestPutAndGetBlob(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		tests := []struct {
			name string
			size int
		}{
			{"empty", 0},
			{"single_byte", 1},
			{"4KB", 4096},
			{"1MB", 1024 * 1024},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				data := make([]byte, tt.size)
				_, err := rand.Read(data)
				require.NoError(t, err)

				h := sha256.Sum256(data)
				expectedHash := hex.EncodeToString(h[:])

				hash, n, err := putBlob(ctx, s, bytes.NewReader(data))
				require.NoError(t, err)
				assert.Equal(t, expectedHash, hash)
				assert.Equal(t, int64(tt.size), n)

				rc, size, err := getBlob(ctx, s, hash)
				require.NoError(t, err)
				defer rc.Close()
				assert.Equal(t, int64(tt.size), size)

				readData, err := io.ReadAll(rc)
				require.NoError(t, err)
				assert.Equal(t, data, readData)
			})
		}
	})
}

func TestBlobDeduplication(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		data := []byte("deduplicated payload")

		h1, _, err := putBlob(ctx, s, bytes.NewReader(data))
		require.NoError(t, err)

		h2, _, err := putBlob(ctx, s, bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, h1, h2)

		rc, _, err := getBlob(ctx, s, h1)
		require.NoError(t, err)
		defer rc.Close()
		readData, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, data, readData)
	})
}

func TestGetBlobNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		_, _, err := getBlob(ctx, s, "0000000000000000000000000000000000000000000000000000000000000000")
		assert.True(t, errors.Is(err, objectstore.ErrNotFound))
	})
}

func TestGetBlob_InvalidHash(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		badHashes := []string{
			"",
			"short",
			"not-64-hex-chars",
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85G",
			"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855",
		}
		for _, hash := range badHashes {
			t.Run(hash, func(t *testing.T) {
				_, _, err := getBlob(ctx, s, hash)
				assert.ErrorIs(t, err, ErrInvalidHash)
			})
		}
	})
}

func TestConcurrentBlobUploads(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		const writers = 32
		data := []byte("concurrent shared blob")

		var wg sync.WaitGroup
		errCh := make(chan error, writers)

		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				var payload []byte
				if idx%2 == 0 {
					payload = data
				} else {
					payload = []byte(fmt.Sprintf("unique-%d", idx))
				}
				hash, n, err := putBlob(ctx, s, bytes.NewReader(payload))
				if err != nil {
					errCh <- err
					return
				}
				if n != int64(len(payload)) {
					errCh <- fmt.Errorf("unexpected size: %d != %d", n, len(payload))
					return
				}
				rc, _, err := getBlob(ctx, s, hash)
				if err != nil {
					errCh <- err
					return
				}
				_ = rc.Close()
			}(i)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}
	})
}

func FuzzBlobRoundTrip(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("a"))
	f.Add([]byte("hello cas storage"))
	f.Add(bytes.Repeat([]byte("x"), 4096))
	f.Add(bytes.Repeat([]byte("large content payload"), 500))

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		h := sha256.Sum256(data)
		expectedHash := hex.EncodeToString(h[:])

		// 1. In-memory backend
		memStore, err := objectstore.Open(ctx, "mem://")
		require.NoError(t, err)
		defer memStore.Close()

		memHash, memN, err := putBlob(ctx, memStore, bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, expectedHash, memHash)
		assert.Equal(t, int64(len(data)), memN)

		memRC, memSize, err := getBlob(ctx, memStore, memHash)
		require.NoError(t, err)
		assert.Equal(t, int64(len(data)), memSize)
		memOut, err := io.ReadAll(memRC)
		memRC.Close()
		require.NoError(t, err)
		assert.Equal(t, data, memOut)

		// 2. Disk backend
		diskStore, err := objectstore.Open(ctx, "file://"+t.TempDir()+"/bucket?create_dir=true")
		require.NoError(t, err)
		defer diskStore.Close()

		diskHash, diskN, err := putBlob(ctx, diskStore, bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, expectedHash, diskHash)
		assert.Equal(t, int64(len(data)), diskN)

		diskRC, diskSize, err := getBlob(ctx, diskStore, diskHash)
		require.NoError(t, err)
		assert.Equal(t, int64(len(data)), diskSize)
		diskOut, err := io.ReadAll(diskRC)
		diskRC.Close()
		require.NoError(t, err)
		assert.Equal(t, data, diskOut)
	})
}

func BenchmarkPutBlob(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	for _, size := range []int{1024, 64 * 1024, 1024 * 1024} {
		b.Run(fmt.Sprintf("size_%dB", size), func(b *testing.B) {
			data := make([]byte, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()

			for b.Loop() {
				_, _, err := putBlob(ctx, s, bytes.NewReader(data))
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGetBlob(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	data := make([]byte, 64*1024)
	hash, _, err := putBlob(ctx, s, bytes.NewReader(data))
	require.NoError(b, err)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	for b.Loop() {
		rc, _, err := getBlob(ctx, s, hash)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
}
