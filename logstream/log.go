package logstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

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

func WithTailListLimit(limit int) Option {
	return func(l *Log) {
		if limit > 0 {
			l.listLimit = limit
		}
	}
}

type streamState struct {
	ch        chan struct{}
	lastKnown uint64
}

type Log struct {
	store         *objectstore.Store
	prefix        string
	maxRecordSize int
	listLimit     int

	streamsMu sync.Mutex
	streams   map[string]*streamState
}

func New(store *objectstore.Store, opts ...Option) *Log {
	l := &Log{
		store:         store,
		prefix:        "wal",
		maxRecordSize: recordio.DefaultMaxRecordSize,
		listLimit:     1000,
		streams:       make(map[string]*streamState),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

const gateDistance = 16

func (l *Log) stream(name string) *streamState {
	l.streamsMu.Lock()
	defer l.streamsMu.Unlock()
	s, ok := l.streams[name]
	if !ok {
		s = &streamState{
			ch: make(chan struct{}, 1),
		}
		l.streams[name] = s
	}
	return s
}

func (l *Log) Append(ctx context.Context, stream string, records []Record) (uint64, error) {
	if len(records) == 0 {
		return 0, errors.New("logstream: batch is empty")
	}
	if !validStreamName(stream) {
		return 0, fmt.Errorf("logstream: invalid stream name %q", stream)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
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

	st := l.stream(stream)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case st.ch <- struct{}{}:
	}
	defer func() { <-st.ch }()

	if st.lastKnown == 0 {
		t, err := l.Tail(ctx, stream)
		if err != nil {
			return 0, err
		}
		st.lastKnown = t
	}

	seq := st.lastKnown + 1
	runway := 3
	tries := 0

	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		key := segmentKey(l.prefix, stream, seq)
		_, err := l.store.Put(ctx, key, bytes.NewReader(payload), &objectstore.Condition{Absent: true})
		if err == nil {
			st.lastKnown = seq
			return seq, nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return 0, err
		}

		tries++
		if tries < runway {
			seq++
		} else {
			if seq > math.MaxUint64-gateDistance {
				return 0, errors.New("logstream: sequence number overflow")
			}
			gateKey := segmentKey(l.prefix, stream, seq+gateDistance)
			ex, err := exists(ctx, l.store, gateKey)
			if err != nil {
				return 0, err
			}
			if !ex {
				seq++
				tries = 0
			} else {
				probe := func(ctx context.Context, s uint64) (bool, error) {
					return exists(ctx, l.store, segmentKey(l.prefix, stream, s))
				}
				head, probes, err := jump(ctx, seq+gateDistance, probe)
				if err != nil {
					return 0, err
				}
				seq = head + 1
				runway = max(3, 2*probes)
				tries = 0
			}
		}

		delay := time.Duration(1+rand.IntN(15)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *Log) Read(ctx context.Context, stream string, seq uint64) ([]Record, error) {
	if !validStreamName(stream) {
		return nil, fmt.Errorf("logstream: invalid stream name %q", stream)
	}
	if seq == 0 {
		return nil, errors.New("logstream: sequence number must be greater than zero")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	key := segmentKey(l.prefix, stream, seq)
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

func (l *Log) Tail(ctx context.Context, stream string) (uint64, error) {
	if !validStreamName(stream) {
		return 0, fmt.Errorf("logstream: invalid stream name %q", stream)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	prefix := fmt.Sprintf("%s/%s/", l.prefix, stream)
	objs, err := l.store.List(ctx, prefix, "", l.listLimit)
	if err != nil {
		return 0, err
	}
	if len(objs) == 0 {
		return 0, nil
	}

	lastSeq, err := parseSeq(objs[len(objs)-1].Key)
	if err != nil {
		return 0, err
	}
	if len(objs) < l.listLimit {
		return lastSeq, nil
	}

	probe := func(ctx context.Context, s uint64) (bool, error) {
		return exists(ctx, l.store, segmentKey(l.prefix, stream, s))
	}
	head, _, err := jump(ctx, lastSeq, probe)
	return head, err
}

func jump(ctx context.Context, lo uint64, probe func(context.Context, uint64) (bool, error)) (uint64, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
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
		if err := ctx.Err(); err != nil {
			return 0, probes, err
		}
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
		if err := ctx.Err(); err != nil {
			return 0, probes, err
		}
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

func exists(ctx context.Context, store *objectstore.Store, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	objs, err := store.List(ctx, key, "", 1)
	if err != nil {
		return false, err
	}
	return len(objs) > 0 && objs[0].Key == key, nil
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
