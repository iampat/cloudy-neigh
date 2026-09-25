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
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Loader struct {
	store   objectstore.Store
	table   *atomic.Pointer[Table]
	scope   namespace.Scope
	branch  string
	mu      sync.Mutex
	loaded  map[string]bool
	lastGen string
}

func NewLoader(store objectstore.Store, table *atomic.Pointer[Table], scope namespace.Scope, branch string) (*Loader, error) {
	if store == nil {
		return nil, errors.New("query: nil store")
	}
	if table == nil {
		return nil, errors.New("query: nil table")
	}
	return &Loader{
		store:  store,
		table:  table,
		scope:  scope,
		branch: branch,
		loaded: make(map[string]bool),
	}, nil
}

func (l *Loader) Sync(ctx context.Context) (int, error) {
	syncStart := time.Now()
	manifestKey := l.scope.ManifestKey(l.branch)
	manifest, gen, err := manifest.Read(ctx, l.store, manifestKey)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync manifest %s: %w", manifestKey, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if gen != "" && gen == l.lastGen {
		return 0, nil
	}

	var t *Table
	loadedCount := 0
	for _, seg := range manifest.Segments {
		if l.loaded[seg.SegmentId] {
			continue
		}
		if t == nil {
			t = l.table.Load().Clone()
		}
		segKey := l.scope.SegmentKey(seg.GetSegmentId())

		loadStart := time.Now()
		rc, _, err := l.store.Get(ctx, segKey)
		if err != nil {
			if loadedCount > 0 {
				l.table.Store(t)
			}
			return loadedCount, fmt.Errorf("get segment %s: %w", segKey, err)
		}

		reader := segment.NewReader(rc)
		count := 0
		var readErr error
		for {
			select {
			case <-ctx.Done():
				readErr = ctx.Err()
			default:
			}
			if readErr != nil {
				break
			}

			mut, err := reader.Next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = fmt.Errorf("read mutation from %s: %w", segKey, err)
				}
				break
			}
			switch mut.Op {
			case storagepb.MutationOp_PUT:
				var rec cloudyneighpb.Record
				if err := proto.Unmarshal(mut.Payload, &rec); err != nil {
					readErr = fmt.Errorf("unmarshal record from %s: %w", segKey, err)
					break
				}
				if err := t.UpsertRecord(&rec); err != nil {
					readErr = fmt.Errorf("upsert record %s from %s: %w", rec.Id, segKey, err)
					break
				}
			case storagepb.MutationOp_DELETE:
				t.Delete(mut.DocId)
			default:
				readErr = fmt.Errorf("unknown mutation op %v in %s", mut.Op, segKey)
				break
			}
			if readErr != nil {
				break
			}
			count++
		}
		rc.Close()
		if readErr != nil {
			if loadedCount > 0 {
				l.table.Store(t)
			}
			return loadedCount, readErr
		}

		slog.Debug("loaded segment",
			"manifest", manifestKey,
			"segment_id", seg.SegmentId,
			"records", count,
			"load_dur", time.Since(loadStart),
		)

		l.loaded[seg.SegmentId] = true
		loadedCount++
	}
	if t != nil {
		l.table.Store(t)
	}
	l.lastGen = gen

	if loadedCount > 0 {
		slog.Info("sync pass",
			"manifest", manifestKey,
			"segments", loadedCount,
			"total_dur", time.Since(syncStart),
		)
	}

	return loadedCount, nil
}
