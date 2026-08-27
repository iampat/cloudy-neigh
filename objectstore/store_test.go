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
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func put(t *testing.T, s *objectstore.Store, key, value string) string {
	t.Helper()
	gen, err := s.Put(context.Background(), key, strings.NewReader(value), nil)
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	return gen
}

func raceAbsentPut(t *testing.T, stores []*objectstore.Store, key string, writers int) {
	t.Helper()
	var wins, losses atomic.Int32
	errCh := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		s := stores[i%len(stores)]
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Put(context.Background(), key, strings.NewReader(strconv.Itoa(i)), &objectstore.Condition{Absent: true})
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
	if wins.Load() != 1 || losses.Load() != int32(writers-1) {
		t.Fatalf("wins=%d losses=%d, want 1 and %d", wins.Load(), losses.Load(), writers-1)
	}
}

func runContract(t *testing.T, open func(t *testing.T) *objectstore.Store, cfg contractConfig) {
	ctx := context.Background()
	prefix := func(t *testing.T, s *objectstore.Store) string {
		p := strings.ReplaceAll(t.Name(), "/", "_") + "/"
		objs, err := s.List(ctx, p, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			if err := s.Delete(ctx, o.Key); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	t.Run("RoundTrip", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		gen := put(t, s, k, "v1")
		if gen == "" {
			t.Fatal("Put returned an empty generation")
		}
		r, _, err := s.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got := read(t, r); got != "v1" {
			t.Fatalf("Get = %q, want %q", got, "v1")
		}
		r, gen2, err := s.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got := read(t, r); got != "v1" {
			t.Fatalf("Get = %q, want %q", got, "v1")
		}
		if gen2 != gen {
			t.Fatalf("generation = %q, want %q", gen2, gen)
		}
	})

	t.Run("GenerationChangesOnIdenticalWrite", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		g1 := put(t, s, k, "same")
		g2 := put(t, s, k, "same")
		if g1 == g2 {
			t.Fatalf("generation did not change: %q", g1)
		}
	})

	t.Run("AbsentCondition", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		gen, err := s.Put(ctx, k, strings.NewReader("v1"), &objectstore.Condition{Absent: true})
		if err != nil || gen == "" {
			t.Fatalf("Put(Absent) fresh = (%q, %v)", gen, err)
		}
		gen, err = s.Put(ctx, k, strings.NewReader("v2"), &objectstore.Condition{Absent: true})
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("Put(Absent) existing = %v, want ErrPreconditionFailed", err)
		}
		if gen != "" {
			t.Fatalf("failed Put(Absent) returned generation %q", gen)
		}
		r, _, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if got := read(t, r); got != "v1" {
			t.Fatalf("loser overwrote: %q", got)
		}
	})

	t.Run("AbsentConditionRace", func(t *testing.T) {
		s := open(t)
		raceAbsentPut(t, []*objectstore.Store{s}, prefix(t, s)+"k", cfg.raceWriters)
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
						r, gen, err := s.Get(ctx, k)
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
						_, err = s.Put(ctx, k, strings.NewReader(strconv.Itoa(v+1)), &objectstore.Condition{GenerationMatch: gen})
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
		k := prefix(t, s) + "k"
		g1 := put(t, s, k, "v1")
		put(t, s, k, "v2")
		gen, err := s.Put(ctx, k, strings.NewReader("v3"), &objectstore.Condition{GenerationMatch: g1})
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("stale CAS = %v, want ErrPreconditionFailed", err)
		}
		if gen != "" {
			t.Fatalf("failed CAS returned generation %q", gen)
		}
	})

	t.Run("DeleteRecreateInvalidatesOldGeneration", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		g1 := put(t, s, k, "v1")
		if err := s.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		put(t, s, k, "v1")
		if _, err := s.Put(ctx, k, strings.NewReader("v2"), &objectstore.Condition{GenerationMatch: g1}); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("old token after re-create = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("AbsentKey", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "nope"
		if _, _, err := s.Get(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
		if _, _, err := s.Get(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, k); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Delete = %v, want ErrNotFound", err)
		}
		gen := put(t, s, k, "v")
		if err := s.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, k, strings.NewReader("v"), &objectstore.Condition{GenerationMatch: gen}); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("CAS on deleted key = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("MalformedGeneration", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		put(t, s, k, "v1")
		_, err := s.Put(ctx, k, strings.NewReader("v2"), &objectstore.Condition{GenerationMatch: "not-a-token"})
		if err == nil || errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("malformed generation = %v, want a plain error", err)
		}
	})

	t.Run("InvalidCondition", func(t *testing.T) {
		for name, cond := range map[string]*objectstore.Condition{
			"bothSet": {Absent: true, GenerationMatch: "12345"},
		} {
			t.Run(name, func(t *testing.T) {
				s := open(t)
				k := prefix(t, s) + "k"
				put(t, s, k, "v1")
				_, err := s.Put(ctx, k, strings.NewReader("v2"), cond)
				if err == nil || errors.Is(err, objectstore.ErrPreconditionFailed) {
					t.Fatalf("Put with %s condition = %v, want a plain error", name, err)
				}
			})
		}
	})

	t.Run("NilConditionOverwrites", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		put(t, s, k, "v1")
		if _, err := s.Put(ctx, k, strings.NewReader("v1"), &objectstore.Condition{}); err != nil {
			t.Fatalf("zero-Condition Put = %v, want nil", err)
		}
		if _, err := s.Put(ctx, k, strings.NewReader("v2"), nil); err != nil {
			t.Fatalf("unconditional Put = %v, want nil", err)
		}
		r, _, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, _ := io.ReadAll(r)
		if string(got) != "v2" {
			t.Fatalf("value = %q, want %q", got, "v2")
		}
	})

	t.Run("CanceledContext", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Put(canceled, k, strings.NewReader("v"), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Put = %v, want context.Canceled", err)
		}
	})

	t.Run("List", func(t *testing.T) {
		s := open(t)
		p := prefix(t, s)
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
			if o.Generation == "" {
				t.Errorf("missing generation for %q", o.Key)
			}
		}
		want := []string{p + "a/1", p + "a/2", p + "a/3"}
		if fmt.Sprint(keys) != fmt.Sprint(want) {
			t.Fatalf("List = %v, want %v", keys, want)
		}
		r, gen, err := s.Get(ctx, objs[0].Key)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		if objs[0].Generation != gen {
			t.Fatalf("List generation %q != Get generation %q", objs[0].Generation, gen)
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

	t.Run("Exists", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "exists"
		ok, err := s.Exists(ctx, k)
		if err != nil {
			t.Fatalf("Exists before Put: %v", err)
		}
		if ok {
			t.Fatal("Exists before Put = true, want false")
		}
		put(t, s, k, "val")
		ok, err = s.Exists(ctx, k)
		if err != nil {
			t.Fatalf("Exists after Put: %v", err)
		}
		if !ok {
			t.Fatal("Exists after Put = false, want true")
		}
		if err := s.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		ok, err = s.Exists(ctx, k)
		if err != nil {
			t.Fatalf("Exists after Delete: %v", err)
		}
		if ok {
			t.Fatal("Exists after Delete = true, want false")
		}
	})

	t.Run("ExistsCanceledContext", func(t *testing.T) {
		s := open(t)
		k := prefix(t, s) + "k"
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Exists(canceled, k); !errors.Is(err, context.Canceled) {
			t.Fatalf("Exists = %v, want context.Canceled", err)
		}
	})
}

func openURL(tb testing.TB, url string) *objectstore.Store {
	tb.Helper()
	s, err := objectstore.Open(context.Background(), url)
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

func TestMem(t *testing.T) {
	runContract(t, func(t *testing.T) *objectstore.Store {
		s := openURL(t, "mem://")
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 32, casWriters: 8, casIters: 50})
}

func TestDisk(t *testing.T) {
	runContract(t, func(t *testing.T) *objectstore.Store {
		s := openURL(t, "file://"+t.TempDir()+"/bucket?create_dir=true")
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 32, casWriters: 8, casIters: 50})
}
