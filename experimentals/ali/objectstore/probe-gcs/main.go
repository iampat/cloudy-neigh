package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/storage"
	"gocloud.dev/blob"
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/gcerrors"
	"gocloud.dev/gcp"
	"golang.org/x/oauth2"
)

var errNotGCS = errors.New("asFunc conversion failed")

func openBucket(ctx context.Context) (*blob.Bucket, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GCS_TOKEN")})
	client, err := gcp.NewHTTPClient(gcp.DefaultTransport(), ts)
	if err != nil {
		return nil, err
	}
	return gcsblob.OpenBucket(ctx, client, os.Getenv("GCS_BUCKET"), nil)
}

func putIfAbsent(ctx context.Context, b *blob.Bucket, key string, data []byte) error {
	return b.WriteAll(ctx, key, data, &blob.WriterOptions{IfNotExist: true})
}

func getWithGeneration(ctx context.Context, b *blob.Bucket, key string) ([]byte, int64, error) {
	r, err := b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	var sr *storage.Reader
	if !r.As(&sr) {
		return nil, 0, errNotGCS
	}
	gen := sr.Attrs.Generation
	data, err := io.ReadAll(r)
	return data, gen, err
}

func putIfGenerationMatch(ctx context.Context, b *blob.Bucket, key string, data []byte, gen int64) error {
	opts := &blob.WriterOptions{
		BeforeWrite: func(as func(any) bool) error {
			var objp **storage.ObjectHandle
			if !as(&objp) {
				return errNotGCS
			}
			*objp = (*objp).If(storage.Conditions{GenerationMatch: gen})
			return nil
		},
	}
	return b.WriteAll(ctx, key, data, opts)
}

func main() {
	ctx := context.Background()
	b, err := openBucket(ctx)
	if err != nil {
		panic(err)
	}
	defer b.Close()
	pass := true
	check := func(name string, ok bool, detail string) {
		status := "PASS"
		if !ok {
			status = "FAIL"
			pass = false
		}
		fmt.Printf("%s  %-40s %s\n", status, name, detail)
	}

	// 1. PutIfAbsent: fresh key wins, second attempt gets FailedPrecondition.
	_ = b.Delete(ctx, "k1")
	err = putIfAbsent(ctx, b, "k1", []byte("v1"))
	check("PutIfAbsent fresh key", err == nil, fmt.Sprint(err))
	err = putIfAbsent(ctx, b, "k1", []byte("v2"))
	check("PutIfAbsent existing key", gcerrors.Code(err) == gcerrors.FailedPrecondition,
		fmt.Sprintf("code=%v", gcerrors.Code(err)))

	// 2. Concurrent PutIfAbsent: exactly one winner.
	_ = b.Delete(ctx, "k2")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if putIfAbsent(ctx, b, "k2", []byte(fmt.Sprintf("w%d", i))) == nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	check("PutIfAbsent 8-way race", wins.Load() == 1, fmt.Sprintf("winners=%d", wins.Load()))

	// 3. GetWithGeneration, then generation changes on identical-bytes rewrite.
	_, g1, err := getWithGeneration(ctx, b, "k1")
	check("GetWithGeneration", err == nil && g1 > 0, fmt.Sprintf("gen=%d err=%v", g1, err))
	if err := b.WriteAll(ctx, "k1", []byte("v1"), nil); err != nil {
		panic(err)
	}
	_, g2, _ := getWithGeneration(ctx, b, "k1")
	check("generation changes, same bytes", g2 != g1, fmt.Sprintf("g1=%d g2=%d", g1, g2))

	// 4. Stale generation fails with FailedPrecondition.
	err = putIfGenerationMatch(ctx, b, "k1", []byte("stale"), g1)
	check("CAS with stale generation", gcerrors.Code(err) == gcerrors.FailedPrecondition,
		fmt.Sprintf("code=%v err=%v", gcerrors.Code(err), err))
	err = putIfGenerationMatch(ctx, b, "k1", []byte("fresh"), g2)
	check("CAS with live generation", err == nil, fmt.Sprint(err))

	// 5. CAS counter: 4 goroutines x 5 increments with retry -> exactly 20.
	_ = b.Delete(ctx, "ctr")
	if err := b.WriteAll(ctx, "ctr", []byte("0"), nil); err != nil {
		panic(err)
	}
	var retries atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				for {
					data, gen, err := getWithGeneration(ctx, b, "ctr")
					if err != nil {
						panic(err)
					}
					v, _ := strconv.Atoi(strings.TrimSpace(string(data)))
					err = putIfGenerationMatch(ctx, b, "ctr", []byte(strconv.Itoa(v+1)), gen)
					if err == nil {
						break
					}
					if gcerrors.Code(err) != gcerrors.FailedPrecondition {
						panic(err)
					}
					retries.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	data, _, err := getWithGeneration(ctx, b, "ctr")
	check("CAS counter 4x5", err == nil && string(data) == "20",
		fmt.Sprintf("final=%q retries=%d", data, retries.Load()))

	// 6. Missing key -> NotFound.
	_, _, err = getWithGeneration(ctx, b, "nope")
	check("missing key", gcerrors.Code(err) == gcerrors.NotFound, fmt.Sprintf("code=%v", gcerrors.Code(err)))

	if !pass {
		os.Exit(1)
	}
	fmt.Println("ALL PASS")
}
