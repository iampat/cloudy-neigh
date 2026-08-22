package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"gocloud.dev/blob"
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/gcp"
	"golang.org/x/oauth2"
)

type counter struct {
	base http.RoundTripper
	mu   sync.Mutex
	reqs []string
}

func (c *counter) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := c.base.RoundTrip(r)
	code := 0
	if resp != nil {
		code = resp.StatusCode
	}
	path := r.URL.Path
	if q := r.URL.Query().Get("alt"); q != "" {
		path += "?alt=" + q
	}
	c.mu.Lock()
	c.reqs = append(c.reqs, fmt.Sprintf("%s %s -> %d (%.0fms)", r.Method, path, code, float64(time.Since(start).Microseconds())/1000))
	c.mu.Unlock()
	return resp, err
}

func (c *counter) reset() {
	c.mu.Lock()
	c.reqs = nil
	c.mu.Unlock()
}

func (c *counter) report(op string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Printf("\n%s: %d HTTP request(s)\n", op, len(c.reqs))
	for _, r := range c.reqs {
		fmt.Println("    ", r)
	}
}

func main() {
	ctx := context.Background()
	c := &counter{base: gcp.DefaultTransport()}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GCS_TOKEN")})
	client, err := gcp.NewHTTPClient(c, ts)
	if err != nil {
		panic(err)
	}
	b, err := gcsblob.OpenBucket(ctx, client, os.Getenv("GCS_BUCKET"), nil)
	if err != nil {
		panic(err)
	}
	defer b.Close()
	payload := bytes.Repeat([]byte("x"), 1024)
	key := "rtt/" + strconv.FormatInt(time.Now().UnixNano(), 10)

	// Warm the connection so TLS setup is not counted in the first op.
	_, _ = b.Exists(ctx, "warmup")
	c.reset()

	// Put, exactly as objectstore.gcsStore.Put does it.
	var sw *storage.Writer
	w, err := b.NewWriter(ctx, key, &blob.WriterOptions{
		BeforeWrite: func(as func(any) bool) error {
			as(&sw)
			return nil
		},
	})
	if err != nil {
		panic(err)
	}
	io.Copy(w, bytes.NewReader(payload))
	if err := w.Close(); err != nil {
		panic(err)
	}
	gen := sw.Attrs().Generation
	c.report(fmt.Sprintf("Put (generation %d, read from the upload response)", gen))

	// Get.
	c.reset()
	r, err := b.NewReader(ctx, key, nil)
	if err != nil {
		panic(err)
	}
	io.Copy(io.Discard, r)
	r.Close()
	c.report("Get")

	// GetWithGeneration.
	c.reset()
	r, err = b.NewReader(ctx, key, nil)
	if err != nil {
		panic(err)
	}
	var sr *storage.Reader
	if !r.As(&sr) {
		panic("As failed")
	}
	g := sr.Attrs.Generation
	io.Copy(io.Discard, r)
	r.Close()
	c.report(fmt.Sprintf("GetWithGeneration (generation %d, from the same response)", g))

	// PutIfGenerationMatch.
	c.reset()
	w, err = b.NewWriter(ctx, key, &blob.WriterOptions{
		BeforeWrite: func(as func(any) bool) error {
			var objp **storage.ObjectHandle
			if !as(&objp) {
				return fmt.Errorf("no handle")
			}
			*objp = (*objp).If(storage.Conditions{GenerationMatch: g})
			as(&sw)
			return nil
		},
	})
	if err != nil {
		panic(err)
	}
	io.Copy(w, bytes.NewReader(payload))
	if err := w.Close(); err != nil {
		panic(err)
	}
	c.report("PutIfGenerationMatch")

	// PutIfAbsent on a fresh key.
	c.reset()
	if err := b.WriteAll(ctx, key+"/fresh", payload, &blob.WriterOptions{IfNotExist: true}); err != nil {
		panic(err)
	}
	c.report("PutIfAbsent (fresh key)")

	// PutIfAbsent losing.
	c.reset()
	err = b.WriteAll(ctx, key+"/fresh", payload, &blob.WriterOptions{IfNotExist: true})
	c.report(fmt.Sprintf("PutIfAbsent (existing key, err=%v)", err != nil))

	// A cold KVFS-style read chain: ref, then manifest, then blob.
	c.reset()
	chain := time.Now()
	for _, k := range []string{key, key, key} {
		rr, err := b.NewReader(ctx, k, nil)
		if err != nil {
			panic(err)
		}
		io.Copy(io.Discard, rr)
		rr.Close()
	}
	fmt.Printf("\nThree sequential reads (the cold KVFS chain): %.0fms total\n", float64(time.Since(chain).Milliseconds()))
	c.report("chain")

	fmt.Println("\n" + strings.Repeat("-", 60))
}
