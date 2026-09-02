package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"log/slog"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
)

const headerSize = 20

type event struct {
	Type      string  `json:"type"`
	Host      string  `json:"host,omitempty"`
	URL       string  `json:"url,omitempty"`
	MinSize   int     `json:"min_size,omitempty"`
	MaxSize   int     `json:"max_size,omitempty"`
	Writers   int     `json:"writers,omitempty"`
	Stream    string  `json:"stream,omitempty"`
	Seconds   float64 `json:"seconds,omitempty"`
	DrainSecs float64 `json:"drain_secs,omitempty"`
	Appends   int     `json:"appends,omitempty"`
	Fails     int     `json:"fails,omitempty"`
	Writer    uint32  `json:"w,omitempty"`
	Record    uint64  `json:"r,omitempty"`
	Seq       uint64  `json:"s,omitempty"`
	Size      int     `json:"n,omitempty"`
	Micros    int64   `json:"u,omitempty"`
	StartAt   int64   `json:"at,omitempty"`
	Drain     bool    `json:"drain_append,omitempty"`
	Err       string  `json:"err,omitempty"`
}

type appendRecord struct {
	writer  uint32
	record  uint64
	seq     uint64
	size    int
	micros  int64
	startAt int64
	drain   bool
}

type failureRecord struct {
	writer uint32
	record uint64
	micros int64
	err    string
}

type runResult struct {
	writers  int
	stream   string
	seconds  float64
	drain    float64
	appends  []appendRecord
	failures []failureRecord
}

type config struct {
	url      string
	host     string
	writers  string
	duration time.Duration
	minSize  int
	maxSize  int
	out      string
	sanity   string
	readers  int
	prefix   string
	debugLog string
}

func main() {
	var c config
	flag.StringVar(&c.url, "url", "", "object store URL, for example gs://bucket")
	flag.StringVar(&c.host, "host", "", "label for this machine, recorded in the log")
	flag.StringVar(&c.writers, "writers", "1,5,10,20,50,100", "comma separated writer counts")
	flag.DurationVar(&c.duration, "duration", time.Minute, "run time for each writer count")
	flag.IntVar(&c.minSize, "min", 1024, "smallest payload in bytes")
	flag.IntVar(&c.maxSize, "max", 10240, "largest payload in bytes")
	flag.StringVar(&c.out, "out", "", "path for the result JSONL")
	flag.StringVar(&c.sanity, "sanity", "", "read a result JSONL and verify the stream of each run instead of benchmarking")
	flag.IntVar(&c.readers, "readers", 64, "parallel segment readers for the sanity check")
	flag.StringVar(&c.prefix, "prefix", "", "stream name prefix, so a rerun does not collide")
	flag.StringVar(&c.debugLog, "debuglog", "", "path for the logstream debug records, which count the uploads and the probes")
	flag.Parse()

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "walbench:", err)
		os.Exit(1)
	}
}

func run(c config) error {
	if c.sanity != "" {
		return runSanity(c.sanity, c.readers)
	}
	if c.url == "" {
		return errors.New("-url is required")
	}
	if c.out == "" {
		return errors.New("-out is required")
	}
	if c.minSize <= headerSize || c.maxSize < c.minSize {
		return fmt.Errorf("bad payload range %d..%d", c.minSize, c.maxSize)
	}
	counts, err := parseCounts(c.writers)
	if err != nil {
		return err
	}
	if c.host == "" {
		c.host, _ = os.Hostname()
	}
	if c.debugLog != "" {
		df, err := os.Create(c.debugLog)
		if err != nil {
			return err
		}
		defer df.Close()
		dbw := bufio.NewWriter(df)
		defer dbw.Flush()
		slog.SetDefault(slog.New(slog.NewJSONHandler(dbw, &slog.HandlerOptions{Level: slog.LevelDebug})))
		fmt.Println("debug log:", c.debugLog)
	}

	ctx := context.Background()
	store, err := objectstore.Open(ctx, c.url)
	if err != nil {
		return fmt.Errorf("open %s: %w", c.url, err)
	}
	defer store.Close()

	f, err := os.Create(c.out)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	defer bw.Flush()
	enc := json.NewEncoder(bw)

	if err := enc.Encode(event{
		Type: "meta", Host: c.host, URL: c.url,
		MinSize: c.minSize, MaxSize: c.maxSize,
	}); err != nil {
		return err
	}

	for _, n := range counts {
		name := streamName(c.prefix, n)
		probe, err := logstream.New(store, name, nil)
		if err != nil {
			return err
		}
		tail, err := probe.Tail(ctx)
		if err != nil {
			return fmt.Errorf("tail %s: %w", name, err)
		}
		if tail != 0 {
			return fmt.Errorf("stream %s already holds %d segments, pick another -prefix", name, tail)
		}
		fmt.Printf("run n=%d stream=%s for %s\n", n, name, c.duration)
		r, err := benchmark(ctx, c.url, name, n, c.duration, c.minSize, c.maxSize)
		if err != nil {
			return err
		}
		if err := writeRun(enc, c.host, r); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		report(r)
	}

	fmt.Println("results:", c.out)
	return nil
}

func writeRun(enc *json.Encoder, host string, r runResult) error {
	err := enc.Encode(event{
		Type: "run", Host: host, Writers: r.writers, Stream: r.stream,
		Seconds: r.seconds, DrainSecs: r.drain,
		Appends: len(r.appends), Fails: len(r.failures),
	})
	if err != nil {
		return err
	}
	for _, a := range r.appends {
		err := enc.Encode(event{
			Type: "append", Host: host, Writers: r.writers,
			Writer: a.writer, Record: a.record, Seq: a.seq,
			Size: a.size, Micros: a.micros,
			StartAt: a.startAt, Drain: a.drain,
		})
		if err != nil {
			return err
		}
	}
	for _, f := range r.failures {
		err := enc.Encode(event{
			Type: "fail", Host: host, Writers: r.writers,
			Writer: f.writer, Record: f.record,
			Micros: f.micros, Err: f.err,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func parseCounts(s string) ([]int, error) {
	var counts []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("bad writer count %q: %w", part, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("writer count %d must be positive", n)
		}
		counts = append(counts, n)
	}
	if len(counts) == 0 {
		return nil, errors.New("no writer counts")
	}
	return counts, nil
}

// Every writer in a run shares one stream. That contention is what this
// benchmark measures, so the stream count is not a knob.
func streamName(prefix string, writers int) string {
	return fmt.Sprintf("%sbench-n%d", prefix, writers)
}

// One record leaves the producer with its header already stamped, except for
// the writer field. The writer that claims it stamps that field itself.
type item struct {
	id  uint64
	buf []byte
}

func benchmark(ctx context.Context, url, name string, n int, d time.Duration, minSize, maxSize int) (runResult, error) {
	stores := make([]objectstore.Store, n)
	for i := range stores {
		s, err := objectstore.Open(ctx, url)
		if err != nil {
			for _, prev := range stores[:i] {
				prev.Close()
			}
			return runResult{}, fmt.Errorf("open store %d: %w", i, err)
		}
		stores[i] = s
	}
	defer func() {
		for _, s := range stores {
			s.Close()
		}
	}()

	type outcome struct {
		appends  []appendRecord
		failures []failureRecord
	}
	outcomes := make([]outcome, n)

	var stop atomic.Bool
	work := make(chan item, 1024)
	start := time.Now()

	go func() {
		defer close(work)
		rng := rand.New(rand.NewPCG(uint64(n), 1))
		var id uint64
		for !stop.Load() {
			size := minSize + rng.IntN(maxSize-minSize+1)
			buf := make([]byte, size)
			fill(buf, id, rng)
			select {
			case work <- item{id: id, buf: buf}:
				id++
			case <-ctx.Done():
				return
			}
		}
	}()

	g, gctx := errgroup.WithContext(ctx)
	for i := range n {
		id := i
		g.Go(func() error {
			log, err := logstream.New(stores[id], name, nil)
			if err != nil {
				return err
			}
			for it := range work {
				if stop.Load() {
					return nil
				}
				binary.BigEndian.PutUint32(it.buf[16:20], uint32(id))

				t0 := time.Now()
				seq, err := log.Append(gctx, []logstream.Record{it.buf})
				elapsed := time.Since(t0)
				startAt := t0.Sub(start).Microseconds()
				if err != nil {
					outcomes[id].failures = append(outcomes[id].failures, failureRecord{
						writer: uint32(id),
						record: it.id,
						micros: elapsed.Microseconds(),
						err:    err.Error(),
					})
					continue
				}
				outcomes[id].appends = append(outcomes[id].appends, appendRecord{
					writer:  uint32(id),
					record:  it.id,
					seq:     seq,
					size:    len(it.buf),
					micros:  elapsed.Microseconds(),
					startAt: startAt,
					drain:   startAt+elapsed.Microseconds() > d.Microseconds(),
				})
			}
			return nil
		})
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	stop.Store(true)
	stopped := time.Now()

	// The producer may already be parked on the send. Drain one item so it
	// observes the stop flag and closes the channel.
	select {
	case <-work:
	default:
	}

	done := make(chan error, 1)
	go func() { done <- g.Wait() }()
	var gerr error
	for waiting := true; waiting; {
		select {
		case gerr = <-done:
			waiting = false
		case <-time.After(15 * time.Second):
			fmt.Printf("       draining %.0fs, an append is still in flight\n", time.Since(stopped).Seconds())
		}
	}
	if gerr != nil {
		return runResult{}, gerr
	}

	r := runResult{
		writers: n, stream: name,
		seconds: time.Since(start).Seconds(),
		drain:   time.Since(stopped).Seconds(),
	}
	for _, o := range outcomes {
		r.appends = append(r.appends, o.appends...)
		r.failures = append(r.failures, o.failures...)
	}
	return r, nil
}

func fill(buf []byte, id uint64, rng *rand.Rand) {
	body := buf[headerSize:]
	i := 0
	for ; i+8 <= len(body); i += 8 {
		binary.LittleEndian.PutUint64(body[i:], rng.Uint64())
	}
	for tail := rng.Uint64(); i < len(body); i++ {
		body[i] = byte(tail)
		tail >>= 8
	}
	binary.BigEndian.PutUint64(buf[0:8], id)
	binary.BigEndian.PutUint32(buf[8:12], uint32(len(buf)))
	binary.BigEndian.PutUint32(buf[12:16], crc32.ChecksumIEEE(body))
	binary.BigEndian.PutUint32(buf[16:20], 0)
}

func report(r runResult) {
	if len(r.appends) == 0 {
		fmt.Printf("  n=%-3d no successful append, %d failures\n", r.writers, len(r.failures))
		return
	}
	var steady []int64
	var bytesTotal int64
	var maxSeq uint64
	drained := 0
	perWriter := make([]int, r.writers)
	latByWriter := make(map[uint32][]int64)
	for _, a := range r.appends {
		if a.seq > maxSeq {
			maxSeq = a.seq
		}
		if a.drain {
			drained++
			continue
		}
		steady = append(steady, a.micros)
		bytesTotal += int64(a.size)
		perWriter[a.writer]++
		latByWriter[a.writer] = append(latByWriter[a.writer], a.micros)
	}
	if len(steady) == 0 {
		fmt.Printf("  n=%-3d every append landed in the drain window\n", r.writers)
		return
	}
	sort.Slice(steady, func(i, j int) bool { return steady[i] < steady[j] })

	window := r.seconds - r.drain
	attempts := len(r.appends) + len(r.failures)
	fmt.Printf("  n=%-3d appends=%-7d %.1f/s  %.2f MiB/s  maxseq=%d\n",
		r.writers, len(steady), float64(len(steady))/window,
		float64(bytesTotal)/window/(1<<20), maxSeq)
	fmt.Printf("       window %.1fs + drain %.1fs (%d appends dropped)  fails=%d  error rate %.3f%%\n",
		window, r.drain, drained, len(r.failures),
		100*float64(len(r.failures))/float64(attempts))
	fmt.Printf("       append ms     p50=%.1f p90=%.1f p99=%.1f max=%.1f  mean=%.1f\n",
		ms(pct(steady, 50)), ms(pct(steady, 90)), ms(pct(steady, 99)),
		ms(steady[len(steady)-1]), ms(mean(steady)))

	sorted := append([]int(nil), perWriter...)
	sort.Ints(sorted)
	starved := 0
	for _, v := range sorted {
		if v <= 1 {
			starved++
		}
	}
	fmt.Printf("       per writer    min=%d p50=%d max=%d  <=1 append: %d/%d  Jain=%.3f (1/n=%.3f)\n",
		sorted[0], sorted[len(sorted)/2], sorted[len(sorted)-1],
		starved, r.writers, jain(perWriter), 1/float64(r.writers))

	if r.writers > 1 {
		top := 0
		for w, c := range perWriter {
			if c > perWriter[top] {
				top = w
			}
		}
		var rest []int64
		for w, v := range latByWriter {
			if int(w) != top {
				rest = append(rest, v...)
			}
		}
		win := append([]int64(nil), latByWriter[uint32(top)]...)
		sort.Slice(win, func(i, j int) bool { return win[i] < win[j] })
		sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
		fmt.Printf("       busiest w%d    %d appends (%.1f%%)  p50=%.1fms | others %d appends p50=%.1fms\n",
			top, perWriter[top], 100*float64(perWriter[top])/float64(len(steady)),
			ms(pct(win, 50)), len(rest), ms(pct(rest, 50)))
	}
}

func jain(counts []int) float64 {
	var sum, sumSq float64
	for _, c := range counts {
		sum += float64(c)
		sumSq += float64(c) * float64(c)
	}
	if sumSq == 0 {
		return 0
	}
	return sum * sum / (float64(len(counts)) * sumSq)
}

func pct(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func ms(micros int64) float64 { return float64(micros) / 1000 }

func mean(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	var sum int64
	for _, x := range v {
		sum += x
	}
	return sum / int64(len(v))
}

func runSanity(path string, readers int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var url string
	runs := map[int]*runResult{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return err
		}
		switch e.Type {
		case "meta":
			url = e.URL
		case "run":
			runs[e.Writers] = &runResult{writers: e.Writers, stream: e.Stream, seconds: e.Seconds}
		case "append":
			r := runs[e.Writers]
			if r == nil {
				return fmt.Errorf("append for unknown run n=%d", e.Writers)
			}
			r.appends = append(r.appends, appendRecord{
				writer: e.Writer, record: e.Record, seq: e.Seq,
			})
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if url == "" {
		return errors.New("no meta line in the log")
	}

	ctx := context.Background()
	store, err := objectstore.Open(ctx, url)
	if err != nil {
		return err
	}
	defer store.Close()

	order := make([]int, 0, len(runs))
	for n := range runs {
		order = append(order, n)
	}
	sort.Ints(order)

	failed := false
	for _, n := range order {
		ok, err := checkStream(ctx, store, runs[n], readers)
		if err != nil {
			return err
		}
		if !ok {
			failed = true
		}
	}
	if failed {
		return errors.New("sanity check failed")
	}
	fmt.Println("sanity check passed")
	return nil
}

func checkStream(ctx context.Context, store objectstore.Store, r *runResult, readers int) (bool, error) {
	stream := r.stream
	log, err := logstream.New(store, stream, nil)
	if err != nil {
		return false, err
	}
	acked := len(r.appends)
	tail, err := log.Tail(ctx)
	if err != nil {
		return false, fmt.Errorf("tail %s: %w", stream, err)
	}

	type segment struct {
		keys    []uint64
		missing bool
		bad     []string
	}
	segs := make([]segment, tail)

	var wg sync.WaitGroup
	work := make(chan uint64)
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seq := range work {
				s := &segs[seq-1]
				records, err := log.Read(ctx, seq)
				if errors.Is(err, logstream.ErrEndOfStream) {
					s.missing = true
					continue
				}
				if err != nil {
					s.bad = append(s.bad, fmt.Sprintf("seq %d: %v", seq, err))
					continue
				}
				for _, rec := range records {
					id, msg := verify(seq, rec)
					if msg != "" {
						s.bad = append(s.bad, msg)
						continue
					}
					s.keys = append(s.keys, id)
				}
			}
		}()
	}
	for seq := uint64(1); seq <= tail; seq++ {
		work <- seq
	}
	close(work)
	wg.Wait()

	seen := make(map[uint64]int, acked)
	var holes []uint64
	var bad []string
	records := 0
	for i := range segs {
		s := &segs[i]
		if s.missing {
			holes = append(holes, uint64(i+1))
			continue
		}
		bad = append(bad, s.bad...)
		records += len(s.keys)
		for _, k := range s.keys {
			seen[k]++
		}
	}

	lost := 0
	for _, a := range r.appends {
		if seen[a.record] == 0 {
			lost++
		}
	}
	duplicates := 0
	for _, count := range seen {
		if count > 1 {
			duplicates += count - 1
		}
	}

	ok := len(holes) == 0 && len(bad) == 0 && lost == 0
	status := "PASS"
	if !ok {
		status = "FAIL"
	}
	fmt.Printf("%s %-28s tail=%-7d records=%-7d acked=%-7d holes=%d corrupt=%d lost=%d dup=%d\n",
		status, stream, tail, records, acked, len(holes), len(bad), lost, duplicates)
	for i, msg := range bad {
		if i == 5 {
			fmt.Printf("     ... %d more\n", len(bad)-5)
			break
		}
		fmt.Println("    ", msg)
	}
	if len(holes) > 0 {
		fmt.Println("     first holes:", holes[:min(len(holes), 10)])
	}
	return ok, nil
}

func verify(seq uint64, rec logstream.Record) (uint64, string) {
	if len(rec) < headerSize {
		return 0, fmt.Sprintf("seq %d: record is %d bytes, want at least %d", seq, len(rec), headerSize)
	}
	id := binary.BigEndian.Uint64(rec[0:8])
	if n := binary.BigEndian.Uint32(rec[8:12]); int(n) != len(rec) {
		return 0, fmt.Sprintf("seq %d: length field %d, record %d bytes", seq, n, len(rec))
	}
	if want := binary.BigEndian.Uint32(rec[12:16]); want != crc32.ChecksumIEEE(rec[headerSize:]) {
		return 0, fmt.Sprintf("seq %d: record %d has a bad CRC", seq, id)
	}
	return id, ""
}
