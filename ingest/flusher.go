package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	PollInterval time.Duration
}

type Flusher struct {
	store objectstore.Store
	cfg   Config

	mu       sync.Mutex
	flushers map[string]*streamFlusher
	cancels  map[string]context.CancelFunc
}

func NewFlusher(store objectstore.Store, cfg Config) (*Flusher, error) {
	if store == nil {
		return nil, errors.New("ingest: nil store")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}

	return &Flusher{
		store: store,
		cfg:   cfg,
	}, nil
}

func (f *Flusher) Run(ctx context.Context) error {
	var g errgroup.Group

	f.mu.Lock()
	f.flushers = make(map[string]*streamFlusher)
	f.cancels = make(map[string]context.CancelFunc)
	f.mu.Unlock()

	discoverCtx, cancelDiscover := context.WithTimeout(context.Background(), 5*time.Second)
	initialTargets, err := f.discoverStreams(discoverCtx)
	cancelDiscover()
	if err != nil {
		return err
	}
	for _, target := range initialTargets {
		if err := f.startStream(ctx, &g, target); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(f.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			f.stopAllStreams()
			return g.Wait()
		case <-ticker.C:
			targets, err := f.discoverStreams(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					f.stopAllStreams()
					return g.Wait()
				}
				slog.Warn("discover streams failed", "err", err)
				continue
			}
			for _, target := range targets {
				if err := f.startStream(ctx, &g, target); err != nil {
					f.stopAllStreams()
					return err
				}
			}
		}
	}
}

type streamTarget struct {
	scope     namespace.Scope
	walPrefix string
}

func (f *Flusher) discoverStreams(ctx context.Context) ([]streamTarget, error) {
	var targets []streamTarget
	seen := make(map[string]bool)

	add := func(target streamTarget) {
		if !seen[target.walPrefix] {
			seen[target.walPrefix] = true
			targets = append(targets, target)
		}
	}

	defaultScope := namespace.Scope{
		Tenant:    namespace.DefaultTenant,
		Namespace: namespace.DefaultNamespace,
	}
	add(streamTarget{
		scope:     defaultScope,
		walPrefix: defaultScope.WALPrefix(),
	})

	startAfter := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		objs, err := f.store.List(ctx, "", startAfter, 100)
		if err != nil {
			return nil, err
		}
		if len(objs) == 0 {
			break
		}

		for _, obj := range objs {
			target, nextJump, ok := parseStreamTarget(obj.Key)
			if ok {
				add(target)
			}
			if nextJump > startAfter {
				startAfter = nextJump
			} else if obj.Key > startAfter {
				startAfter = obj.Key
			}
		}

		if len(objs) < 100 {
			break
		}
	}

	return targets, nil
}

func parseStreamTarget(key string) (streamTarget, string, bool) {
	if tenant, rest, ok := strings.Cut(key, "/"+namespace.NamespaceDir+"/"); ok {
		ns, _, _ := strings.Cut(rest, "/")
		if tenant != "" && ns != "" {
			scope := namespace.Scope{Tenant: tenant, Namespace: ns}
			if scope.Validate() == nil {
				jump := path.Join(tenant, namespace.NamespaceDir, ns) + "/~"
				return streamTarget{
					scope:     scope,
					walPrefix: scope.WALPrefix(),
				}, jump, true
			}
		}
	}
	if strings.HasPrefix(key, "wal/") {
		return streamTarget{
			scope:     namespace.Scope{},
			walPrefix: "wal",
		}, "wal/~", true
	}
	return streamTarget{}, key, false
}

func (f *Flusher) startStream(ctx context.Context, g *errgroup.Group, target streamTarget) error {
	f.mu.Lock()
	if _, exists := f.flushers[target.walPrefix]; exists {
		f.mu.Unlock()
		return nil
	}
	log, err := logstream.New(f.store, target.walPrefix)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	sf := &streamFlusher{
		store:             f.store,
		log:               log,
		scope:             target.scope,
		walPrefix:         target.walPrefix,
		pollInterval:      f.cfg.PollInterval,
		branchCheckpoints: make(map[string]uint64),
	}
	streamCtx, cancel := context.WithCancel(ctx)
	f.flushers[target.walPrefix] = sf
	f.cancels[target.walPrefix] = cancel
	f.mu.Unlock()

	g.Go(func() error {
		return sf.Run(streamCtx)
	})
	return nil
}

func (f *Flusher) stopAllStreams() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cancel := range f.cancels {
		cancel()
	}
}

type streamFlusher struct {
	store             objectstore.Store
	log               *logstream.Log
	scope             namespace.Scope
	walPrefix         string
	pollInterval      time.Duration
	branchCheckpoints map[string]uint64
}

func (s *streamFlusher) initCheckpoints(ctx context.Context) (uint64, error) {
	var branches []string
	var err error
	if s.walPrefix == "wal" && s.scope == (namespace.Scope{}) {
		branches, err = namespace.ListBranches(ctx, s.store, "")
	} else {
		branches, err = s.scope.ListBranches(ctx, s.store)
	}
	if err != nil {
		return 0, fmt.Errorf("list branches: %w", err)
	}

	rootBranches, _, _ := namespace.ReadBranchesJSON(ctx, s.store, namespace.BranchesFile)
	for _, rb := range rootBranches {
		bScope, _ := namespace.ScopeFromRef(rb)
		if bScope == s.scope {
			found := false
			for _, b := range branches {
				if b == rb {
					found = true
					break
				}
			}
			if !found {
				branches = append(branches, rb)
			}
		}
	}

	var minCheckpoint uint64
	hasBranch := false
	for _, branch := range branches {
		m, _, err := manifest.Read(ctx, s.store, branch)
		if err != nil {
			if errors.Is(err, objectstore.ErrNotFound) {
				continue
			}
			return 0, fmt.Errorf("read branch manifest %s: %w", branch, err)
		}
		s.branchCheckpoints[branch] = m.CheckpointSeq
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

func (s *streamFlusher) Run(ctx context.Context) error {
	minCheckpoint, err := s.initCheckpoints(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return s.shutdownFlush(0)
		}
		return err
	}

	seq := minCheckpoint + 1

	for {
		records, err := s.log.Read(ctx, seq)
		if err != nil {
			if errors.Is(err, logstream.ErrEndOfStream) {
				select {
				case <-ctx.Done():
					return s.shutdownFlush(seq)
				case <-time.After(s.pollInterval):
					continue
				}
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return s.shutdownFlush(seq)
			}
			return fmt.Errorf("read log seq %d: %w", seq, err)
		}

		if err := s.processRecords(ctx, seq, records); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return s.shutdownFlush(seq)
			}
			return err
		}

		seq++
	}
}

func (s *streamFlusher) shutdownFlush(startSeq uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if startSeq == 0 {
		minSeq, err := s.initCheckpoints(ctx)
		if err != nil {
			return fmt.Errorf("init checkpoints on shutdown: %w", err)
		}
		startSeq = minSeq + 1
	}

	seq := startSeq
	for {
		records, err := s.log.Read(ctx, seq)
		if err != nil {
			if errors.Is(err, logstream.ErrEndOfStream) {
				return nil
			}
			return fmt.Errorf("drain log seq %d: %w", seq, err)
		}
		if err := s.processRecords(ctx, seq, records); err != nil {
			return err
		}
		seq++
	}
}

func (s *streamFlusher) processRecords(ctx context.Context, seq uint64, records []logstream.Record) error {
	branchMutations := make(map[string][]*storagepb.DocumentMutation)

	for _, rec := range records {
		var walRec storagepb.WalRecord
		if err := proto.Unmarshal(rec, &walRec); err != nil {
			return fmt.Errorf("unmarshal wal record at seq %d: %w", seq, err)
		}

		if evt := walRec.GetBranchEvent(); evt != nil {
			if evt.Type == storagepb.BranchLifecycleEvent_FORK {
				if muts := branchMutations[evt.ParentBranch]; len(muts) > 0 {
					if err := s.flushBranch(ctx, evt.ParentBranch, muts, seq); err != nil {
						return err
					}
					delete(branchMutations, evt.ParentBranch)
				}
				if lastSeq, ok := s.branchCheckpoints[evt.Branch]; !ok || seq > lastSeq {
					s.branchCheckpoints[evt.Branch] = seq
				}
			}
			continue
		}

		mut := walRec.GetMutation()
		if mut == nil || mut.Branch == "" {
			continue
		}

		if lastSeq, ok := s.branchCheckpoints[mut.Branch]; ok && seq <= lastSeq {
			continue
		}

		branchMutations[mut.Branch] = append(branchMutations[mut.Branch], mut)
	}

	for branch, muts := range branchMutations {
		if len(muts) > 0 {
			if err := s.flushBranch(ctx, branch, muts, seq); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *streamFlusher) flushBranch(ctx context.Context, branch string, mutations []*storagepb.DocumentMutation, seq uint64) error {
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
	if _, err := s.store.Put(ctx, segKey, bytes.NewReader(data), objectstore.Condition{Absent: true}); err != nil {
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
		m, gen, err := manifest.Read(ctx, s.store, branch)
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
		for _, item := range m.Segments {
			if item.SegmentId == segRef.SegmentId {
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

		_, err = manifest.Write(ctx, s.store, branch, m, gen)
		if err == nil {
			_ = scope.AddBranch(ctx, s.store, branch)
			if scope.BranchesPath() != namespace.BranchesFile {
				_ = namespace.AddBranch(ctx, s.store, "", branch)
			}
			s.branchCheckpoints[branch] = seq
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
