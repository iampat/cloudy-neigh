package logstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/iampat/cloudy-neigh/internal/xtime"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/iampat/cloudy-neigh/recordio"
)

var ErrEndOfStream = errors.New("logstream: end of stream")

type Record []byte

type Option func(*Log)

func WithPrefix(prefix string) Option {
	return func(l *Log) {
		if prefix != "" {
			l.prefix = strings.Trim(prefix, "/")
		}
	}
}

func WithMaxRecordSize(max int) Option {
	return func(l *Log) {
		if max > 0 {
			l.maxRecordSize = max
		}
	}
}

const headListLimit = 1000

type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error)
	Get(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error)
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error)
}

type Log struct {
	store         ObjectStore
	stream        string
	prefix        string
	maxRecordSize int

	ch        chan struct{}
	lastKnown uint64
	winStreak int
	rttEMA    time.Duration
}

func New(store ObjectStore, stream string, opts ...Option) (*Log, error) {
	if !validStreamName(stream) {
		return nil, fmt.Errorf("logstream: invalid stream name %q", stream)
	}
	l := &Log{
		store:         store,
		stream:        stream,
		prefix:        "wal",
		maxRecordSize: recordio.DefaultMaxRecordSize,
		ch:            make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

func (l *Log) Append(ctx context.Context, records []Record) (uint64, error) {
	if len(records) == 0 {
		return 0, errors.New("logstream: batch is empty")
	}
	for _, r := range records {
		if len(r) > l.maxRecordSize {
			return 0, fmt.Errorf("logstream: record size %d exceeds max %d", len(r), l.maxRecordSize)
		}
	}

	var buf bytes.Buffer
	w := recordio.NewWriter(&buf)
	for _, r := range records {
		if _, _, err := w.WriteRecord(r); err != nil {
			return 0, err
		}
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	payload := buf.Bytes()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case l.ch <- struct{}{}:
	}
	defer func() { <-l.ch }()

	if l.lastKnown == 0 {
		t, err := l.Tail(ctx)
		if err != nil {
			return 0, err
		}
		l.lastKnown = t
	}

	seq := l.lastKnown + 1
	first := seq
	var collisions, lists, jumpProbes int
	start := time.Now()

	for {
		key := segmentKey(l.prefix, l.stream, seq)
		putStart := time.Now()
		_, err := l.store.Put(ctx, key, bytes.NewReader(payload), objectstore.Condition{Absent: true})
		if err == nil {
			l.lastKnown = seq
			slog.Debug("logstream append",
				"stream", l.stream,
				"seq", seq,
				"first_try", first,
				"collisions", collisions,
				"lists", lists,
				"jump_probes", jumpProbes,
				"uploads", collisions+1,
				"elapsed_ms", time.Since(start).Milliseconds())
			_ = l.paceWinner(ctx, collisions, time.Since(putStart))
			return seq, nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return 0, err
		}
		collisions++

		head, probes, err := l.head(ctx, seq)
		lists++
		jumpProbes += probes
		if err != nil {
			return 0, err
		}
		seq = head + 1
	}
}

func (l *Log) paceWinner(ctx context.Context, collisions int, putDuration time.Duration) error {
	if l.rttEMA == 0 {
		l.rttEMA = putDuration
	} else {
		l.rttEMA = (l.rttEMA*4 + putDuration) / 5
	}

	if collisions > 0 {
		l.winStreak = 0
		return nil
	}

	l.winStreak++
	if l.winStreak <= 1 {
		return nil
	}

	delayFactor := min(float64(l.winStreak-1)*0.25, 1.0)
	baseDelay := time.Duration(float64(l.rttEMA) * delayFactor)
	var jitter time.Duration
	if l.rttEMA > 10 {
		jitter = time.Duration(rand.Int64N(int64(l.rttEMA / 10)))
	}
	return xtime.Sleep(ctx, baseDelay+jitter)
}

func (l *Log) head(ctx context.Context, lo uint64) (uint64, int, error) {
	var start string
	if lo > 0 {
		start = segmentKey(l.prefix, l.stream, lo)
	}
	objs, err := l.store.List(ctx, l.streamPrefix(), start, headListLimit)
	if err != nil {
		return 0, 0, err
	}
	if len(objs) == 0 {
		return lo, 0, nil
	}
	last, err := parseSeq(objs[len(objs)-1].Key)
	if err != nil {
		return 0, 0, err
	}
	if len(objs) < headListLimit {
		return last, 0, nil
	}
	return jump(ctx, last, l.probe)
}

func (l *Log) streamPrefix() string {
	return fmt.Sprintf("%s/%s/", l.prefix, l.stream)
}

func (l *Log) probe(ctx context.Context, seq uint64) (bool, error) {
	return l.store.Exists(ctx, segmentKey(l.prefix, l.stream, seq))
}

func (l *Log) Read(ctx context.Context, seq uint64) ([]Record, error) {
	if seq == 0 {
		return nil, errors.New("logstream: sequence number must be greater than zero")
	}
	key := segmentKey(l.prefix, l.stream, seq)
	rc, _, err := l.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, ErrEndOfStream
		}
		return nil, err
	}
	defer rc.Close()

	s := recordio.NewScanner(rc, recordio.WithScannerMaxRecordSize(l.maxRecordSize))
	var all []byte
	var lens []int
	for s.Scan() {
		rec := s.Record()
		lens = append(lens, len(rec))
		all = append(all, rec...)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}

	records := make([]Record, len(lens))
	offset := 0
	for i, n := range lens {
		records[i] = Record(all[offset : offset+n])
		offset += n
	}
	return records, nil
}

func (l *Log) Tail(ctx context.Context) (uint64, error) {
	head, _, err := l.head(ctx, 0)
	return head, err
}

func jump(ctx context.Context, lo uint64, probe func(context.Context, uint64) (bool, error)) (uint64, int, error) {
	if lo == math.MaxUint64 {
		return lo, 0, nil
	}

	ex, err := probe(ctx, lo+1)
	if err != nil {
		return 0, 1, err
	}
	if !ex {
		return lo, 1, nil
	}

	probes := 1
	low := lo + 1
	d := uint64(2)
	var high uint64

	for {
		var target uint64
		if d > math.MaxUint64-lo {
			target = math.MaxUint64
		} else {
			target = lo + d
		}

		ex, err := probe(ctx, target)
		probes++
		if err != nil {
			return 0, probes, err
		}
		if !ex {
			high = target
			break
		}
		low = target
		if target == math.MaxUint64 {
			return target, probes, nil
		}
		if d > math.MaxUint64/2 {
			d = math.MaxUint64
		} else {
			d *= 2
		}
	}

	for low+1 < high {
		mid := low + (high-low)/2
		probes++
		ok, err := probe(ctx, mid)
		if err != nil {
			return 0, probes, err
		}
		if ok {
			low = mid
		} else {
			high = mid
		}
	}
	return low, probes, nil
}

func segmentKey(prefix, stream string, seq uint64) string {
	return fmt.Sprintf("%s/%s/%020d.recordio", prefix, stream, seq)
}

func parseSeq(key string) (uint64, error) {
	base := path.Base(key)
	if !strings.HasSuffix(base, ".recordio") {
		return 0, fmt.Errorf("logstream: key %q does not end with .recordio", key)
	}
	raw := strings.TrimSuffix(base, ".recordio")
	if len(raw) != 20 {
		return 0, fmt.Errorf("logstream: key %q sequence number has length %d, want 20", key, len(raw))
	}
	seq, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("logstream: invalid sequence number in key %q: %w", key, err)
	}
	if seq == 0 {
		return 0, fmt.Errorf("logstream: key %q has sequence number 0", key)
	}
	return seq, nil
}

func validStreamName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
