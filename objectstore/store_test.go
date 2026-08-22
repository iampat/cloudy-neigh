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
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
)

type contractConfig struct {
	raceWriters    int
	casWriters     int
	casIters       int
	listGeneration bool
}

func read(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func put(t *testing.T, s objectstore.Store, key, value string) string {
	t.Helper()
	gen, err := s.Put(context.Background(), key, strings.NewReader(value))
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	return gen
}

func runContract(t *testing.T, open func(t *testing.T) objectstore.Store, cfg contractConfig) {
	ctx := context.Background()
	prefix := func(t *testing.T) string {
		return fmt.Sprintf("t%d/", time.Now().UnixNano())
	}

	t.Run("RoundTrip", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		gen := put(t, s, k, "v1")
		if gen == "" {
			t.Fatal("Put returned an empty generation")
		}
		r, err := s.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got := read(t, r); got != "v1" {
			t.Fatalf("Get = %q, want %q", got, "v1")
		}
		r, gen2, err := s.GetWithGeneration(ctx, k)
		if err != nil {
			t.Fatalf("GetWithGeneration: %v", err)
		}
		if got := read(t, r); got != "v1" {
			t.Fatalf("GetWithGeneration = %q, want %q", got, "v1")
		}
		if gen2 != gen {
			t.Fatalf("generation = %q, want %q", gen2, gen)
		}
	})

	t.Run("GenerationChangesOnIdenticalWrite", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		g1 := put(t, s, k, "same")
		g2 := put(t, s, k, "same")
		if g1 == g2 {
			t.Fatalf("generation did not change: %q", g1)
		}
	})

	t.Run("PutIfAbsent", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		gen, err := s.PutIfAbsent(ctx, k, strings.NewReader("v1"))
		if err != nil || gen == "" {
			t.Fatalf("PutIfAbsent fresh = (%q, %v)", gen, err)
		}
		gen, err = s.PutIfAbsent(ctx, k, strings.NewReader("v2"))
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("PutIfAbsent existing = %v, want ErrPreconditionFailed", err)
		}
		if gen != "" {
			t.Fatalf("failed PutIfAbsent returned generation %q", gen)
		}
		r, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if got := read(t, r); got != "v1" {
			t.Fatalf("loser overwrote: %q", got)
		}
	})

	t.Run("PutIfAbsentRace", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		var wins, losses atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < cfg.raceWriters; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := s.PutIfAbsent(ctx, k, strings.NewReader(strconv.Itoa(i)))
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, objectstore.ErrPreconditionFailed):
					losses.Add(1)
				default:
					t.Errorf("PutIfAbsent: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if wins.Load() != 1 || losses.Load() != int32(cfg.raceWriters-1) {
			t.Fatalf("wins=%d losses=%d, want 1 and %d", wins.Load(), losses.Load(), cfg.raceWriters-1)
		}
	})

	t.Run("CompareAndSwapCounter", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "ctr"
		put(t, s, k, "0")
		var wg sync.WaitGroup
		for i := 0; i < cfg.casWriters; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < cfg.casIters; n++ {
					for {
						r, gen, err := s.GetWithGeneration(ctx, k)
						if err != nil {
							t.Errorf("GetWithGeneration: %v", err)
							return
						}
						data, err := io.ReadAll(r)
						r.Close()
						if err != nil {
							t.Error(err)
							return
						}
						v, err := strconv.Atoi(string(data))
						if err != nil {
							t.Errorf("counter payload %q", data)
							return
						}
						_, err = s.PutIfGenerationMatch(ctx, k, strings.NewReader(strconv.Itoa(v+1)), gen)
						if err == nil {
							break
						}
						if !errors.Is(err, objectstore.ErrPreconditionFailed) {
							t.Errorf("PutIfGenerationMatch: %v", err)
							return
						}
					}
				}
			}()
		}
		wg.Wait()
		r, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		want := strconv.Itoa(cfg.casWriters * cfg.casIters)
		if got := read(t, r); got != want {
			t.Fatalf("counter = %q, want %q", got, want)
		}
	})

	t.Run("StaleGenerationFails", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		g1 := put(t, s, k, "v1")
		put(t, s, k, "v2")
		gen, err := s.PutIfGenerationMatch(ctx, k, strings.NewReader("v3"), g1)
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("stale CAS = %v, want ErrPreconditionFailed", err)
		}
		if gen != "" {
			t.Fatalf("failed CAS returned generation %q", gen)
		}
	})

	t.Run("DeleteRecreateInvalidatesOldToken", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		g1 := put(t, s, k, "v1")
		if err := s.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		put(t, s, k, "v1")
		if _, err := s.PutIfGenerationMatch(ctx, k, strings.NewReader("v2"), g1); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("old token after re-create = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("AbsentKey", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "nope"
		if _, err := s.Get(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
		if _, _, err := s.GetWithGeneration(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("GetWithGeneration = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Delete = %v, want ErrNotFound", err)
		}
		if _, err := s.PutIfGenerationMatch(ctx, k, strings.NewReader("v"), "12345"); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("CAS on absent key = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("EmptyTokenRejected", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		put(t, s, k, "v1")
		_, err := s.PutIfGenerationMatch(ctx, k, strings.NewReader("v2"), "")
		if err == nil || errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("empty token = %v, want a plain error", err)
		}
	})

	t.Run("CanceledContext", func(t *testing.T) {
		s := open(t)
		k := prefix(t) + "k"
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Put(canceled, k, strings.NewReader("v")); !errors.Is(err, context.Canceled) {
			t.Fatalf("Put = %v, want context.Canceled", err)
		}
	})

	t.Run("List", func(t *testing.T) {
		s := open(t)
		p := prefix(t)
		for _, k := range []string{"a/1", "a/2", "a/3", "b/1"} {
			put(t, s, p+k, "v")
		}
		objs, err := s.List(ctx, p+"a/", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, o := range objs {
			keys = append(keys, o.Key)
			if o.Size != 1 {
				t.Errorf("size of %q = %d, want 1", o.Key, o.Size)
			}
			if cfg.listGeneration && o.Generation == "" {
				t.Errorf("missing generation for %q", o.Key)
			}
			if !cfg.listGeneration && o.Generation != "" {
				t.Errorf("unexpected generation %q for %q", o.Generation, o.Key)
			}
		}
		want := []string{p + "a/1", p + "a/2", p + "a/3"}
		if fmt.Sprint(keys) != fmt.Sprint(want) {
			t.Fatalf("List = %v, want %v", keys, want)
		}
		objs, err = s.List(ctx, p+"a/", p+"a/1", 0)
		if err != nil || len(objs) != 2 || objs[0].Key != p+"a/2" {
			t.Fatalf("List startAfter = %v, %v", objs, err)
		}
		objs, err = s.List(ctx, p+"a/", "", 1)
		if err != nil || len(objs) != 1 || objs[0].Key != p+"a/1" {
			t.Fatalf("List limit = %v, %v", objs, err)
		}
	})
}

func TestMem(t *testing.T) {
	runContract(t, func(t *testing.T) objectstore.Store {
		s := objectstore.OpenMem()
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 32, casWriters: 8, casIters: 50})
}

func TestDisk(t *testing.T) {
	runContract(t, func(t *testing.T) objectstore.Store {
		s, err := objectstore.OpenDisk(t.TempDir() + "/bucket")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 32, casWriters: 8, casIters: 50})
}
