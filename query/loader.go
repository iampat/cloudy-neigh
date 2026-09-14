package query

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
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

func (l *Loader) Sync(ctx context.Context, branch string) (int, error) {
	syncStart := time.Now()
	manifest, gen, err := kvfs.ResolveBranch(ctx, l.store, branch)
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
	segKey := segment.RefKey(branch, seg)

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
	var muts []*storagepb.DocumentMutation
	for {
		mut, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read mutation from %s: %w", segKey, err)
		}
		muts = append(muts, mut)
	}
	type decodedMut struct {
		op    storagepb.MutationOp
		docID string
		rec   *cloudyneighpb.Record
	}
	decoded := make([]decodedMut, 0, len(muts))
	for _, mut := range muts {
		switch mut.Op {
		case storagepb.MutationOp_PUT:
			var rec cloudyneighpb.Record
			if err := proto.Unmarshal(mut.Payload, &rec); err != nil {
				return fmt.Errorf("unmarshal record from %s: %w", segKey, err)
			}
			decoded = append(decoded, decodedMut{op: mut.Op, docID: mut.DocId, rec: &rec})
		case storagepb.MutationOp_DELETE:
			decoded = append(decoded, decodedMut{op: mut.Op, docID: mut.DocId})
		default:
			return fmt.Errorf("unknown mutation op %v in %s", mut.Op, segKey)
		}
	}
	decodeDur := time.Since(decodeStart)

	applyStart := time.Now()
	for _, d := range decoded {
		switch d.op {
		case storagepb.MutationOp_PUT:
			if err := b.UpsertRecord(d.rec); err != nil {
				return fmt.Errorf("upsert record %s from %s: %w", d.rec.Id, segKey, err)
			}
		case storagepb.MutationOp_DELETE:
			b.Delete(d.docID)
		}
	}
	applyDur := time.Since(applyStart)

	slog.Debug("loaded segment",
		"branch", branch,
		"segment_id", seg.SegmentId,
		"records", len(muts),
		"fetch_dur", fetchDur,
		"decode_dur", decodeDur,
		"apply_dur", applyDur,
	)

	return nil
}
