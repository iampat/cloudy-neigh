package kvfs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreOpen(t *testing.T) {
	_, err := kvfs.Open(nil, nil)
	assert.ErrorIs(t, err, kvfs.ErrNilStore)

	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)

	ks, err := kvfs.Open(s, &kvfs.Options{WALFlushInterval: 100 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, ks.Close())
}

func TestStoreCRUDSync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s, nil)
		require.NoError(t, err)
		defer ks.Close()

		// 1. Get from non-existent branch returns ErrNotFound
		_, err = ks.Get(ctx, "main", "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)

		// 2. Set key on new branch initializes branch and sets key
		val1 := []byte("hello kvfs value")
		err = ks.Set(ctx, "main", "k1", bytes.NewReader(val1), kvfs.Sync)
		require.NoError(t, err)

		// 3. Get key returns matching payload and size
		v, err := ks.Get(ctx, "main", "k1")
		require.NoError(t, err)
		assert.Equal(t, int64(len(val1)), v.Size)

		got, err := io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val1, got)

		// 4. Set second key on existing branch (nil opts defaults to Sync)
		val2 := []byte("second value")
		err = ks.Set(ctx, "main", "k2", bytes.NewReader(val2), nil)
		require.NoError(t, err)

		// 5. Overwrite first key
		val1Updated := []byte("hello updated")
		err = ks.Set(ctx, "main", "k1", bytes.NewReader(val1Updated), kvfs.Sync)
		require.NoError(t, err)

		v, err = ks.Get(ctx, "main", "k1")
		require.NoError(t, err)
		got, _ = io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, val1Updated, got)

		// 6. Delete key
		err = ks.Delete(ctx, "main", "k1", kvfs.Sync)
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
		err = ks.Delete(ctx, "main", "k1", nil)
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func TestStoreCRUDAsync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s, &kvfs.Options{WALFlushInterval: 20 * time.Millisecond})
		require.NoError(t, err)

		// 1. Set key asynchronously with NoSync
		val1 := []byte("async value 1")
		err = ks.Set(ctx, "main", "k1", bytes.NewReader(val1), kvfs.NoSync)
		require.NoError(t, err)

		val2 := []byte("async value 2")
		err = ks.Set(ctx, "main", "k2", bytes.NewReader(val2), kvfs.NoSync)
		require.NoError(t, err)

		// Wait for background flusher to compact WAL -> manifest
		require.Eventually(t, func() bool {
			v, err := ks.Get(ctx, "main", "k1")
			if err != nil {
				return false
			}
			got, _ := io.ReadAll(v.Data)
			v.Data.Close()
			return bytes.Equal(val1, got)
		}, 1*time.Second, 10*time.Millisecond)

		v, err := ks.Get(ctx, "main", "k2")
		require.NoError(t, err)
		got, _ := io.ReadAll(v.Data)
		v.Data.Close()
		assert.Equal(t, val2, got)

		// 2. Delete key asynchronously
		err = ks.Delete(ctx, "main", "k1", kvfs.NoSync)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			_, err := ks.Get(ctx, "main", "k1")
			return errors.Is(err, objectstore.ErrNotFound)
		}, 1*time.Second, 10*time.Millisecond)

		// 3. Close flushes any tail WAL records
		val3 := []byte("async value 3 before close")
		err = ks.Set(ctx, "main", "k3", bytes.NewReader(val3), kvfs.NoSync)
		require.NoError(t, err)

		require.NoError(t, ks.Close())

		// Reopen store and verify k3 was flushed on close
		ks2, err := kvfs.Open(s, nil)
		require.NoError(t, err)
		defer ks2.Close()

		v3, err := ks2.Get(ctx, "main", "k3")
		require.NoError(t, err)
		got3, _ := io.ReadAll(v3.Data)
		v3.Data.Close()
		assert.Equal(t, val3, got3)
	})
}

func TestStoreBranchIsolation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s, nil)
		require.NoError(t, err)
		defer ks.Close()

		// 1. Populate main branch
		err = ks.Set(ctx, "main", "shared.txt", bytes.NewReader([]byte("v1")), kvfs.Sync)
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
		err = ks.Set(ctx, "dev", "shared.txt", bytes.NewReader([]byte("v2-dev")), kvfs.Sync)
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
		ks, err := kvfs.Open(s, nil)
		require.NoError(t, err)
		defer ks.Close()

		assert.ErrorIs(t, ks.Set(ctx, "main", "", bytes.NewReader([]byte("x")), nil), kvfs.ErrInvalidKey)
		assert.ErrorIs(t, ks.Set(ctx, "main", "k", nil, nil), kvfs.ErrNilReader)
		assert.ErrorIs(t, ks.Delete(ctx, "main", "", nil), kvfs.ErrInvalidKey)

		_, err = ks.Get(ctx, "main", "")
		assert.ErrorIs(t, err, kvfs.ErrInvalidKey)

		assert.ErrorIs(t, ks.Set(ctx, "invalid//branch", "k", bytes.NewReader([]byte("x")), nil), kvfs.ErrInvalidBranchName)
		assert.ErrorIs(t, ks.Set(ctx, "invalid//branch", "k", bytes.NewReader([]byte("x")), kvfs.NoSync), kvfs.ErrInvalidBranchName)
	})
}

func TestStoreConcurrentSets(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(s, &kvfs.Options{WALFlushInterval: 10 * time.Millisecond})
		require.NoError(t, err)
		defer ks.Close()

		const writers = 16
		var wg sync.WaitGroup
		errCh := make(chan error, writers)

		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				key := fmt.Sprintf("key-%d", idx)
				val := []byte(fmt.Sprintf("val-%d", idx))
				var opts *kvfs.WriteOptions
				if idx%2 == 0 {
					opts = kvfs.Sync
				} else {
					opts = kvfs.NoSync
				}
				if err := ks.Set(ctx, "concurrent-branch", key, bytes.NewReader(val), opts); err != nil {
					errCh <- fmt.Errorf("Set(%s): %w", key, err)
				}
			}(i)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}

		// Verify all 16 keys are present after flush
		require.Eventually(t, func() bool {
			for i := 0; i < writers; i++ {
				key := fmt.Sprintf("key-%d", i)
				expectedVal := []byte(fmt.Sprintf("val-%d", i))
				v, err := ks.Get(ctx, "concurrent-branch", key)
				if err != nil {
					return false
				}
				got, err := io.ReadAll(v.Data)
				v.Data.Close()
				if err != nil || !bytes.Equal(expectedVal, got) {
					return false
				}
			}
			return true
		}, 2*time.Second, 20*time.Millisecond)
	})
}

func BenchmarkStoreSetSync(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	defer s.Close()
	ctx := context.Background()

	ks, err := kvfs.Open(s, nil)
	require.NoError(b, err)
	defer ks.Close()

	val := []byte("benchmark-payload")
	b.ReportAllocs()

	for b.Loop() {
		err := ks.Set(ctx, "bench-branch", "k", bytes.NewReader(val), kvfs.Sync)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreSetAsync(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	defer s.Close()
	ctx := context.Background()

	ks, err := kvfs.Open(s, &kvfs.Options{WALFlushInterval: 50 * time.Millisecond})
	require.NoError(b, err)
	defer ks.Close()

	val := []byte("benchmark-payload")
	b.ReportAllocs()

	for b.Loop() {
		err := ks.Set(ctx, "bench-branch", "k", bytes.NewReader(val), kvfs.NoSync)
		if err != nil {
			b.Fatal(err)
		}
	}
}
