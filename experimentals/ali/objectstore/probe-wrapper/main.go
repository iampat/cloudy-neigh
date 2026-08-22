package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gocloud.dev/blob/fileblob"
	"gocloud.dev/blob/memblob"

	"probe.local/objectstore/probe-wrapper/condstore"
)

func main() {
	switch os.Getenv("PROBE_CHILD") {
	case "absent":
		childAbsent()
		return
	case "cas":
		childCAS()
		return
	}
	parent()
}

func openFileStore(dir string) (*condstore.Store, func(), error) {
	b, err := fileblob.OpenBucket(dir, &fileblob.Options{CreateDir: true})
	if err != nil {
		return nil, nil, err
	}
	lk, err := condstore.NewFlockLocker(dir + ".lock")
	if err != nil {
		b.Close()
		return nil, nil, err
	}
	return condstore.New(b, lk), func() { lk.Close(); b.Close() }, nil
}

func parent() {
	ctx := context.Background()

	memBucket := memblob.OpenBucket(nil)
	defer memBucket.Close()
	memStore := condstore.New(memBucket, &condstore.MutexLocker{})

	base, err := filepath.Abs("probe-wrapper/tmp")
	must(err)
	must(os.RemoveAll(base))
	must(os.MkdirAll(base, 0o777))

	fileDir := filepath.Join(base, "inproc-bucket")
	fileStore, closeFile, err := openFileStore(fileDir)
	must(err)
	defer closeFile()

	for _, d := range []struct {
		name string
		st   *condstore.Store
	}{{"memblob", memStore}, {"fileblob", fileStore}} {
		fmt.Printf("=== driver %s ===\n", d.name)
		testA(ctx, d.st)
		testB(ctx, d.st)
		testC(ctx, d.st)
	}

	testD(ctx, base)
}

func testA(ctx context.Context, st *condstore.Store) {
	const workers = 32
	var wins, precond, other int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.PutIfAbsent(ctx, "a-key", strings.NewReader(fmt.Sprintf("writer-%d", i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, condstore.ErrPreconditionFailed):
				precond++
			default:
				other++
				fmt.Printf("  a: unexpected error: %v\n", err)
			}
		}(i)
	}
	wg.Wait()
	fmt.Printf("a: PutIfAbsent x%d -> %d success, %d ErrPreconditionFailed, %d other\n",
		workers, wins, precond, other)
}

func casIncrement(ctx context.Context, st *condstore.Store, key string, iters int) (retries int, err error) {
	for i := 0; i < iters; i++ {
		for {
			r, gen, err := st.GetWithGeneration(ctx, key)
			if err != nil {
				return retries, err
			}
			buf, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				return retries, err
			}
			n, err := strconv.Atoi(string(buf))
			if err != nil {
				return retries, err
			}
			_, err = st.PutIfGenerationMatch(ctx, key, strings.NewReader(strconv.Itoa(n+1)), gen)
			if err == nil {
				break
			}
			if errors.Is(err, condstore.ErrPreconditionFailed) {
				retries++
				continue
			}
			return retries, err
		}
	}
	return retries, nil
}

func readInt(ctx context.Context, st *condstore.Store, key string) (int, int64, error) {
	r, gen, err := st.GetWithGeneration(ctx, key)
	if err != nil {
		return 0, 0, err
	}
	defer r.Close()
	buf, err := io.ReadAll(r)
	if err != nil {
		return 0, 0, err
	}
	n, err := strconv.Atoi(string(buf))
	return n, gen, err
}

func testB(ctx context.Context, st *condstore.Store) {
	const workers, iters = 8, 50
	_, err := st.Put(ctx, "b-key", strings.NewReader("0"))
	must(err)
	var wg sync.WaitGroup
	var totalRetries int64
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := casIncrement(ctx, st, "b-key", iters)
			must(err)
			mu.Lock()
			totalRetries += int64(r)
			mu.Unlock()
		}()
	}
	wg.Wait()
	n, gen, err := readInt(ctx, st, "b-key")
	must(err)
	fmt.Printf("b: %d goroutines x %d CAS increments -> final value %d (want %d), generation %d, %d precondition retries\n",
		workers, iters, n, workers*iters, gen, totalRetries)
}

func testC(ctx context.Context, st *condstore.Store) {
	_, err := st.Put(ctx, "c-key", strings.NewReader("v1"))
	must(err)
	r, gen, err := st.GetWithGeneration(ctx, "c-key")
	must(err)
	r.Close()
	newGen, err := st.Put(ctx, "c-key", strings.NewReader("v2"))
	must(err)
	_, err = st.PutIfGenerationMatch(ctx, "c-key", strings.NewReader("v3"), gen)
	fmt.Printf("c: got gen %d, unconditional Put -> gen %d, PutIfGenerationMatch(old gen) -> %v (want ErrPreconditionFailed)\n",
		gen, newGen, err)
}

func testD(ctx context.Context, base string) {
	fmt.Println("=== fileblob cross-process ===")
	exe, err := os.Executable()
	must(err)

	dir := filepath.Join(base, "xproc-bucket")

	runChildren := func(mode string, n int) []string {
		outs := make([]string, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				cmd := exec.Command(exe)
				cmd.Env = append(os.Environ(),
					"PROBE_CHILD="+mode,
					"PROBE_DIR="+dir,
					"PROBE_ID="+strconv.Itoa(i))
				out, err := cmd.CombinedOutput()
				if err != nil {
					outs[i] = fmt.Sprintf("EXITERR %v: %s", err, out)
					return
				}
				outs[i] = strings.TrimSpace(string(out))
			}(i)
		}
		wg.Wait()
		return outs
	}

	outs := runChildren("absent", 4)
	wins, precond := 0, 0
	for _, o := range outs {
		switch o {
		case "WIN":
			wins++
		case "PRECOND":
			precond++
		default:
			fmt.Printf("  d1 child output: %q\n", o)
		}
	}
	fmt.Printf("d1: 4 processes PutIfAbsent -> %d success, %d ErrPreconditionFailed\n", wins, precond)

	st, closeSt, err := openFileStore(dir)
	must(err)
	_, err = st.Put(ctx, "counter", strings.NewReader("0"))
	must(err)

	start := time.Now()
	outs = runChildren("cas", 4)
	elapsed := time.Since(start)
	for _, o := range outs {
		if !strings.HasPrefix(o, "DONE") {
			fmt.Printf("  d2 child output: %q\n", o)
		}
	}
	n, gen, err := readInt(ctx, st, "counter")
	must(err)
	closeSt()
	fmt.Printf("d2: 4 processes x 25 CAS increments -> final value %d (want 100), generation %d, wall time %v\n",
		n, gen, elapsed)
	fmt.Printf("d2 child reports: %v\n", outs)
}

func childAbsent() {
	ctx := context.Background()
	st, closeSt, err := openFileStore(os.Getenv("PROBE_DIR"))
	must(err)
	defer closeSt()
	_, err = st.PutIfAbsent(ctx, "xkey", strings.NewReader("pid-"+strconv.Itoa(os.Getpid())))
	switch {
	case err == nil:
		fmt.Println("WIN")
	case errors.Is(err, condstore.ErrPreconditionFailed):
		fmt.Println("PRECOND")
	default:
		fmt.Println("ERR:", err)
	}
}

func childCAS() {
	ctx := context.Background()
	st, closeSt, err := openFileStore(os.Getenv("PROBE_DIR"))
	must(err)
	defer closeSt()
	retries, err := casIncrement(ctx, st, "counter", 25)
	if err != nil {
		fmt.Println("ERR:", err)
		os.Exit(1)
	}
	fmt.Printf("DONE retries=%d\n", retries)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
