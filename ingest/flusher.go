package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	DocThreshold  int
	TimeThreshold time.Duration
	PollInterval  time.Duration
	CheckpointSeq uint64
}

func (c *Config) setDefaults() {
	if c.DocThreshold <= 0 {
		c.DocThreshold = 10000
	}
	if c.TimeThreshold <= 0 {
		c.TimeThreshold = 10 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 100 * time.Millisecond
	}
}

type memtable struct {
	branch    string
	mutations []*storagepb.DocumentMutation
	firstAt   time.Time
	lastSeq   uint64
}

type Flusher struct {
	store objectstore.Store
	log   *logstream.Log
	cfg   Config

	mu                sync.Mutex
	memtables         map[string]*memtable
	branchCheckpoints map[string]uint64
	checkpointSeq     uint64
}

func NewFlusher(store objectstore.Store, log *logstream.Log, cfg Config) (*Flusher, error) {
	if store == nil {
		return nil, errors.New("ingest: nil store")
	}
	if log == nil {
		return nil, errors.New("ingest: nil log")
	}
	cfg.setDefaults()

	return &Flusher{
		store:             store,
		log:               log,
		cfg:               cfg,
		memtables:         make(map[string]*memtable),
		branchCheckpoints: make(map[string]uint64),
		checkpointSeq:     cfg.CheckpointSeq,
	}, nil
}

func (f *Flusher) CheckpointSeq() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpointSeq
}

func (f *Flusher) initCheckpoints(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	objs, err := f.store.List(ctx, "refs/heads/", "", 1000)
	if err != nil {
		return fmt.Errorf("list branch refs: %w", err)
	}

	var minCheckpoint uint64
	hasBranch := false

	for _, obj := range objs {
		branch := strings.TrimPrefix(obj.Key, "refs/heads/")
		if branch == "" {
			continue
		}
		manifest, _, err := kvfs.ResolveBranch(ctx, f.store, branch)
		if err != nil {
			if errors.Is(err, objectstore.ErrNotFound) {
				continue
			}
			return fmt.Errorf("resolve branch %s: %w", branch, err)
		}
		f.branchCheckpoints[branch] = manifest.CheckpointSeq
		if !hasBranch || manifest.CheckpointSeq < minCheckpoint {
			minCheckpoint = manifest.CheckpointSeq
			hasBranch = true
		}
	}

	if f.checkpointSeq == 0 && hasBranch {
		f.checkpointSeq = minCheckpoint
	}
	return nil
}

func (f *Flusher) Run(ctx context.Context) error {
	if err := f.initCheckpoints(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = f.initCheckpoints(context.Background())
			f.mu.Lock()
			seq := f.checkpointSeq + 1
			f.mu.Unlock()
			return f.drainAndFlush(seq)
		}
		return err
	}

	f.mu.Lock()
	seq := f.checkpointSeq + 1
	f.mu.Unlock()

	for {
		records, err := f.log.Read(ctx, seq)
		if err != nil {
			if errors.Is(err, logstream.ErrEndOfStream) {
				if err := f.flushExpiredMemtables(ctx); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return f.drainAndFlush(seq)
					}
					return err
				}
				select {
				case <-ctx.Done():
					return f.drainAndFlush(seq)
				case <-time.After(f.cfg.PollInterval):
					continue
				}
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return f.drainAndFlush(seq)
			}
			return fmt.Errorf("read log seq %d: %w", seq, err)
		}

		if err := f.processRecords(ctx, seq, records); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return f.drainAndFlush(seq + 1)
			}
			return err
		}

		seq++
	}
}

func (f *Flusher) processRecords(ctx context.Context, seq uint64, records []logstream.Record) error {
	for _, rec := range records {
		var walRec storagepb.WalRecord
		if err := proto.Unmarshal(rec, &walRec); err != nil {
			return fmt.Errorf("unmarshal wal record at seq %d: %w", seq, err)
		}

		mut := walRec.GetMutation()
		if mut == nil || mut.Branch == "" {
			continue
		}

		f.mu.Lock()
		if lastSeq, ok := f.branchCheckpoints[mut.Branch]; ok && seq <= lastSeq {
			f.mu.Unlock()
			continue
		}

		mt, ok := f.memtables[mut.Branch]
		if !ok {
			mt = &memtable{
				branch:  mut.Branch,
				firstAt: time.Now(),
			}
			f.memtables[mut.Branch] = mt
		}
		mt.mutations = append(mt.mutations, mut)
		mt.lastSeq = seq

		shouldFlush := len(mt.mutations) >= f.cfg.DocThreshold
		if shouldFlush {
			delete(f.memtables, mut.Branch)
		}
		f.mu.Unlock()

		if shouldFlush {
			if err := f.flushMemtable(ctx, mt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *Flusher) flushExpiredMemtables(ctx context.Context) error {
	now := time.Now()
	var toFlush []*memtable

	f.mu.Lock()
	for branch, mt := range f.memtables {
		if len(mt.mutations) > 0 && now.Sub(mt.firstAt) >= f.cfg.TimeThreshold {
			toFlush = append(toFlush, mt)
			delete(f.memtables, branch)
		}
	}
	f.mu.Unlock()

	for _, mt := range toFlush {
		if err := f.flushMemtable(ctx, mt); err != nil {
			return err
		}
	}
	return nil
}

func (f *Flusher) drainAndFlush(startSeq uint64) error {
	drainCtx := context.Background()
	seq := startSeq

	for {
		records, err := f.log.Read(drainCtx, seq)
		if err != nil {
			if errors.Is(err, logstream.ErrEndOfStream) {
				break
			}
			return fmt.Errorf("drain log seq %d: %w", seq, err)
		}
		if err := f.processRecords(drainCtx, seq, records); err != nil {
			return err
		}
		seq++
	}

	return f.flushAll(drainCtx)
}

func (f *Flusher) flushAll(ctx context.Context) error {
	var toFlush []*memtable

	f.mu.Lock()
	for _, mt := range f.memtables {
		if len(mt.mutations) > 0 {
			toFlush = append(toFlush, mt)
		}
	}
	f.memtables = make(map[string]*memtable)
	f.mu.Unlock()

	for _, mt := range toFlush {
		if err := f.flushMemtable(ctx, mt); err != nil {
			return err
		}
	}
	return nil
}

func (f *Flusher) flushMemtable(ctx context.Context, mt *memtable) error {
	if len(mt.mutations) == 0 {
		return nil
	}

	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, mut := range mt.mutations {
		if err := w.Write(mut); err != nil {
			return fmt.Errorf("write segment record: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close segment writer: %w", err)
	}
	data := buf.Bytes()

	segID := newSegmentID()
	segKey := fmt.Sprintf("segments/%s/%s.recordio", mt.branch, segID)
	if _, err := f.store.Put(ctx, segKey, bytes.NewReader(data), objectstore.Condition{Absent: true}); err != nil {
		return fmt.Errorf("upload segment %s: %w", segKey, err)
	}

	segRef := &storagepb.SegmentRef{
		SegmentId: segID,
		DocCount:  uint64(len(mt.mutations)),
		DocsSize:  int64(len(data)),
	}

	endSeq := mt.lastSeq

	for {
		manifest, gen, err := kvfs.ResolveBranch(ctx, f.store, mt.branch)
		if err != nil {
			if errors.Is(err, objectstore.ErrNotFound) {
				manifest = &storagepb.BranchManifest{
					SchemaVersion: 1,
				}
				gen = ""
			} else {
				return fmt.Errorf("resolve branch %s: %w", mt.branch, err)
			}
		}

		alreadyPresent := false
		for _, s := range manifest.Segments {
			if s.SegmentId == segRef.SegmentId {
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			manifest.Segments = append(manifest.Segments, segRef)
		}

		if endSeq > manifest.CheckpointSeq {
			manifest.CheckpointSeq = endSeq
		}

		_, err = kvfs.UpdateBranch(ctx, f.store, mt.branch, manifest, gen)
		if err == nil {
			f.mu.Lock()
			f.branchCheckpoints[mt.branch] = endSeq
			f.mu.Unlock()
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return fmt.Errorf("update branch %s: %w", mt.branch, err)
		}
	}
}

func newSegmentID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
