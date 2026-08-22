package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
	"golang.org/x/oauth2"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	idx := int(p / 100 * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func summarize(label string, lat []time.Duration, wall time.Duration) {
	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total time.Duration
	for _, d := range lat {
		total += d
	}
	mean := total / time.Duration(len(lat))
	fmt.Printf("\n%s\n", label)
	fmt.Printf("  samples %d over %.1fs (%.0f reads/sec)\n", len(lat), wall.Seconds(), float64(len(lat))/wall.Seconds())
	fmt.Printf("  min %6.1fms   mean %6.1fms   max %7.1fms\n", ms(sorted[0]), ms(mean), ms(sorted[len(sorted)-1]))
	fmt.Printf("  p50 %6.1fms   p90 %6.1fms   p95 %6.1fms   p99 %6.1fms   p99.9 %6.1fms\n",
		ms(percentile(sorted, 50)), ms(percentile(sorted, 90)), ms(percentile(sorted, 95)),
		ms(percentile(sorted, 99)), ms(percentile(sorted, 99.9)))

	lo, hi := sorted[0], percentile(sorted, 99)
	const buckets = 12
	width := (hi - lo) / buckets
	if width <= 0 {
		return
	}
	counts := make([]int, buckets+1)
	for _, d := range sorted {
		b := int((d - lo) / width)
		if b > buckets {
			b = buckets
		}
		counts[b]++
	}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	fmt.Println("  distribution:")
	for i, c := range counts {
		edge := lo + time.Duration(i)*width
		bar := string(bytes.Repeat([]byte("#"), c*40/max))
		label := fmt.Sprintf("%6.1f", ms(edge))
		if i == buckets {
			label = " >p99"
		}
		fmt.Printf("    %sms %-40s %d\n", label, bar, c)
	}
}

func main() {
	ctx := context.Background()
	bucket := os.Getenv("GCS_BUCKET")
	if bucket == "" {
		panic("GCS_BUCKET is required")
	}
	keys := atoi(env("KEYS", "3000"))
	writers := atoi(env("WRITERS", "64"))
	readers := atoi(env("READERS", "16"))
	payload := bytes.Repeat([]byte("x"), atoi(env("SIZE", "1024")))

	var ts oauth2.TokenSource
	if tok := os.Getenv("GCS_TOKEN"); tok != "" {
		ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})
	}
	s, err := objectstore.OpenGCS(ctx, bucket, ts)
	if err != nil {
		panic(err)
	}
	defer s.Close()

	// Content-addressed, two-byte sharded keys, matching the layout in
	// storage.md so the prefix spread matches production.
	run := make([]byte, 8)
	rand.Read(run)
	names := make([]string, keys)
	for i := range names {
		h := sha256.Sum256([]byte(fmt.Sprintf("%x/%d", run, i)))
		hs := hex.EncodeToString(h[:])
		names[i] = fmt.Sprintf("objects/%s/%s/%s", hs[0:2], hs[2:4], hs)
	}

	fmt.Printf("populating %d keys of %d bytes with %d writers\n", keys, len(payload), writers)
	start := time.Now()
	var wg sync.WaitGroup
	work := make(chan string, keys)
	for _, n := range names {
		work <- n
	}
	close(work)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range work {
				if _, err := s.Put(ctx, n, bytes.NewReader(payload)); err != nil {
					fmt.Fprintln(os.Stderr, "put:", err)
					os.Exit(1)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("populated in %.1fs\n", time.Since(start).Seconds())

	shuffle := func() []string {
		out := append([]string(nil), names...)
		for i := len(out) - 1; i > 0; i-- {
			j, _ := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
			out[i], out[j.Int64()] = out[j.Int64()], out[i]
		}
		return out
	}

	readOne := func(key string) time.Duration {
		t := time.Now()
		r, err := s.Get(ctx, key)
		if err != nil {
			fmt.Fprintln(os.Stderr, "get:", err)
			os.Exit(1)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		r.Close()
		return time.Since(t)
	}

	// Warm the connection pool. The first request pays TLS setup.
	for _, n := range names[:20] {
		readOne(n)
	}

	// One key read repeatedly. A per-operation benchmark measures this,
	// and it is not what a content-addressed read costs.
	if hot := atoi(env("HOT", "500")); hot > 0 {
		hotLat := make([]time.Duration, hot)
		start = time.Now()
		for i := range hotLat {
			hotLat[i] = readOne(names[0])
		}
		summarize(fmt.Sprintf("Get, one key read %d times, hot object", hot), hotLat, time.Since(start))
	}
	if os.Getenv("HOT_ONLY") != "" {
		return
	}

	// One request at a time: the latency of a direct key read with no
	// contention.
	order := shuffle()
	lat := make([]time.Duration, 0, len(order))
	start = time.Now()
	for _, n := range order {
		lat = append(lat, readOne(n))
	}
	summarize(fmt.Sprintf("Get, sequential, 1 outstanding request, %d keys", len(lat)), lat, time.Since(start))

	// The same reads with concurrency, which is how a real client drives it.
	order = shuffle()
	conc := make([]time.Duration, len(order))
	start = time.Now()
	idx := make(chan int, len(order))
	for i := range order {
		idx <- i
	}
	close(idx)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				conc[i] = readOne(order[i])
			}
		}()
	}
	wg.Wait()
	summarize(fmt.Sprintf("Get, %d concurrent readers", readers), conc, time.Since(start))
}
