package query

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Loader struct {
	store  objectstore.Store
	table  *Table
	mu     sync.Mutex
	loaded map[string]bool
}

func NewLoader(store objectstore.Store, table *Table) (*Loader, error) {
	if store == nil {
		return nil, errors.New("query: nil store")
	}
	if table == nil {
		return nil, errors.New("query: nil table")
	}
	return &Loader{
		store:  store,
		table:  table,
		loaded: make(map[string]bool),
	}, nil
}

func (l *Loader) Sync(ctx context.Context, branch string) (int, error) {
	manifest, _, err := kvfs.ResolveBranch(ctx, l.store, branch)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync branch %s: %w", branch, err)
	}

	loadedCount := 0
	for _, seg := range manifest.Segments {
		l.mu.Lock()
		loaded := l.loaded[seg.SegmentId]
		l.mu.Unlock()
		if loaded {
			continue
		}
		if err := l.loadSegment(ctx, branch, seg.SegmentId); err != nil {
			return loadedCount, err
		}
		l.mu.Lock()
		l.loaded[seg.SegmentId] = true
		l.mu.Unlock()
		loadedCount++
	}
	return loadedCount, nil
}

func (l *Loader) loadSegment(ctx context.Context, branch, segID string) error {
	segKey := segment.Key(branch, segID)
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

		switch mut.Op {
		case storagepb.MutationOp_PUT:
			var rec cloudyneighpb.Record
			if err := proto.Unmarshal(mut.Payload, &rec); err != nil {
				return fmt.Errorf("unmarshal record from %s: %w", segKey, err)
			}
			if err := l.table.UpsertRecord(&rec); err != nil {
				return fmt.Errorf("upsert record %s from %s: %w", rec.Id, segKey, err)
			}
		case storagepb.MutationOp_DELETE:
			l.table.Delete(mut.DocId)
		default:
			return fmt.Errorf("unknown mutation op %v in %s", mut.Op, segKey)
		}
	}
	return nil
}
