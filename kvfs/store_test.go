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
	ctx := context.Background()

	_, err := kvfs.Open(ctx, nil, "main", nil)
	assert.ErrorIs(t, err, kvfs.ErrNilStore)

	s, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer s.Close()

	_, err = kvfs.Open(ctx, s, "invalid//branch", nil)
	assert.ErrorIs(t, err, kvfs.ErrInvalidBranchName)

	_, err = kvfs.Open(ctx, s, "nonexistent", nil)
	assert.ErrorIs(t, err, objectstore.ErrNotFound)

	ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{WALFlushInterval: 100 * time.Millisecond})
	require.NoError(t, err)
	assert.Equal(t, "main", ks.Branch())
	require.NoError(t, ks.Close())
}

func TestStoreCRUDSync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		_, err = ks.Get(ctx, "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)

		val1 := []byte("hello kvfs value")
		err = ks.Set(ctx, "k1", bytes.NewReader(val1), kvfs.Sync)
		require.NoError(t, err)

		v, err := ks.Get(ctx, "k1")
		require.NoError(t, err)
		assert.Equal(t, int64(len(val1)), v.Size)

		got, err := io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val1, got)

		val2 := []byte("second value")
		err = ks.Set(ctx, "k2", bytes.NewReader(val2), nil)
		require.NoError(t, err)

		val1Updated := []byte("hello updated")
		err = ks.Set(ctx, "k1", bytes.NewReader(val1Updated), kvfs.Sync)
		require.NoError(t, err)

		v, err = ks.Get(ctx, "k1")
		require.NoError(t, err)
		got, err = io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val1Updated, got)

		err = ks.Delete(ctx, "k1", kvfs.Sync)
		require.NoError(t, err)

		_, err = ks.Get(ctx, "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)

		v, err = ks.Get(ctx, "k2")
		require.NoError(t, err)
		got, err = io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val2, got)

		err = ks.Delete(ctx, "k1", nil)
		require.NoError(t, err)
	})
}

func TestStoreCRUDAsync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{WALFlushInterval: 20 * time.Millisecond})
		require.NoError(t, err)

		val1 := []byte("async value 1")
		err = ks.Set(ctx, "k1", bytes.NewReader(val1), kvfs.NoSync)
		require.NoError(t, err)

		val2 := []byte("async value 2")
		err = ks.Set(ctx, "k2", bytes.NewReader(val2), kvfs.NoSync)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			v, err := ks.Get(ctx, "k1")
			if err != nil {
				return false
			}
			got, err := io.ReadAll(v.Data)
			v.Data.Close()
			return err == nil && bytes.Equal(val1, got)
		}, 1*time.Second, 10*time.Millisecond)

		v, err := ks.Get(ctx, "k2")
		require.NoError(t, err)
		got, err := io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val2, got)

		err = ks.Delete(ctx, "k1", kvfs.NoSync)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			_, err := ks.Get(ctx, "k1")
			return errors.Is(err, objectstore.ErrNotFound)
		}, 1*time.Second, 10*time.Millisecond)

		val3 := []byte("async value 3 before close")
		err = ks.Set(ctx, "k3", bytes.NewReader(val3), kvfs.NoSync)
		require.NoError(t, err)

		require.NoError(t, ks.Close())

		ks2, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks2.Close()

		v3, err := ks2.Get(ctx, "k3")
		require.NoError(t, err)
		got3, err := io.ReadAll(v3.Data)
		require.NoError(t, err)
		v3.Data.Close()
		assert.Equal(t, val3, got3)
	})
}

func TestStoreFork(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ksMain, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ksMain.Close()

		err = ksMain.Set(ctx, "shared.txt", bytes.NewReader([]byte("v1")), kvfs.Sync)
		require.NoError(t, err)

		ksDev, err := ksMain.Fork(ctx, "dev")
		require.NoError(t, err)
		defer ksDev.Close()
		assert.Equal(t, "dev", ksDev.Branch())

		v, err := ksDev.Get(ctx, "shared.txt")
		require.NoError(t, err)
		got, err := io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, []byte("v1"), got)

		err = ksDev.Set(ctx, "shared.txt", bytes.NewReader([]byte("v2-dev")), kvfs.Sync)
		require.NoError(t, err)

		v, err = ksMain.Get(ctx, "shared.txt")
		require.NoError(t, err)
		got, err = io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, []byte("v1"), got)

		v, err = ksDev.Get(ctx, "shared.txt")
		require.NoError(t, err)
		got, err = io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, []byte("v2-dev"), got)
	})
}

func TestStoreValidationErrors(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		assert.ErrorIs(t, ks.Set(ctx, "", bytes.NewReader([]byte("x")), nil), kvfs.ErrInvalidKey)
		assert.ErrorIs(t, ks.Set(ctx, "k", nil, nil), kvfs.ErrNilReader)
		assert.ErrorIs(t, ks.Delete(ctx, "", nil), kvfs.ErrInvalidKey)

		_, err = ks.Get(ctx, "")
		assert.ErrorIs(t, err, kvfs.ErrInvalidKey)
	})
}

func TestStoreConcurrentSets(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{WALFlushInterval: 10 * time.Millisecond})
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
				if err := ks.Set(ctx, key, bytes.NewReader(val), opts); err != nil {
					errCh <- fmt.Errorf("Set(%s): %w", key, err)
				}
			}(i)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}

		require.Eventually(t, func() bool {
			for i := 0; i < writers; i++ {
				key := fmt.Sprintf("key-%d", i)
				expectedVal := []byte(fmt.Sprintf("val-%d", i))
				v, err := ks.Get(ctx, key)
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

func TestBatchKeyOrdering(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		b := ks.NewBatch()
		require.NoError(t, b.Set(ctx, "k1", bytes.NewReader([]byte("v1"))))
		require.NoError(t, b.Set(ctx, "k1", bytes.NewReader([]byte("v2"))))
		require.NoError(t, b.Set(ctx, "k2", bytes.NewReader([]byte("v-temp"))))
		require.NoError(t, b.Delete("k2"))
		require.NoError(t, b.Commit(ctx, kvfs.Sync))

		v1, err := ks.Get(ctx, "k1")
		require.NoError(t, err)
		got1, err := io.ReadAll(v1.Data)
		require.NoError(t, err)
		v1.Data.Close()
		assert.Equal(t, []byte("v2"), got1)

		_, err = ks.Get(ctx, "k2")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func TestBatchSync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		b := ks.NewBatch()
		require.NoError(t, b.Set(ctx, "k1", bytes.NewReader([]byte("v1"))))
		require.NoError(t, b.Set(ctx, "k2", bytes.NewReader([]byte("v2"))))
		require.NoError(t, b.Commit(ctx, kvfs.Sync))

		v1, err := ks.Get(ctx, "k1")
		require.NoError(t, err)
		got1, err := io.ReadAll(v1.Data)
		require.NoError(t, err)
		v1.Data.Close()
		assert.Equal(t, []byte("v1"), got1)

		v2, err := ks.Get(ctx, "k2")
		require.NoError(t, err)
		got2, err := io.ReadAll(v2.Data)
		require.NoError(t, err)
		v2.Data.Close()
		assert.Equal(t, []byte("v2"), got2)

		b2 := ks.NewBatch()
		require.NoError(t, b2.Delete("k1"))
		require.NoError(t, b2.Set(ctx, "k2", bytes.NewReader([]byte("v2-updated"))))
		require.NoError(t, b2.Commit(ctx, nil))

		_, err = ks.Get(ctx, "k1")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)

		v2Up, err := ks.Get(ctx, "k2")
		require.NoError(t, err)
		got2Up, err := io.ReadAll(v2Up.Data)
		require.NoError(t, err)
		v2Up.Data.Close()
		assert.Equal(t, []byte("v2-updated"), got2Up)
	})
}

func TestBatchAsync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{WALFlushInterval: 20 * time.Millisecond})
		require.NoError(t, err)
		defer ks.Close()

		b := ks.NewBatch()
		require.NoError(t, b.Set(ctx, "k1", bytes.NewReader([]byte("async1"))))
		require.NoError(t, b.Set(ctx, "k2", bytes.NewReader([]byte("async2"))))
		require.NoError(t, b.Commit(ctx, kvfs.NoSync))

		require.Eventually(t, func() bool {
			v, err := ks.Get(ctx, "k1")
			if err != nil {
				return false
			}
			got, err := io.ReadAll(v.Data)
			v.Data.Close()
			return err == nil && bytes.Equal([]byte("async1"), got)
		}, 1*time.Second, 10*time.Millisecond)

		v2, err := ks.Get(ctx, "k2")
		require.NoError(t, err)
		got2, err := io.ReadAll(v2.Data)
		require.NoError(t, err)
		v2.Data.Close()
		assert.Equal(t, []byte("async2"), got2)
	})
}

func TestBatchEmptyCommit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		b := ks.NewBatch()
		require.NoError(t, b.Commit(ctx, kvfs.Sync))
	})
}

func TestBatchClosed(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		b := ks.NewBatch()
		require.NoError(t, b.Set(ctx, "k", bytes.NewReader([]byte("v"))))
		require.NoError(t, b.Commit(ctx, kvfs.Sync))

		assert.ErrorIs(t, b.Set(ctx, "k", bytes.NewReader([]byte("v"))), kvfs.ErrBatchClosed)
		assert.ErrorIs(t, b.Delete("k"), kvfs.ErrBatchClosed)
		assert.ErrorIs(t, b.Commit(ctx, kvfs.Sync), kvfs.ErrBatchClosed)

		b2 := ks.NewBatch()
		require.NoError(t, b2.Close())
		assert.ErrorIs(t, b2.Set(ctx, "k", bytes.NewReader([]byte("v"))), kvfs.ErrBatchClosed)
		assert.ErrorIs(t, b2.Delete("k"), kvfs.ErrBatchClosed)
		assert.ErrorIs(t, b2.Commit(ctx, kvfs.Sync), kvfs.ErrBatchClosed)
	})
}

func TestBatchValidationErrors(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		b := ks.NewBatch()
		assert.ErrorIs(t, b.Set(ctx, "", bytes.NewReader([]byte("v"))), kvfs.ErrInvalidKey)
		assert.ErrorIs(t, b.Set(ctx, "k", nil), kvfs.ErrNilReader)
		assert.ErrorIs(t, b.Delete(""), kvfs.ErrInvalidKey)
	})
}

func TestStoreReadCacheLease(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{
			ManifestLeaseTTL: 40 * time.Millisecond,
		})
		require.NoError(t, err)
		defer ks.Close()

		val1 := []byte("cached value 1")
		err = ks.Set(ctx, "k1", bytes.NewReader(val1), kvfs.Sync)
		require.NoError(t, err)

		v, err := ks.Get(ctx, "k1")
		require.NoError(t, err)
		got, err := io.ReadAll(v.Data)
		require.NoError(t, err)
		v.Data.Close()
		assert.Equal(t, val1, got)

		ksOther, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ksOther.Close()

		val2 := []byte("updated value from other")
		err = ksOther.Set(ctx, "k1", bytes.NewReader(val2), kvfs.Sync)
		require.NoError(t, err)

		vCached, err := ks.Get(ctx, "k1")
		require.NoError(t, err)
		gotCached, err := io.ReadAll(vCached.Data)
		require.NoError(t, err)
		vCached.Data.Close()
		assert.Equal(t, val1, gotCached)

		require.Eventually(t, func() bool {
			vRefreshed, err := ks.Get(ctx, "k1")
			if err != nil {
				return false
			}
			gotRefreshed, err := io.ReadAll(vRefreshed.Data)
			vRefreshed.Data.Close()
			return err == nil && bytes.Equal(val2, gotRefreshed)
		}, 1*time.Second, 10*time.Millisecond)
	})
}

func TestStoreReadCacheWriteThrough(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{
			ManifestLeaseTTL: 1 * time.Hour,
		})
		require.NoError(t, err)
		defer ks.Close()

		val1 := []byte("write-through 1")
		err = ks.Set(ctx, "k", bytes.NewReader(val1), kvfs.Sync)
		require.NoError(t, err)

		v1, err := ks.Get(ctx, "k")
		require.NoError(t, err)
		got1, err := io.ReadAll(v1.Data)
		require.NoError(t, err)
		v1.Data.Close()
		assert.Equal(t, val1, got1)

		val2 := []byte("write-through 2")
		err = ks.Set(ctx, "k", bytes.NewReader(val2), kvfs.Sync)
		require.NoError(t, err)

		v2, err := ks.Get(ctx, "k")
		require.NoError(t, err)
		got2, err := io.ReadAll(v2.Data)
		require.NoError(t, err)
		v2.Data.Close()
		assert.Equal(t, val2, got2)
	})
}

func TestStoreReadConcurrentSingleflight(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		ks, err := kvfs.Open(ctx, s, "main", nil)
		require.NoError(t, err)
		defer ks.Close()

		val := []byte("singleflight payload")
		err = ks.Set(ctx, "shared", bytes.NewReader(val), kvfs.Sync)
		require.NoError(t, err)

		const readers = 16
		var wg sync.WaitGroup
		errCh := make(chan error, readers)

		for i := 0; i < readers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := ks.Get(ctx, "shared")
				if err != nil {
					errCh <- err
					return
				}
				got, err := io.ReadAll(v.Data)
				v.Data.Close()
				if err != nil {
					errCh <- err
					return
				}
				if !bytes.Equal(val, got) {
					errCh <- fmt.Errorf("expected %s, got %s", val, got)
				}
			}()
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}
	})
}

func BenchmarkStoreSetSync(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	ks, err := kvfs.Open(ctx, s, "main", nil)
	require.NoError(b, err)
	b.Cleanup(func() { ks.Close() })

	val := []byte("benchmark-payload")
	b.ReportAllocs()

	for b.Loop() {
		err := ks.Set(ctx, "k", bytes.NewReader(val), kvfs.Sync)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreSetAsync(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{WALFlushInterval: 50 * time.Millisecond})
	require.NoError(b, err)
	b.Cleanup(func() { ks.Close() })

	val := []byte("benchmark-payload")
	b.ReportAllocs()

	for b.Loop() {
		err := ks.Set(ctx, "k", bytes.NewReader(val), kvfs.NoSync)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGetCached(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	ks, err := kvfs.Open(ctx, s, "main", &kvfs.Options{ManifestLeaseTTL: 1 * time.Hour})
	require.NoError(b, err)
	b.Cleanup(func() { ks.Close() })

	val := []byte("benchmark-payload")
	err = ks.Set(ctx, "k", bytes.NewReader(val), kvfs.Sync)
	require.NoError(b, err)

	b.ReportAllocs()

	for b.Loop() {
		v, err := ks.Get(ctx, "k")
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, v.Data)
		_ = v.Data.Close()
	}
}
