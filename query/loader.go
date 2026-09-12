package query

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

type Loader struct {
	store   objectstore.Store
	table   *Table
	mu      sync.Mutex
	loaded  map[string]bool
	lastGen string
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
	syncStart := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	manifest, gen, err := kvfs.ResolveBranch(ctx, l.store, branch)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync branch %s: %w", branch, err)
	}

	if gen != "" && gen == l.lastGen {
		return 0, nil
	}

	loadedCount := 0
	for _, seg := range manifest.Segments {
		if l.loaded[seg.SegmentId] {
			continue
		}
		if err := l.loadSegment(ctx, branch, seg.SegmentId); err != nil {
			return loadedCount, err
		}
		l.loaded[seg.SegmentId] = true
		loadedCount++
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

func (l *Loader) loadSegment(ctx context.Context, branch, segID string) error {
	segKey := segment.Key(branch, segID)

	fetchStart := time.Now()
	rc, _, err := l.store.Get(ctx, segKey)
	if err != nil {
		return fmt.Errorf("get segment %s: %w", segKey, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read segment %s: %w", segKey, err)
	}
	fetchDur := time.Since(fetchStart)

	decodeStart := time.Now()
	reader := segment.NewReader(bytes.NewReader(data))
	var muts []Mutation
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
			muts = append(muts, Mutation{Op: OpUpsert, Record: &rec})
		case storagepb.MutationOp_DELETE:
			muts = append(muts, Mutation{Op: OpDelete, ID: mut.DocId})
		default:
			return fmt.Errorf("unknown mutation op %v in %s", mut.Op, segKey)
		}
	}
	decodeDur := time.Since(decodeStart)

	applyStart := time.Now()
	if err := l.table.ApplyMutations(muts); err != nil {
		return fmt.Errorf("apply mutations from %s: %w", segKey, err)
	}
	applyDur := time.Since(applyStart)

	slog.Debug("loaded segment",
		"branch", branch,
		"segment_id", segID,
		"records", len(muts),
		"fetch_dur", fetchDur,
		"decode_dur", decodeDur,
		"apply_dur", applyDur,
	)

	return nil
}
