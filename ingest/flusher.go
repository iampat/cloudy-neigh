package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
}

func withDefaults(c Config) Config {
	if c.DocThreshold <= 0 {
		c.DocThreshold = 10000
	}
	if c.TimeThreshold <= 0 {
		c.TimeThreshold = 10 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 100 * time.Millisecond
	}
	return c
}

type memtable struct {
	branch    string
	mutations []*storagepb.DocumentMutation
	firstAt   time.Time
	lastSeq   uint64
}

const listLimit = 1000

type Flusher struct {
	store objectstore.Store
	log   *logstream.Log
	cfg   Config

	memtables         map[string]*memtable
	branchCheckpoints map[string]uint64

	lastSegTime string
	lastMicros  uint64
}

func NewFlusher(store objectstore.Store, log *logstream.Log, cfg Config) (*Flusher, error) {
	if store == nil {
		return nil, errors.New("ingest: nil store")
	}
	if log == nil {
		return nil, errors.New("ingest: nil log")
	}

	return &Flusher{
		store:             store,
		log:               log,
		cfg:               withDefaults(cfg),
		memtables:         make(map[string]*memtable),
		branchCheckpoints: make(map[string]uint64),
	}, nil
}

func (f *Flusher) initCheckpoints(ctx context.Context) (uint64, error) {
	var startAfter string
	var minCheckpoint uint64
	hasBranch := false

	for {
		branches, err := kvfs.ListBranches(ctx, f.store, startAfter, listLimit)
		if err != nil {
			return 0, fmt.Errorf("list branches: %w", err)
		}
		if len(branches) == 0 {
			break
		}

		for _, branch := range branches {
			manifest, _, err := kvfs.ResolveBranch(ctx, f.store, branch)
			if err != nil {
				if errors.Is(err, objectstore.ErrNotFound) {
					continue
				}
				return 0, fmt.Errorf("resolve branch %s: %w", branch, err)
			}
			f.branchCheckpoints[branch] = manifest.CheckpointSeq
			if !hasBranch || manifest.CheckpointSeq < minCheckpoint {
				minCheckpoint = manifest.CheckpointSeq
				hasBranch = true
			}
		}

		if len(branches) < listLimit {
			break
		}
		startAfter = branches[len(branches)-1]
	}

	if hasBranch {
		return minCheckpoint, nil
	}
	return 0, nil
}

func (f *Flusher) Run(ctx context.Context) error {
	minCheckpoint, err := f.initCheckpoints(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return f.shutdownFlush(0)
		}
		return err
	}

	seq := minCheckpoint + 1

	for {
		records, err := f.log.Read(ctx, seq)
		if err != nil {
			if errors.Is(err, logstream.ErrEndOfStream) {
				if err := f.flushExpiredMemtables(ctx); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return f.shutdownFlush(seq)
					}
					return err
				}
				select {
				case <-ctx.Done():
					return f.shutdownFlush(seq)
				case <-time.After(f.cfg.PollInterval):
					continue
				}
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return f.shutdownFlush(seq)
			}
			return fmt.Errorf("read log seq %d: %w", seq, err)
		}

		if err := f.processRecords(ctx, seq, records); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return f.shutdownFlush(seq + 1)
			}
			return err
		}

		seq++
	}
}

func (f *Flusher) shutdownFlush(startSeq uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if startSeq == 0 {
		minSeq, err := f.initCheckpoints(ctx)
		if err != nil {
			return fmt.Errorf("init checkpoints on shutdown: %w", err)
		}
		startSeq = minSeq + 1
	}

	seq := startSeq
	for {
		records, err := f.log.Read(ctx, seq)
		if err != nil {
			if errors.Is(err, logstream.ErrEndOfStream) {
				break
			}
			return fmt.Errorf("drain log seq %d: %w", seq, err)
		}
		if err := f.processRecords(ctx, seq, records); err != nil {
			return err
		}
		seq++
	}

	return f.flushAll(ctx)
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

		if lastSeq, ok := f.branchCheckpoints[mut.Branch]; ok && seq <= lastSeq {
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
	}

	var toFlush []*memtable
	for _, mt := range f.memtables {
		if len(mt.mutations) >= f.cfg.DocThreshold {
			toFlush = append(toFlush, mt)
		}
	}

	for _, mt := range toFlush {
		if err := f.flushMemtable(ctx, mt); err != nil {
			return err
		}
		delete(f.memtables, mt.branch)
	}
	return nil
}

func (f *Flusher) flushExpiredMemtables(ctx context.Context) error {
	now := time.Now()
	var toFlush []*memtable
	for _, mt := range f.memtables {
		if len(mt.mutations) > 0 && now.Sub(mt.firstAt) >= f.cfg.TimeThreshold {
			toFlush = append(toFlush, mt)
		}
	}

	for _, mt := range toFlush {
		if err := f.flushMemtable(ctx, mt); err != nil {
			return err
		}
		delete(f.memtables, mt.branch)
	}
	return nil
}

func (f *Flusher) flushAll(ctx context.Context) error {
	var toFlush []*memtable
	for _, mt := range f.memtables {
		if len(mt.mutations) > 0 {
			toFlush = append(toFlush, mt)
		}
	}

	for _, mt := range toFlush {
		if err := f.flushMemtable(ctx, mt); err != nil {
			return err
		}
		delete(f.memtables, mt.branch)
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

	segID, err := f.newSegmentID()
	if err != nil {
		return fmt.Errorf("generate segment id: %w", err)
	}
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
			f.branchCheckpoints[mt.branch] = endSeq
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return fmt.Errorf("update branch %s: %w", mt.branch, err)
		}
	}
}

func (f *Flusher) newSegmentID() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	now := time.Now().UTC()
	ts := now.Format("20060102150405")
	micros := uint64(now.Nanosecond() / 1000)

	if ts < f.lastSegTime {
		ts = f.lastSegTime
		micros = f.lastMicros + 1
	} else if ts == f.lastSegTime && micros <= f.lastMicros {
		micros = f.lastMicros + 1
	}

	f.lastSegTime = ts
	f.lastMicros = micros

	return fmt.Sprintf("%s-%06d%s", ts, micros, hex.EncodeToString(b[:])), nil
}
