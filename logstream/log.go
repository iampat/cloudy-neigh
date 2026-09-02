package logstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// Winner pacing yields time on consecutive uncontested appends so competing writers
// have a window to claim sequence slots without starvation.
const (
	headListLimit = 1000
	basePacingRTT = 20 * time.Millisecond
)

type Log struct {
	store  objectstore.Store
	prefix string

	ch        chan struct{}
	lastKnown uint64
	winStreak int
}

func New(store objectstore.Store, prefix string) (*Log, error) {
	if store == nil {
		return nil, errors.New("logstream: nil store")
	}
	if !validPrefix(prefix) {
		return nil, fmt.Errorf("logstream: invalid prefix %q", prefix)
	}
	return &Log{
		store:  store,
		prefix: prefix,
		ch:     make(chan struct{}, 1),
	}, nil
}

func (l *Log) Append(ctx context.Context, records []Record) (uint64, error) {
	if len(records) == 0 {
		return 0, errors.New("logstream: batch is empty")
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
		key := segmentKey(l.prefix, seq)
		_, err := l.store.Put(ctx, key, bytes.NewReader(payload), objectstore.Condition{Absent: true})
		if err == nil {
			l.lastKnown = seq
			slog.Debug("logstream append",
				"prefix", l.prefix,
				"seq", seq,
				"first_try", first,
				"collisions", collisions,
				"lists", lists,
				"jump_probes", jumpProbes,
				"uploads", collisions+1,
				"elapsed_ms", time.Since(start).Milliseconds())
			_ = l.paceWinner(ctx, collisions)
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

func (l *Log) paceWinner(ctx context.Context, collisions int) error {
	if collisions > 0 {
		l.winStreak = 0
		return nil
	}

	l.winStreak++
	if l.winStreak <= 1 {
		return nil
	}

	delayFactor := min(float64(l.winStreak-1)*0.25, 1.0)
	baseDelay := time.Duration(float64(basePacingRTT) * delayFactor)
	var jitter time.Duration
	if basePacingRTT > 10 {
		jitter = time.Duration(rand.Int64N(int64(basePacingRTT / 10)))
	}
	return xtime.Sleep(ctx, baseDelay+jitter)
}

func (l *Log) head(ctx context.Context, lo uint64) (uint64, int, error) {
	var start string
	if lo > 0 {
		start = segmentKey(l.prefix, lo)
	}
	objs, err := l.store.List(ctx, l.prefix+"/", start, headListLimit)
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

func (l *Log) probe(ctx context.Context, seq uint64) (bool, error) {
	return l.store.Exists(ctx, segmentKey(l.prefix, seq))
}

func (l *Log) Read(ctx context.Context, seq uint64) ([]Record, error) {
	if seq == 0 {
		return nil, errors.New("logstream: sequence number must be greater than zero")
	}
	key := segmentKey(l.prefix, seq)
	rc, _, err := l.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, ErrEndOfStream
		}
		return nil, err
	}
	defer rc.Close()

	s := recordio.NewScanner(rc)
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

	step := uint64(1)
	low := lo + 1
	high := lo + 1
	probes := 1

	for {
		if step > (math.MaxUint64-high)/2 {
			high = math.MaxUint64
		} else {
			high += step * 2
			step *= 2
		}

		probes++
		ok, err := probe(ctx, high)
		if err != nil {
			return 0, probes, err
		}
		if !ok {
			break
		}
		low = high
		if high == math.MaxUint64 {
			return high, probes, nil
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

func segmentKey(prefix string, seq uint64) string {
	return fmt.Sprintf("%s/%020d.recordio", prefix, seq)
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

func validPrefix(prefix string) bool {
	if len(prefix) == 0 || prefix[0] == '/' || prefix[len(prefix)-1] == '/' || strings.Contains(prefix, "//") {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '/' {
			continue
		}
		return false
	}
	return true
}
