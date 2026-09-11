package query

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Loader struct {
	store    objectstore.Store
	table    *Table
	mu       sync.Mutex
	loaded   map[string]bool
	inFlight map[string]bool
}

func NewLoader(store objectstore.Store, table *Table) (*Loader, error) {
	if store == nil {
		return nil, errors.New("query: nil store")
	}
	if table == nil {
		return nil, errors.New("query: nil table")
	}
	return &Loader{
		store:    store,
		table:    table,
		loaded:   make(map[string]bool),
		inFlight: make(map[string]bool),
	}, nil
}

func (l *Loader) Table() *Table {
	return l.table
}

func (l *Loader) IsLoaded(segmentID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loaded[segmentID]
}

func (l *Loader) LoadedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.loaded)
}

func (l *Loader) Sync(ctx context.Context, branch string) (int, error) {
	manifest, _, err := kvfs.ResolveBranch(ctx, l.store, branch)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync branch %s: %w", branch, err)
	}

	l.mu.Lock()
	var toLoad []*storagepb.SegmentRef
	for _, seg := range manifest.Segments {
		if !l.loaded[seg.SegmentId] && !l.inFlight[seg.SegmentId] {
			l.inFlight[seg.SegmentId] = true
			toLoad = append(toLoad, seg)
		}
	}
	l.mu.Unlock()

	loadedCount := 0
	for _, seg := range toLoad {
		err := l.loadSegment(ctx, branch, seg.SegmentId)
		l.mu.Lock()
		delete(l.inFlight, seg.SegmentId)
		if err == nil {
			l.loaded[seg.SegmentId] = true
			loadedCount++
		}
		l.mu.Unlock()

		if err != nil {
			return loadedCount, err
		}
	}

	return loadedCount, nil
}

func (l *Loader) loadSegment(ctx context.Context, branch, segID string) error {
	segKey := fmt.Sprintf("segments/%s/%s.recordio", branch, segID)
	rc, _, err := l.store.Get(ctx, segKey)
	if err != nil {
		return fmt.Errorf("get segment %s: %w", segKey, err)
	}
	defer rc.Close()

	reader := segment.NewReader(rc)
	for {
		mut, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read mutation from %s: %w", segKey, err)
		}
		if mut == nil {
			continue
		}

		switch mut.Op {
		case storagepb.MutationOp_PUT:
			var doc cloudyneighpb.Document
			if err := proto.Unmarshal(mut.Payload, &doc); err != nil {
				return fmt.Errorf("unmarshal document from %s: %w", segKey, err)
			}
			if err := l.table.UpsertDoc(&doc); err != nil {
				return fmt.Errorf("upsert document %s from %s: %w", doc.Id, segKey, err)
			}
		case storagepb.MutationOp_DELETE:
			l.table.Delete(mut.DocId)
		}
	}
	return nil
}

func (l *Loader) Run(ctx context.Context, branch string, pollInterval time.Duration) error {
	if err := namespace.ValidateBranch(branch); err != nil {
		return err
	}
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if _, err := l.Sync(ctx, branch); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ctx.Err()
			}
			slog.WarnContext(ctx, "manifest loader sync failed", "branch", branch, "err", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
