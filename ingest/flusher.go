package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	PollInterval time.Duration
	Scope        namespace.Scope
}

func withDefaults(c Config) Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 100 * time.Millisecond
	}
	return c
}

type Flusher struct {
	store objectstore.Store
	log   *logstream.Log
	scope namespace.Scope
	cfg   Config

	branchCheckpoints map[string]uint64
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
		scope:             cfg.Scope,
		cfg:               withDefaults(cfg),
		branchCheckpoints: make(map[string]uint64),
	}, nil
}

func (f *Flusher) initCheckpoints(ctx context.Context) (uint64, error) {
	branches, err := namespace.ListBranches(ctx, f.store, "")
	if err != nil {
		return 0, fmt.Errorf("list branches: %w", err)
	}

	var minCheckpoint uint64
	hasBranch := false
	for _, branch := range branches {
		m, _, err := manifest.Read(ctx, f.store, branch)
		if err != nil {
			if errors.Is(err, objectstore.ErrNotFound) {
				continue
			}
			return 0, fmt.Errorf("read branch manifest %s: %w", branch, err)
		}
		f.branchCheckpoints[branch] = m.CheckpointSeq
		if !hasBranch || m.CheckpointSeq < minCheckpoint {
			minCheckpoint = m.CheckpointSeq
			hasBranch = true
		}
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
				return f.shutdownFlush(seq)
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
				return nil
			}
			return fmt.Errorf("drain log seq %d: %w", seq, err)
		}
		if err := f.processRecords(ctx, seq, records); err != nil {
			return err
		}
		seq++
	}
}

func (f *Flusher) processRecords(ctx context.Context, seq uint64, records []logstream.Record) error {
	branchMutations := make(map[string][]*storagepb.DocumentMutation)

	for _, rec := range records {
		var walRec storagepb.WalRecord
		if err := proto.Unmarshal(rec, &walRec); err != nil {
			return fmt.Errorf("unmarshal wal record at seq %d: %w", seq, err)
		}

		if evt := walRec.GetBranchEvent(); evt != nil {
			if evt.Type == storagepb.BranchLifecycleEvent_FORK {
				if muts := branchMutations[evt.ParentBranch]; len(muts) > 0 {
					if err := f.flushBranch(ctx, evt.ParentBranch, muts, seq); err != nil {
						return err
					}
					delete(branchMutations, evt.ParentBranch)
				}
				if lastSeq, ok := f.branchCheckpoints[evt.Branch]; !ok || seq > lastSeq {
					f.branchCheckpoints[evt.Branch] = seq
				}
			}
			continue
		}

		mut := walRec.GetMutation()
		if mut == nil || mut.Branch == "" {
			continue
		}

		if lastSeq, ok := f.branchCheckpoints[mut.Branch]; ok && seq <= lastSeq {
			continue
		}

		branchMutations[mut.Branch] = append(branchMutations[mut.Branch], mut)
	}

	for branch, muts := range branchMutations {
		if len(muts) > 0 {
			if err := f.flushBranch(ctx, branch, muts, seq); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *Flusher) flushBranch(ctx context.Context, branch string, mutations []*storagepb.DocumentMutation, seq uint64) error {
	if len(mutations) == 0 {
		return nil
	}

	encodeUploadStart := time.Now()
	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, mut := range mutations {
		if err := w.Write(mut); err != nil {
			return fmt.Errorf("write segment record: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close segment writer: %w", err)
	}
	data := buf.Bytes()

	scope, branchName := namespace.ScopeFromRef(branch)
	segID := fmt.Sprintf("%020d-%s", seq, branchName)
	segKey := scope.SegmentKey(segID)
	if _, err := f.store.Put(ctx, segKey, bytes.NewReader(data), objectstore.Condition{Absent: true}); err != nil {
		return fmt.Errorf("upload segment %s: %w", segKey, err)
	}
	encodeUploadDur := time.Since(encodeUploadStart)

	segRef := &storagepb.SegmentRef{
		SegmentId: segID,
		DocCount:  uint64(len(mutations)),
		DocsSize:  int64(len(data)),
		Key:       segKey,
	}

	casStart := time.Now()
	for {
		m, gen, err := manifest.Read(ctx, f.store, branch)
		if err != nil {
			if errors.Is(err, objectstore.ErrNotFound) {
				m = &storagepb.BranchManifest{
					SchemaVersion: 1,
				}
				gen = ""
			} else {
				return fmt.Errorf("read branch manifest %s: %w", branch, err)
			}
		}

		alreadyPresent := false
		for _, s := range m.Segments {
			if s.SegmentId == segRef.SegmentId {
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			m.Segments = append(m.Segments, segRef)
		}

		if seq > m.CheckpointSeq {
			m.CheckpointSeq = seq
		}

		_, err = manifest.Write(ctx, f.store, branch, m, gen)
		if err == nil {
			_ = namespace.AddBranch(ctx, f.store, "", branch)
			f.branchCheckpoints[branch] = seq
			casDur := time.Since(casStart)

			slog.Info("flush",
				"branch", branch,
				"docs", len(mutations),
				"encode_upload_dur", encodeUploadDur,
				"cas_dur", casDur,
			)
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return fmt.Errorf("update branch %s: %w", branch, err)
		}
	}
}
