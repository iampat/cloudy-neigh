package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
	"gocloud.dev/gcerrors"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func walk(dir string) []string {
	var paths []string
	must(filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			paths = append(paths, rel)
		}
		return nil
	}))
	return paths
}

func main() {
	ctx := context.Background()
	root := "probe-fileblob/tmp/bucket"
	must(os.RemoveAll(root))
	must(os.MkdirAll(root, 0o755))
	absRoot, err := filepath.Abs(root)
	must(err)

	b, err := fileblob.OpenBucket(absRoot, &fileblob.Options{CreateDir: true})
	must(err)
	defer b.Close()

	// ---- 1. Layout + metadata round-trip ----
	fmt.Println("=== 1. layout & metadata ===")
	for _, key := range []string{"a/b", "c"} {
		w, err := b.NewWriter(ctx, key, &blob.WriterOptions{Metadata: map[string]string{"generation": "7"}})
		must(err)
		_, err = w.Write([]byte("hello " + key))
		must(err)
		must(w.Close())
	}
	for _, p := range walk(absRoot) {
		fmt.Println("file:", p)
	}
	for _, key := range []string{"a/b", "c"} {
		attrs, err := b.Attributes(ctx, key)
		must(err)
		fmt.Printf("key=%q Metadata=%v ETag=%s ModTime=%s (unixnano=%d) Size=%d ContentType=%s\n",
			key, attrs.Metadata, attrs.ETag, attrs.ModTime.Format("2006-01-02T15:04:05.000000000Z07:00"), attrs.ModTime.UnixNano(), attrs.Size, attrs.ContentType)
	}

	// ---- 2. ETag semantics on identical rewrites ----
	fmt.Println("\n=== 2. ETag on identical rewrites ===")
	payload := []byte("same bytes every time")
	for i := 0; i < 3; i++ {
		must(b.WriteAll(ctx, "etag-key", payload, nil))
		attrs, err := b.Attributes(ctx, "etag-key")
		must(err)
		fmt.Printf("write %d: ETag=%s ModTime.UnixNano=%d Size=%d\n", i+1, attrs.ETag, attrs.ModTime.UnixNano(), attrs.Size)
	}

	// ---- 3. IfNotExist race ----
	fmt.Println("\n=== 3. IfNotExist with 32 goroutines ===")
	var succ, fail int64
	codeCounts := make(map[gcerrors.ErrorCode]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			w, err := b.NewWriter(ctx, "ifnotexist-key", &blob.WriterOptions{IfNotExist: true})
			if err == nil {
				_, err = w.Write([]byte(fmt.Sprintf("writer-%d", i)))
				if cerr := w.Close(); err == nil {
					err = cerr
				}
			}
			if err == nil {
				atomic.AddInt64(&succ, 1)
			} else {
				atomic.AddInt64(&fail, 1)
				mu.Lock()
				codeCounts[gcerrors.Code(err)]++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	fmt.Printf("successes=%d failures=%d codes=%v (FailedPrecondition=%d)\n", succ, fail, codeCounts, gcerrors.FailedPrecondition)

	// ---- 4. Torn writes: 32 concurrent writers, different payloads, same key ----
	fmt.Println("\n=== 4. concurrent different-payload writes to one key ===")
	const N = 32
	const sz = 1 << 20
	wg = sync.WaitGroup{}
	start = make(chan struct{})
	var werrs int64
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			data := bytes.Repeat([]byte{byte('A' + i%26)}, sz)
			err := b.WriteAll(ctx, "torn-key", data, &blob.WriterOptions{Metadata: map[string]string{"writer": fmt.Sprintf("%c", 'A'+i%26), "idx": fmt.Sprintf("%d", i)}})
			if err != nil {
				atomic.AddInt64(&werrs, 1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	fmt.Printf("write errors: %d\n", werrs)
	data, err := b.ReadAll(ctx, "torn-key")
	must(err)
	uniform := true
	for _, c := range data {
		if c != data[0] {
			uniform = false
			break
		}
	}
	attrs, err := b.Attributes(ctx, "torn-key")
	must(err)
	fmt.Printf("data: len=%d firstByte=%c uniform=%v\n", len(data), data[0], uniform)
	fmt.Printf("metadata: %v  -> data/metadata same writer: %v\n", attrs.Metadata, uniform && attrs.Metadata["writer"] == string(data[0]))
	var temps []string
	for _, p := range walk(absRoot) {
		if strings.Contains(p, ".tmp") {
			temps = append(temps, p)
		}
	}
	fmt.Printf("leftover temp files: %d %v\n", len(temps), temps)

	// ---- 5. direct O_EXCL create in bucket dir, no sidecar ----
	fmt.Println("\n=== 5. direct O_EXCL file, no sidecar ===")
	f, err := os.OpenFile(filepath.Join(absRoot, "direct-key"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	must(err)
	_, err = f.Write([]byte("written directly"))
	must(err)
	must(f.Close())
	got, err := b.ReadAll(ctx, "direct-key")
	fmt.Printf("ReadAll: err=%v data=%q\n", err, got)
	attrs2, err := b.Attributes(ctx, "direct-key")
	if err != nil {
		fmt.Printf("Attributes: err=%v\n", err)
	} else {
		fmt.Printf("Attributes: ETag=%s ContentType=%s Metadata=%v Size=%d\n", attrs2.ETag, attrs2.ContentType, attrs2.Metadata, attrs2.Size)
	}
	// and can fileblob list it?
	it := b.List(&blob.ListOptions{})
	fmt.Print("list keys:")
	for {
		obj, err := it.Next(ctx)
		if err != nil {
			break
		}
		fmt.Printf(" %q", obj.Key)
	}
	fmt.Println()

	stress(ctx, b, absRoot)
}

func stress(ctx context.Context, b *blob.Bucket, absRoot string) {
	// ---- 6. IfNotExist stress: 200 rounds x 32 goroutines ----
	fmt.Println("\n=== 6. IfNotExist stress (200 rounds x 32) ===")
	dist := make(map[int64]int)
	otherCodes := make(map[gcerrors.ErrorCode]int)
	for round := 0; round < 200; round++ {
		key := fmt.Sprintf("stress-%d", round)
		var succ int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				err := b.WriteAll(ctx, key, []byte(fmt.Sprintf("w%d", i)), &blob.WriterOptions{IfNotExist: true})
				if err == nil {
					atomic.AddInt64(&succ, 1)
				} else if c := gcerrors.Code(err); c != gcerrors.FailedPrecondition {
					otherCodes[c]++
				}
			}(i)
		}
		close(start)
		wg.Wait()
		dist[succ]++
	}
	fmt.Printf("success-count distribution: %v  non-FailedPrecondition codes: %v\n", dist, otherCodes)

	// ---- 7. sidecar clobber on FAILED IfNotExist ----
	fmt.Println("\n=== 7. sidecar clobber on failed IfNotExist ===")
	must(b.WriteAll(ctx, "clobber-key", []byte("original data"), &blob.WriterOptions{Metadata: map[string]string{"owner": "first"}}))
	err := b.WriteAll(ctx, "clobber-key", []byte("loser data"), &blob.WriterOptions{IfNotExist: true, Metadata: map[string]string{"owner": "second"}})
	fmt.Printf("second write err=%v code=%v\n", err, gcerrors.Code(err))
	data, err := b.ReadAll(ctx, "clobber-key")
	must(err)
	attrs, err := b.Attributes(ctx, "clobber-key")
	must(err)
	fmt.Printf("data=%q metadata=%v  (metadata clobbered by loser: %v)\n", data, attrs.Metadata, attrs.Metadata["owner"] == "second")

	// ---- 8. torn-write stress: 30 rounds ----
	fmt.Println("\n=== 8. torn-write stress (30 rounds x 32 writers, 256KB) ===")
	mismatch, nonUniform := 0, 0
	for round := 0; round < 30; round++ {
		key := fmt.Sprintf("torn-%d", round)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				data := bytes.Repeat([]byte{byte('A' + i%26)}, 256<<10)
				_ = b.WriteAll(ctx, key, data, &blob.WriterOptions{Metadata: map[string]string{"writer": fmt.Sprintf("%c", 'A'+i%26)}})
			}(i)
		}
		close(start)
		wg.Wait()
		data, err := b.ReadAll(ctx, key)
		must(err)
		uniform := true
		for _, c := range data {
			if c != data[0] {
				uniform = false
				break
			}
		}
		if !uniform {
			nonUniform++
			continue
		}
		attrs, err := b.Attributes(ctx, key)
		must(err)
		if attrs.Metadata["writer"] != string(data[0]) {
			mismatch++
			fmt.Printf("round %d: data all %c but metadata writer=%s\n", round, data[0], attrs.Metadata["writer"])
		}
	}
	fmt.Printf("rounds=30 nonUniformData=%d dataMetadataMismatch=%d\n", nonUniform, mismatch)
}
