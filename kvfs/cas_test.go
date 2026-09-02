package kvfs_test

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

	"github.com/iampat/cloudy-neigh/kvfs"
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

func TestBlobKey(t *testing.T) {
	tests := []struct {
		name string
		hash string
		want string
	}{
		{"empty", "", "objects/"},
		{"short", "abcdef", "objects/abcdef"},
		{
			"valid_sha256",
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"objects/e3/b0/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, kvfs.BlobKey(tt.hash))
		})
	}
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

				hash, n, err := kvfs.PutBlob(ctx, s, bytes.NewReader(data))
				require.NoError(t, err)
				assert.Equal(t, expectedHash, hash)
				assert.Equal(t, int64(tt.size), n)

				exists, err := kvfs.ExistsBlob(ctx, s, hash)
				require.NoError(t, err)
				assert.True(t, exists)

				rc, size, err := kvfs.GetBlob(ctx, s, hash)
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

		h1, _, err := kvfs.PutBlob(ctx, s, bytes.NewReader(data))
		require.NoError(t, err)

		h2, _, err := kvfs.PutBlob(ctx, s, bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, h1, h2)

		rc, _, err := kvfs.GetBlob(ctx, s, h1)
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
		_, _, err := kvfs.GetBlob(ctx, s, "0000000000000000000000000000000000000000000000000000000000000000")
		assert.True(t, errors.Is(err, objectstore.ErrNotFound))
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
				hash, n, err := kvfs.PutBlob(ctx, s, bytes.NewReader(payload))
				if err != nil {
					errCh <- err
					return
				}
				if n != int64(len(payload)) {
					errCh <- fmt.Errorf("unexpected size: %d != %d", n, len(payload))
					return
				}
				exists, err := kvfs.ExistsBlob(ctx, s, hash)
				if err != nil {
					errCh <- err
					return
				}
				if !exists {
					errCh <- fmt.Errorf("blob not found: %s", hash)
					return
				}
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
		store, err := objectstore.Open(ctx, "mem://")
		require.NoError(t, err)
		defer store.Close()

		h := sha256.Sum256(data)
		expectedHash := hex.EncodeToString(h[:])

		hash, n, err := kvfs.PutBlob(ctx, store, bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, expectedHash, hash)
		assert.Equal(t, int64(len(data)), n)

		rc, size, err := kvfs.GetBlob(ctx, store, hash)
		require.NoError(t, err)
		defer rc.Close()
		assert.Equal(t, int64(len(data)), size)

		out, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, data, out)
	})
}
