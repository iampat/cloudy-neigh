package objectstore_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type contractConfig struct {
	raceWriters int
	casWriters  int
	casIters    int
}

func read(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer r.Close()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func put(t *testing.T, s objectstore.Store, key, value string) string {
	t.Helper()
	gen, err := s.Put(context.Background(), key, strings.NewReader(value), objectstore.Condition{})
	require.NoError(t, err)
	return gen
}

func raceAbsentPut(t *testing.T, stores []objectstore.Store, key string, writers int) {
	t.Helper()
	var wins, losses atomic.Int32
	errCh := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		s := stores[i%len(stores)]
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Put(context.Background(), key, strings.NewReader(strconv.Itoa(i)), objectstore.Condition{Absent: true})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, objectstore.ErrPreconditionFailed):
				losses.Add(1)
			default:
				errCh <- fmt.Errorf("Put(Absent): %w", err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	assert.Equal(t, int32(1), wins.Load())
	assert.Equal(t, int32(writers-1), losses.Load())
}

func runContract(t *testing.T, open func(t *testing.T) objectstore.Store, cfg contractConfig) {
	ctx := context.Background()
	prefix := func(t *testing.T, s objectstore.Store) string {
		p := strings.ReplaceAll(t.Name(), "/", "_") + "/"
		objs, err := s.List(ctx, p, "", 0)
		require.NoError(t, err)
		for _, o := range objs {
			require.NoError(t, s.Delete(ctx, o.Key))
		}
		return p
	}

	t.Run("RoundTrip", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		gen := put(t, s, k, "v1")
		require.NotEmpty(t, gen)
		r, obj, err := s.Get(ctx, k)
		require.NoError(t, err)
		assert.Equal(t, gen, obj.Generation)
		assert.Equal(t, int64(2), obj.Size)
		assert.Equal(t, "v1", read(t, r))
		r, obj, err = s.Get(ctx, k)
		require.NoError(t, err)
		assert.Equal(t, gen, obj.Generation)
		assert.Equal(t, int64(2), obj.Size)
		assert.Equal(t, "v1", read(t, r))
	})

	t.Run("GenerationChangesOnIdenticalWrite", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		g1 := put(t, s, k, "same")
		g2 := put(t, s, k, "same")
		assert.NotEqual(t, g1, g2)
	})

	t.Run("AbsentCondition", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		gen, err := s.Put(ctx, k, strings.NewReader("v1"), objectstore.Condition{Absent: true})
		require.NoError(t, err)
		assert.NotEmpty(t, gen)
		gen, err = s.Put(ctx, k, strings.NewReader("v2"), objectstore.Condition{Absent: true})
		assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)
		assert.Empty(t, gen)
		r, _, err := s.Get(ctx, k)
		require.NoError(t, err)
		assert.Equal(t, "v1", read(t, r))
	})

	t.Run("AbsentConditionRace", func(t *testing.T) {
		s := open(t)
		raceAbsentPut(t, []objectstore.Store{s}, prefix(t, s)+"k", cfg.raceWriters)
	})

	t.Run("CompareAndSwapCounter", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "ctr"
		put(t, s, k, "0")
		var wg sync.WaitGroup
		errCh := make(chan error, cfg.casWriters)
		for i := 0; i < cfg.casWriters; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < cfg.casIters; n++ {
					for {
						r, obj, err := s.Get(ctx, k)
						if err != nil {
							errCh <- fmt.Errorf("Get: %w", err)
							return
						}
						data, err := io.ReadAll(r)
						r.Close()
						if err != nil {
							errCh <- err
							return
						}
						v, err := strconv.Atoi(string(data))
						if err != nil {
							errCh <- fmt.Errorf("counter payload %q", data)
							return
						}
						_, err = s.Put(ctx, k, strings.NewReader(strconv.Itoa(v+1)), objectstore.Condition{GenerationMatch: obj.Generation})
						if err == nil {
							break
						}
						if !errors.Is(err, objectstore.ErrPreconditionFailed) {
							errCh <- fmt.Errorf("Put(GenerationMatch): %w", err)
							return
						}
					}
				}
			}()
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}
		r, _, err := s.Get(ctx, k)
		require.NoError(t, err)
		want := strconv.Itoa(cfg.casWriters * cfg.casIters)
		assert.Equal(t, want, read(t, r))
	})

	t.Run("StaleGenerationFails", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		g1 := put(t, s, k, "v1")
		put(t, s, k, "v2")
		gen, err := s.Put(ctx, k, strings.NewReader("v3"), objectstore.Condition{GenerationMatch: g1})
		assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)
		assert.Empty(t, gen)
	})

	t.Run("DeleteRecreateInvalidatesOldGeneration", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		g1 := put(t, s, k, "v1")
		require.NoError(t, s.Delete(ctx, k))
		put(t, s, k, "v1")
		_, err := s.Put(ctx, k, strings.NewReader("v2"), objectstore.Condition{GenerationMatch: g1})
		assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)
	})

	t.Run("AbsentKey", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "nope"
		_, _, err := s.Get(ctx, k)
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
		assert.ErrorIs(t, s.Delete(ctx, k), objectstore.ErrNotFound)
		gen := put(t, s, k, "v")
		require.NoError(t, s.Delete(ctx, k))
		_, err = s.Put(ctx, k, strings.NewReader("v"), objectstore.Condition{GenerationMatch: gen})
		assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)
	})

	t.Run("MalformedGeneration", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		put(t, s, k, "v1")
		_, err := s.Put(ctx, k, strings.NewReader("v2"), objectstore.Condition{GenerationMatch: "not-a-token"})
		assert.Error(t, err)
		assert.NotErrorIs(t, err, objectstore.ErrPreconditionFailed)
	})

	t.Run("InvalidCondition", func(t *testing.T) {
		for name, cond := range map[string]objectstore.Condition{
			"bothSet": {Absent: true, GenerationMatch: "12345"},
		} {
			t.Run(name, func(t *testing.T) {
				s := open(t)
				k := prefix(t, s) + "k"
				put(t, s, k, "v1")
				_, err := s.Put(ctx, k, strings.NewReader("v2"), cond)
				assert.Error(t, err)
				assert.NotErrorIs(t, err, objectstore.ErrPreconditionFailed)
			})
		}
	})

	t.Run("NilConditionOverwrites", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		put(t, s, k, "v1")
		_, err := s.Put(ctx, k, strings.NewReader("v1"), objectstore.Condition{})
		assert.NoError(t, err)
		_, err = s.Put(ctx, k, strings.NewReader("v2"), objectstore.Condition{})
		assert.NoError(t, err)
		r, _, err := s.Get(ctx, k)
		require.NoError(t, err)
		defer r.Close()
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "v2", string(got))
	})

	t.Run("CanceledContext", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := s.Put(canceled, k, strings.NewReader("v"), objectstore.Condition{})
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("List", func(t *testing.T) {
		s := open(t)
		p := prefix(t, s)
		for _, k := range []string{"a/1", "a/2", "a/3", "b/1"} {
			put(t, s, p+k, "v")
		}
		objs, err := s.List(ctx, p+"a/", "", 0)
		require.NoError(t, err)
		var keys []string
		for _, o := range objs {
			keys = append(keys, o.Key)
			assert.Equal(t, int64(1), o.Size)
			assert.NotEmpty(t, o.Generation)
		}
		assert.Equal(t, []string{p + "a/1", p + "a/2", p + "a/3"}, keys)
		require.NotEmpty(t, objs)
		r, _, err := s.Get(ctx, objs[0].Key)
		require.NoError(t, err)
		r.Close()
		obj, err := s.Stat(ctx, objs[0].Key)
		require.NoError(t, err)
		assert.Equal(t, obj.Generation, objs[0].Generation)
		objs, err = s.List(ctx, p+"a/", p+"a/1", 0)
		require.NoError(t, err)
		require.Len(t, objs, 2)
		assert.Equal(t, p+"a/2", objs[0].Key)
		objs, err = s.List(ctx, p+"a/", "", 1)
		require.NoError(t, err)
		require.Len(t, objs, 1)
		assert.Equal(t, p+"a/1", objs[0].Key)
	})

	t.Run("Exists", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "exists"
		ok, err := s.Exists(ctx, k)
		assert.NoError(t, err)
		assert.False(t, ok)
		put(t, s, k, "val")
		ok, err = s.Exists(ctx, k)
		assert.NoError(t, err)
		assert.True(t, ok)
		require.NoError(t, s.Delete(ctx, k))
		ok, err = s.Exists(ctx, k)
		assert.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("Stat", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "stat"
		gen := put(t, s, k, "hello")
		obj, err := s.Stat(ctx, k)
		require.NoError(t, err)
		assert.Equal(t, k, obj.Key)
		assert.Equal(t, gen, obj.Generation)
		assert.Equal(t, int64(5), obj.Size)

		_, err = s.Stat(ctx, prefix(t, s)+"nope")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})

	t.Run("ReadRange", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "range"
		content := "0123456789abcdefghijklmnopqrstuvwxyz"
		gen := put(t, s, k, content)
		totalSize := int64(len(content))

		rc, obj, err := s.ReadRange(ctx, k, 10, 6)
		require.NoError(t, err)
		assert.Equal(t, gen, obj.Generation)
		assert.Equal(t, totalSize, obj.Size)
		assert.Equal(t, "abcdef", read(t, rc))

		rc, _, err = s.ReadRange(ctx, k, 0, 10)
		require.NoError(t, err)
		assert.Equal(t, "0123456789", read(t, rc))

		rc, _, err = s.ReadRange(ctx, k, 26, 10)
		require.NoError(t, err)
		assert.Equal(t, "qrstuvwxyz", read(t, rc))

		rc, _, err = s.ReadRange(ctx, k, 30, 20)
		require.NoError(t, err)
		assert.Equal(t, "uvwxyz", read(t, rc))

		rc, _, err = s.ReadRange(ctx, k, 5, 0)
		require.NoError(t, err)
		assert.Equal(t, "", read(t, rc))

		rc, _, err = s.ReadRange(ctx, k, totalSize, 5)
		require.NoError(t, err)
		assert.Equal(t, "", read(t, rc))

		_, _, err = s.ReadRange(ctx, k, -1, 5)
		assert.Error(t, err)

		_, _, err = s.ReadRange(ctx, k, 5, -1)
		assert.Error(t, err)

		_, _, err = s.ReadRange(ctx, k, totalSize+1, 5)
		assert.Error(t, err)

		_, _, err = s.ReadRange(ctx, prefix(t, s)+"nope", 0, 5)
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func openURL(tb testing.TB, url string) objectstore.Store {
	tb.Helper()
	s, err := objectstore.Open(context.Background(), url)
	require.NoError(tb, err)
	return s
}

func TestMem(t *testing.T) {
	runContract(t, func(t *testing.T) objectstore.Store {
		s := openURL(t, "mem://")
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 32, casWriters: 8, casIters: 50})
}

func TestDisk(t *testing.T) {
	runContract(t, func(t *testing.T) objectstore.Store {
		s := openURL(t, "file://"+t.TempDir()+"/bucket?create_dir=true")
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 32, casWriters: 8, casIters: 50})
}
