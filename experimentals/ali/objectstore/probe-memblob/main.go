package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"gocloud.dev/blob"
	"gocloud.dev/blob/memblob"
	"gocloud.dev/gcerrors"
)

func main() {
	ctx := context.Background()
	b := memblob.OpenBucket(nil)
	defer b.Close()

	fmt.Println("=== 1. ETag across rewrites ===")
	payloads := [][]byte{[]byte("same-bytes"), []byte("same-bytes"), []byte("DIFFERENT")}
	var etags []string
	for i, p := range payloads {
		if err := b.WriteAll(ctx, "etag-key", p, nil); err != nil {
			fmt.Println("write err:", err)
			os.Exit(1)
		}
		attrs, err := b.Attributes(ctx, "etag-key")
		if err != nil {
			fmt.Println("attrs err:", err)
			os.Exit(1)
		}
		etags = append(etags, attrs.ETag)
		fmt.Printf("write %d: bytes=%q etag=%s modtime=%v\n", i+1, p, attrs.ETag, attrs.ModTime.UnixNano())
	}
	fmt.Printf("etag1==etag2 (identical bytes): %v; etag2==etag3: %v\n",
		etags[0] == etags[1], etags[1] == etags[2])

	fmt.Println("\n=== 2. Escape hatches (As / ErrorAs / BeforeWrite asFunc) ===")
	attrs, _ := b.Attributes(ctx, "etag-key")
	var fi os.FileInfo
	var anyIface interface{}
	fmt.Println("Attributes.As(*os.FileInfo):", attrs.As(&fi))
	fmt.Println("Attributes.As(*interface{}):", attrs.As(&anyIface))

	r, _ := b.NewReader(ctx, "etag-key", nil)
	var ior io.Reader
	fmt.Println("Reader.As(*io.Reader):", r.As(&ior))
	fmt.Println("Reader.As(*interface{}):", r.As(&anyIface))
	r.Close()

	_, err := b.NewReader(ctx, "no-such-key", nil)
	var perr *os.PathError
	fmt.Println("ErrorAs(*os.PathError):", b.ErrorAs(err, &perr))
	fmt.Println("ErrorAs(*interface{}):", b.ErrorAs(err, &anyIface))

	beforeWriteCalled := false
	asFuncReturnedTrue := false
	opts := &blob.WriterOptions{
		BeforeWrite: func(asFunc func(interface{}) bool) error {
			beforeWriteCalled = true
			probes := []interface{}{&anyIface, &fi, new(*os.File), new(**os.File), new(io.Writer)}
			for _, p := range probes {
				if asFunc(p) {
					asFuncReturnedTrue = true
					fmt.Printf("BeforeWrite asFunc converted %T\n", p)
				}
			}
			return nil
		},
	}
	if err := b.WriteAll(ctx, "bw-key", []byte("x"), opts); err != nil {
		fmt.Println("BeforeWrite write err:", err)
	}
	fmt.Printf("BeforeWrite called=%v, any asFunc conversion succeeded=%v\n",
		beforeWriteCalled, asFuncReturnedTrue)

	fmt.Println("\n=== 3. WriterOptions.IfNotExist ===")
	err = b.WriteAll(ctx, "etag-key", []byte("clobber?"), &blob.WriterOptions{IfNotExist: true})
	fmt.Printf("IfNotExist on existing key: err=%v\n", err)
	fmt.Printf("gcerrors.Code=%v (FailedPrecondition=%v)\n", gcerrors.Code(err), gcerrors.FailedPrecondition)

	var wins, losses, other atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := b.WriteAll(ctx, "race-fresh-key",
				[]byte(fmt.Sprintf("writer-%d", i)),
				&blob.WriterOptions{IfNotExist: true})
			switch {
			case err == nil:
				wins.Add(1)
			case gcerrors.Code(err) == gcerrors.FailedPrecondition:
				losses.Add(1)
			default:
				other.Add(1)
				fmt.Println("unexpected err:", err)
			}
		}(i)
	}
	wg.Wait()
	fmt.Printf("32 racing IfNotExist writers on fresh key: successes=%d failedPrecondition=%d other=%d\n",
		wins.Load(), losses.Load(), other.Load())

	fmt.Println("\n=== 4. 32 concurrent unconditional WriteAll to one key ===")
	var werrs atomic.Int64
	wg = sync.WaitGroup{}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte(fmt.Sprintf("W%02d|", i)), 1024)
			if err := b.WriteAll(ctx, "hot-key", payload, nil); err != nil {
				werrs.Add(1)
				fmt.Println("concurrent write err:", err)
			}
		}(i)
	}
	wg.Wait()
	got, err := b.ReadAll(ctx, "hot-key")
	if err != nil {
		fmt.Println("read-after err:", err)
	}
	torn := false
	if len(got) != 4*1024 {
		torn = true
	} else {
		first := string(got[:4])
		if !strings.HasPrefix(first, "W") || strings.Count(string(got), first) != 1024 {
			torn = true
		}
	}
	fmt.Printf("write errors=%d len=%d torn=%v firstChunk=%q\n", werrs.Load(), len(got), torn, got[:4])

	fmt.Println("\n=== 5. Missing key error codes ===")
	_, err = b.Attributes(ctx, "no-such-key")
	fmt.Printf("Attributes missing: code=%v err=%v\n", gcerrors.Code(err), err)
	_, err = b.NewReader(ctx, "no-such-key", nil)
	fmt.Printf("NewReader missing: code=%v err=%v\n", gcerrors.Code(err), err)
	err = b.Delete(ctx, "no-such-key")
	fmt.Printf("Delete missing: code=%v err=%v\n", gcerrors.Code(err), err)
	fmt.Printf("gcerrors.NotFound=%v\n", gcerrors.NotFound)
}
