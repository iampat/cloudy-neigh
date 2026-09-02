package kvfs_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreOpen(t *testing.T) {
	_, err := kvfs.Open(nil)
	assert.Error(t, err)

	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)

	ks, err := kvfs.Open(s)
	require.NoError(t, err)
	require.NoError(t, ks.Close())
}

func TestStoreCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s)
		require.NoError(t, err)

		// 1. Get from non-existent branch returns ErrNotFound
		_, err = ks.Get(ctx, "main", "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)

		// 2. Set key on new branch initializes branch and sets key
		val1 := []byte("hello kvfs value")
		err = ks.Set(ctx, "main", "k1", bytes.NewReader(val1))
		require.NoError(t, err)

		// 3. Get key returns matching payload and size
		v, err := ks.Get(ctx, "main", "k1")
		require.NoError(t, err)
		assert.Equal(t, int64(len(val1)), v.Size)

		got, err := io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val1, got)

		// 4. Set second key on existing branch
		val2 := []byte("second value")
		err = ks.Set(ctx, "main", "k2", bytes.NewReader(val2))
		require.NoError(t, err)

		// 5. Overwrite first key
		val1Updated := []byte("hello updated")
		err = ks.Set(ctx, "main", "k1", bytes.NewReader(val1Updated))
		require.NoError(t, err)

		v, err = ks.Get(ctx, "main", "k1")
		require.NoError(t, err)
		got, _ = io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, val1Updated, got)

		// 6. Delete key
		err = ks.Delete(ctx, "main", "k1")
		require.NoError(t, err)

		_, err = ks.Get(ctx, "main", "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)

		// Key 2 is still present
		v, err = ks.Get(ctx, "main", "k2")
		require.NoError(t, err)
		got, _ = io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, val2, got)

		// Deleting non-existent key returns ErrNotFound
		err = ks.Delete(ctx, "main", "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func TestStoreBranchIsolation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s)
		require.NoError(t, err)

		// 1. Populate main branch
		err = ks.Set(ctx, "main", "shared.txt", bytes.NewReader([]byte("v1")))
		require.NoError(t, err)

		// 2. Fork dev branch from main
		err = ks.Branch(ctx, "dev", "main")
		require.NoError(t, err)

		// 3. Both branches see shared.txt
		v, err := ks.Get(ctx, "dev", "shared.txt")
		require.NoError(t, err)
		got, _ := io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, []byte("v1"), got)

		// 4. Mutate shared.txt on dev branch
		err = ks.Set(ctx, "dev", "shared.txt", bytes.NewReader([]byte("v2-dev")))
		require.NoError(t, err)

		// 5. Main branch is unmodified
		v, err = ks.Get(ctx, "main", "shared.txt")
		require.NoError(t, err)
		got, _ = io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, []byte("v1"), got)

		// 6. Dev branch has updated value
		v, err = ks.Get(ctx, "dev", "shared.txt")
		require.NoError(t, err)
		got, _ = io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, []byte("v2-dev"), got)
	})
}

func TestStoreValidationErrors(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s)
		require.NoError(t, err)

		assert.ErrorIs(t, ks.Set(ctx, "main", "", bytes.NewReader([]byte("x"))), kvfs.ErrInvalidKey)
		assert.Error(t, ks.Set(ctx, "main", "k", nil))
		assert.ErrorIs(t, ks.Delete(ctx, "main", ""), kvfs.ErrInvalidKey)

		_, err = ks.Get(ctx, "main", "")
		assert.ErrorIs(t, err, kvfs.ErrInvalidKey)

		assert.ErrorIs(t, ks.Set(ctx, "invalid//branch", "k", bytes.NewReader([]byte("x"))), kvfs.ErrInvalidBranchName)
	})
}

func TestStoreConcurrentSets(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s)
		require.NoError(t, err)

		const writers = 16
		var wg sync.WaitGroup
		errCh := make(chan error, writers)

		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				key := fmt.Sprintf("key-%d", idx)
				val := []byte(fmt.Sprintf("val-%d", idx))
				if err := ks.Set(ctx, "concurrent-branch", key, bytes.NewReader(val)); err != nil {
					errCh <- fmt.Errorf("Set(%s): %w", key, err)
				}
			}(i)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}

		// Verify all 16 keys were preserved across retries
		for i := 0; i < writers; i++ {
			key := fmt.Sprintf("key-%d", i)
			expectedVal := []byte(fmt.Sprintf("val-%d", i))
			v, err := ks.Get(ctx, "concurrent-branch", key)
			require.NoError(t, err, "missing key %s", key)
			got, err := io.ReadAll(v.Data)
			require.NoError(t, err)
			v.Data.Close()
			assert.Equal(t, expectedVal, got)
		}
	})
}

func BenchmarkStoreSet(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	ks, err := kvfs.Open(s)
	require.NoError(b, err)

	val := []byte("benchmark-payload")
	b.ReportAllocs()

	for b.Loop() {
		err := ks.Set(ctx, "bench-branch", "k", bytes.NewReader(val))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGet(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	ks, err := kvfs.Open(s)
	require.NoError(b, err)

	val := []byte("benchmark-payload")
	err = ks.Set(ctx, "bench-branch", "k", bytes.NewReader(val))
	require.NoError(b, err)

	b.ReportAllocs()

	for b.Loop() {
		v, err := ks.Get(ctx, "bench-branch", "k")
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, v.Data)
		_ = v.Data.Close()
	}
}
