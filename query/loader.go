package query

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Loader struct {
	store   objectstore.Store
	table   *atomic.Pointer[Table]
	mu      sync.Mutex
	loaded  map[string]bool
	lastGen string
}

func NewLoader(store objectstore.Store, table *atomic.Pointer[Table]) (*Loader, error) {
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

func (l *Loader) Table() *Table {
	return l.table.Load()
}

func (l *Loader) Sync(ctx context.Context, branch string) (int, error) {
	syncStart := time.Now()
	manifest, gen, err := manifest.Read(ctx, l.store, branch)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync branch %s: %w", branch, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if gen != "" && gen == l.lastGen {
		return 0, nil
	}

	var b *Builder
	loadedCount := 0
	for _, seg := range manifest.Segments {
		if l.loaded[seg.SegmentId] {
			continue
		}
		if b == nil {
			b = l.table.Load().Builder()
		}
		if err := l.loadSegment(ctx, branch, seg, b); err != nil {
			if loadedCount > 0 {
				l.table.Store(b.Build())
			}
			return loadedCount, err
		}
		l.loaded[seg.SegmentId] = true
		loadedCount++
	}
	if b != nil {
		l.table.Store(b.Build())
	}
	l.lastGen = gen

	if loadedCount > 0 {
		slog.Info("sync pass",
			"branch", branch,
			"segments", loadedCount,
			"total_dur", time.Since(syncStart),
		)
	}

	return loadedCount, nil
}

func (l *Loader) loadSegment(ctx context.Context, branch string, seg *storagepb.SegmentRef, b *Builder) error {
	segKey := seg.GetKey()
	if segKey == "" {
		return fmt.Errorf("query: segment ref missing key: %s", seg.GetSegmentId())
	}

	loadStart := time.Now()
	rc, _, err := l.store.Get(ctx, segKey)
	if err != nil {
		return fmt.Errorf("get segment %s: %w", segKey, err)
	}
	defer rc.Close()

	reader := segment.NewReader(rc)
	count := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

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
			if err := b.UpsertRecord(&rec); err != nil {
				return fmt.Errorf("upsert record %s from %s: %w", rec.Id, segKey, err)
			}
		case storagepb.MutationOp_DELETE:
			b.Delete(mut.DocId)
		default:
			return fmt.Errorf("unknown mutation op %v in %s", mut.Op, segKey)
		}
		count++
	}

	slog.Debug("loaded segment",
		"branch", branch,
		"segment_id", seg.SegmentId,
		"records", count,
		"load_dur", time.Since(loadStart),
	)

	return nil
}
