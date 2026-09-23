package query_test

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/query"
	"github.com/iampat/cloudy-neigh/segment"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func writeSegment(t *testing.T, store objectstore.Store, branch, segID string, mutations []*storagepb.DocumentMutation) {
	t.Helper()
	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, m := range mutations {
		require.NoError(t, w.Write(m))
	}
	require.NoError(t, w.Close())
	segKey := segment.Key(segID)
	_, err := store.Put(context.Background(), segKey, bytes.NewReader(buf.Bytes()), objectstore.Condition{Absent: true})
	require.NoError(t, err)
}

func putMutation(t *testing.T, branch, id string, vec []float32, attrs map[string]*cloudyneighpb.AttributeValue) *storagepb.DocumentMutation {
	t.Helper()
	var vectors map[string]*cloudyneighpb.Vector
	if len(vec) > 0 {
		vectors = map[string]*cloudyneighpb.Vector{
			"default": {Values: vec},
		}
	}
	rec := &cloudyneighpb.Record{
		Id:         id,
		Vectors:    vectors,
		Attributes: attrs,
	}
	payload, err := proto.Marshal(rec)
	require.NoError(t, err)
	return &storagepb.DocumentMutation{
		Branch:  branch,
		DocId:   id,
		Op:      storagepb.MutationOp_PUT,
		Payload: payload,
	}
}

func deleteMutation(branch, id string) *storagepb.DocumentMutation {
	return &storagepb.DocumentMutation{
		Branch: branch,
		DocId:  id,
		Op:     storagepb.MutationOp_DELETE,
	}
}

func updateManifest(t *testing.T, store objectstore.Store, branch string, segIDs []string, expectedGen string) string {
	t.Helper()
	var segs []*storagepb.SegmentRef
	for _, id := range segIDs {
		segs = append(segs, &storagepb.SegmentRef{
			SegmentId: id,
			Key:       segment.Key(id),
		})
	}
	m := &storagepb.BranchManifest{
		SchemaVersion: 1,
		Segments:      segs,
	}
	gen, err := manifest.Write(context.Background(), store, branch, m, expectedGen)
	require.NoError(t, err)
	return gen
}

func TestLoader_SyncAndDeduplication(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0, 2.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc1")}),
		putMutation(t, "main", "doc-2", []float32{3.0, 4.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc2")}),
	})
	gen := updateManifest(t, store, "main", []string{"seg-1"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	rec1, ok := table.Load().Get("doc-1")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc1"), rec1.Attributes["title"]))
	require.Equal(t, []float32{1.0, 2.0}, rec1.Vectors["default"].Values)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 0, loaded)

	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", nil, map[string]*cloudyneighpb.AttributeValue{"category": stringAttr("tech")}),
		deleteMutation("main", "doc-2"),
		putMutation(t, "main", "doc-3", []float32{5.0, 6.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc3")}),
	})
	updateManifest(t, store, "main", []string{"seg-1", "seg-2"}, gen)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	rec1, ok = table.Load().Get("doc-1")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc1"), rec1.Attributes["title"]))
	require.True(t, proto.Equal(stringAttr("tech"), rec1.Attributes["category"]))
	require.Equal(t, []float32{1.0, 2.0}, rec1.Vectors["default"].Values)

	_, ok = table.Load().Get("doc-2")
	require.False(t, ok)

	rec3, ok := table.Load().Get("doc-3")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc3"), rec3.Attributes["title"]))

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 0, loaded)
}

func TestLoader_UnknownMutationOp(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-unknown", []*storagepb.DocumentMutation{
		{
			Branch: "main",
			DocId:  "doc-x",
			Op:     storagepb.MutationOp(999),
		},
	})
	updateManifest(t, store, "main", []string{"seg-unknown"}, "")

	_, err = loader.Sync(ctx, "main")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown mutation op")
}

func TestLoader_EmptyBranch(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	loaded, err := loader.Sync(ctx, "nonexistent")
	require.NoError(t, err)
	require.Equal(t, 0, loaded)
}

func TestLoader_Validation(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())

	_, err = query.NewLoader(nil, &table)
	require.Error(t, err)

	_, err = query.NewLoader(store, nil)
	require.Error(t, err)

	_, err = query.NewLoader(store, &table)
	require.NoError(t, err)
}

func TestLoader_DeleteTombstones(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0}, map[string]*cloudyneighpb.AttributeValue{"k": stringAttr("v1")}),
		putMutation(t, "main", "doc-2", []float32{2.0}, map[string]*cloudyneighpb.AttributeValue{"k": stringAttr("v2")}),
	})
	gen := updateManifest(t, store, "main", []string{"seg-1"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	rec1, ok := table.Load().Get("doc-1")
	require.True(t, ok)
	require.Equal(t, "doc-1", rec1.Id)

	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		deleteMutation("main", "doc-1"),
		deleteMutation("main", "doc-1"),
		deleteMutation("main", "nonexistent"),
	})
	gen = updateManifest(t, store, "main", []string{"seg-1", "seg-2"}, gen)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	_, ok = table.Load().Get("doc-1")
	require.False(t, ok)

	rec2, ok := table.Load().Get("doc-2")
	require.True(t, ok)
	require.Equal(t, "doc-2", rec2.Id)

	writeSegment(t, store, "main", "seg-3", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{10.0}, map[string]*cloudyneighpb.AttributeValue{"extra": stringAttr("e")}),
	})
	gen = updateManifest(t, store, "main", []string{"seg-1", "seg-2", "seg-3"}, gen)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	rec1, ok = table.Load().Get("doc-1")
	require.True(t, ok)
	require.Equal(t, []float32{10.0}, rec1.Vectors["default"].Values)
	require.Nil(t, rec1.Attributes["k"])
	require.True(t, proto.Equal(stringAttr("e"), rec1.Attributes["extra"]))

	writeSegment(t, store, "main", "seg-4", []*storagepb.DocumentMutation{
		deleteMutation("main", "doc-1"),
	})
	updateManifest(t, store, "main", []string{"seg-1", "seg-2", "seg-3", "seg-4"}, gen)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	_, ok = table.Load().Get("doc-1")
	require.False(t, ok)
}

func TestLoader_VectorDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0, 2.0}, nil),
	})
	gen := updateManifest(t, store, "main", []string{"seg-1"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-2", []float32{1.0, 2.0, 3.0}, nil),
	})
	updateManifest(t, store, "main", []string{"seg-1", "seg-2"}, gen)

	loaded, err = loader.Sync(ctx, "main")
	require.Error(t, err)
	require.ErrorIs(t, err, query.ErrDimensionMismatch)
	require.Equal(t, 0, loaded)

	_, ok := table.Load().Get("doc-2")
	require.False(t, ok)
}

func TestLoader_GenerationSkip(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0}, map[string]*cloudyneighpb.AttributeValue{"k": stringAttr("v1")}),
	})
	updateManifest(t, store, "main", []string{"seg-1"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	segKey := segment.Key("seg-1")
	err = store.Delete(ctx, segKey)
	require.NoError(t, err)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 0, loaded)
}

func TestLoader_GenerationAdvancesOnlyOnCleanPass(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0}, nil),
	})
	segBadKey := segment.Key("seg-bad")
	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	require.NoError(t, w.Write(&storagepb.DocumentMutation{
		Branch:  "main",
		DocId:   "doc-bad",
		Op:      storagepb.MutationOp_PUT,
		Payload: []byte("not-a-proto"),
	}))
	require.NoError(t, w.Close())
	_, err = store.Put(ctx, segBadKey, bytes.NewReader(buf.Bytes()), objectstore.Condition{Absent: true})
	require.NoError(t, err)

	_ = updateManifest(t, store, "main", []string{"seg-1", "seg-bad"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.Error(t, err)
	require.Equal(t, 1, loaded)

	err = store.Delete(ctx, segBadKey)
	require.NoError(t, err)
	writeSegment(t, store, "main", "seg-bad", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-bad", []float32{2.0}, nil),
	})

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	rec, ok := table.Load().Get("doc-bad")
	require.True(t, ok)
	require.Equal(t, "doc-bad", rec.Id)
}

func TestLoader_ReplayOrder(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0}, map[string]*cloudyneighpb.AttributeValue{"val": stringAttr("first")}),
		putMutation(t, "main", "doc-2", []float32{2.0}, nil),
	})
	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{10.0}, map[string]*cloudyneighpb.AttributeValue{"val": stringAttr("second")}),
		deleteMutation("main", "doc-2"),
	})
	updateManifest(t, store, "main", []string{"seg-1", "seg-2"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 2, loaded)

	rec1, ok := table.Load().Get("doc-1")
	require.True(t, ok)
	require.Equal(t, []float32{10.0}, rec1.Vectors["default"].Values)
	require.True(t, proto.Equal(stringAttr("second"), rec1.Attributes["val"]))

	_, ok = table.Load().Get("doc-2")
	require.False(t, ok)
}

func TestLoader_ConcurrentSync(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	var segIDs []string
	for i := 1; i <= 6; i++ {
		segID := fmt.Sprintf("seg-%d", i)
		segIDs = append(segIDs, segID)
		writeSegment(t, store, "main", segID, []*storagepb.DocumentMutation{
			putMutation(t, "main", fmt.Sprintf("doc-%d", i), []float32{float32(i)}, nil),
		})
	}
	updateManifest(t, store, "main", segIDs, "")

	const goroutines = 8
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			_, err := loader.Sync(ctx, "main")
			errCh <- err
		}()
	}

	for g := 0; g < goroutines; g++ {
		require.NoError(t, <-errCh)
	}

	for i := 1; i <= 6; i++ {
		rec, ok := table.Load().Get(fmt.Sprintf("doc-%d", i))
		require.True(t, ok)
		require.Equal(t, fmt.Sprintf("doc-%d", i), rec.Id)
	}
}

func TestLoader_SnapshotIsolationAcrossSync(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0, 2.0}, map[string]*cloudyneighpb.AttributeValue{"v": stringAttr("initial")}),
		putMutation(t, "main", "doc-2", []float32{3.0, 4.0}, nil),
	})
	gen := updateManifest(t, store, "main", []string{"seg-1"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)

	oldSnap := table.Load()

	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{10.0, 20.0}, map[string]*cloudyneighpb.AttributeValue{"v": stringAttr("updated")}),
		deleteMutation("main", "doc-2"),
		putMutation(t, "main", "doc-3", []float32{5.0, 6.0}, nil),
	})
	updateManifest(t, store, "main", []string{"seg-1", "seg-2"}, gen)

	const readers = 4
	const iters = 50
	errCh := make(chan error, readers+1)

	for r := 0; r < readers; r++ {
		go func() {
			for i := 0; i < iters; i++ {
				rec1, ok := oldSnap.Get("doc-1")
				if !ok || rec1.Attributes["v"].GetStringValue() != "initial" {
					errCh <- fmt.Errorf("doc-1 corrupted on old snapshot")
					return
				}
				if rec1.Vectors["default"].Values[0] != 1.0 {
					errCh <- fmt.Errorf("doc-1 vector corrupted on old snapshot")
					return
				}
				if _, ok := oldSnap.Get("doc-2"); !ok {
					errCh <- fmt.Errorf("doc-2 should still exist on old snapshot")
					return
				}
				if _, ok := oldSnap.Get("doc-3"); ok {
					errCh <- fmt.Errorf("doc-3 should not be visible on old snapshot")
					return
				}
			}
			errCh <- nil
		}()
	}

	go func() {
		_, err := loader.Sync(ctx, "main")
		errCh <- err
	}()

	for i := 0; i < readers+1; i++ {
		require.NoError(t, <-errCh)
	}

	newSnap := table.Load()
	require.NotEqual(t, oldSnap, newSnap)

	rec1, ok := newSnap.Get("doc-1")
	require.True(t, ok)
	require.Equal(t, "updated", rec1.Attributes["v"].GetStringValue())
	require.Equal(t, float32(10.0), rec1.Vectors["default"].Values[0])

	_, ok = newSnap.Get("doc-2")
	require.False(t, ok)

	_, ok = newSnap.Get("doc-3")
	require.True(t, ok)
}

func appendWALRecord(t *testing.T, ctx context.Context, log *logstream.Log, branch, docID string, vec []float32, attrs map[string]*cloudyneighpb.AttributeValue) {
	t.Helper()
	var vectors map[string]*cloudyneighpb.Vector
	if len(vec) > 0 {
		vectors = map[string]*cloudyneighpb.Vector{
			"default": {Values: vec},
		}
	}
	rec := &cloudyneighpb.Record{
		Id:         docID,
		Vectors:    vectors,
		Attributes: attrs,
	}
	payload, err := proto.Marshal(rec)
	require.NoError(t, err)

	walRec := &storagepb.WalRecord{
		Record: &storagepb.WalRecord_Mutation{
			Mutation: &storagepb.DocumentMutation{
				Branch:  branch,
				DocId:   docID,
				Op:      storagepb.MutationOp_PUT,
				Payload: payload,
			},
		},
	}
	recBytes, err := proto.Marshal(walRec)
	require.NoError(t, err)

	_, err = log.Append(ctx, []logstream.Record{recBytes})
	require.NoError(t, err)
}

func flushBranch(t *testing.T, ctx context.Context, store objectstore.Store, branch string, expectedSegCount int) {
	t.Helper()
	flusher, err := ingest.NewFlusher(store, ingest.Config{
		PollInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	flushCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- flusher.Run(flushCtx)
	}()

	ticker := time.NewTicker(5 * time.Millisecond)
	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled waiting for branch %s: %v", branch, ctx.Err())
		case <-timeout:
			t.Fatalf("timed out waiting for branch %s manifest", branch)
		case <-ticker.C:
			m, _, err := manifest.Read(ctx, store, branch)
			if err == nil && len(m.Segments) >= expectedSegCount {
				cancel()
				require.NoError(t, <-errCh)
				return
			}
		}
	}
}

func TestLoader_ForkBranch_Inheritance(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	mainBranch := namespace.BranchRef("", "", "main")
	stagingBranch := namespace.BranchRef("", "", "staging")

	log, err := logstream.New(store, namespace.Scope{}.WALPrefix())
	require.NoError(t, err)

	appendWALRecord(t, ctx, log, mainBranch, "doc-1", []float32{1.0, 0.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc1")})
	appendWALRecord(t, ctx, log, mainBranch, "doc-2", []float32{0.0, 1.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc2")})

	flushBranch(t, ctx, store, mainBranch, 2)

	parentM, _, err := manifest.Read(ctx, store, mainBranch)
	require.NoError(t, err)
	_, err = manifest.Write(ctx, store, stagingBranch, parentM, "")
	require.NoError(t, err)

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	loaded, err := loader.Sync(ctx, stagingBranch)
	require.NoError(t, err)
	require.Equal(t, 2, loaded)

	snap := table.Load()
	rec1, ok := snap.Get("doc-1")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc1"), rec1.Attributes["title"]))

	rec2, ok := snap.Get("doc-2")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc2"), rec2.Attributes["title"]))

	hits, _, err := snap.Search("default", []float32{1.0, 0.0}, 2, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "doc-1", hits[0].Record.Id)
}

func TestLoader_ForkBranch_Divergence(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	log, err := logstream.New(store, namespace.Scope{}.WALPrefix())
	require.NoError(t, err)

	mainBranch := namespace.BranchRef("", "", "main")
	stagingBranch := namespace.BranchRef("", "", "staging")

	appendWALRecord(t, ctx, log, mainBranch, "doc-1", []float32{1.0, 0.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc1")})
	flushBranch(t, ctx, store, mainBranch, 1)

	parentM, _, err := manifest.Read(ctx, store, mainBranch)
	require.NoError(t, err)
	_, err = manifest.Write(ctx, store, stagingBranch, parentM, "")
	require.NoError(t, err)

	appendWALRecord(t, ctx, log, stagingBranch, "doc-2", []float32{0.0, 1.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc2")})
	flushBranch(t, ctx, store, stagingBranch, 2)

	var stagingTable atomic.Pointer[query.Table]
	stagingTable.Store(query.NewTable())
	stagingLoader, err := query.NewLoader(store, &stagingTable)
	require.NoError(t, err)

	loaded, err := stagingLoader.Sync(ctx, stagingBranch)
	require.NoError(t, err)
	require.Equal(t, 2, loaded)

	stagingSnap := stagingTable.Load()
	_, ok1 := stagingSnap.Get("doc-1")
	require.True(t, ok1)
	_, ok2 := stagingSnap.Get("doc-2")
	require.True(t, ok2)

	var mainTable atomic.Pointer[query.Table]
	mainTable.Store(query.NewTable())
	mainLoader, err := query.NewLoader(store, &mainTable)
	require.NoError(t, err)

	loadedMain, err := mainLoader.Sync(ctx, mainBranch)
	require.NoError(t, err)
	require.Equal(t, 1, loadedMain)

	mainSnap := mainTable.Load()
	_, okMain1 := mainSnap.Get("doc-1")
	require.True(t, okMain1)
	_, okMain2 := mainSnap.Get("doc-2")
	require.False(t, okMain2)
}

func TestLoader_ManifestMissingKeyError(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	writeSegment(t, store, "main", "seg-legacy", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-legacy", []float32{1.0, 2.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("legacy")}),
	})

	m := &storagepb.BranchManifest{
		SchemaVersion: 1,
		Segments: []*storagepb.SegmentRef{
			{
				SegmentId: "seg-legacy",
				Key:       "",
			},
		},
	}
	_, err = manifest.Write(ctx, store, "main", m, "")
	require.NoError(t, err)

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	_, err = loader.Sync(ctx, "main")
	require.Error(t, err)
}

func TestLoader_BatchSegmentLoading(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var table atomic.Pointer[query.Table]
	table.Store(query.NewTable())
	loader, err := query.NewLoader(store, &table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-1", []float32{1.0, 0.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc1")}),
		putMutation(t, "main", "doc-2", []float32{0.0, 1.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc2")}),
	})
	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		putMutation(t, "main", "doc-3", []float32{0.6, 0.8}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc3")}),
		putMutation(t, "main", "doc-1", []float32{1.0, 0.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc1-v2"), "tag": stringAttr("updated")}),
	})
	writeSegment(t, store, "main", "seg-3", []*storagepb.DocumentMutation{
		deleteMutation("main", "doc-2"),
		putMutation(t, "main", "doc-4", []float32{0.0, -1.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc4")}),
	})

	updateManifest(t, store, "main", []string{"seg-1", "seg-2", "seg-3"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 3, loaded)

	snap := table.Load()

	rec1, ok := snap.Get("doc-1")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc1-v2"), rec1.Attributes["title"]))
	require.True(t, proto.Equal(stringAttr("updated"), rec1.Attributes["tag"]))

	_, ok = snap.Get("doc-2")
	require.False(t, ok)

	rec3, ok := snap.Get("doc-3")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc3"), rec3.Attributes["title"]))

	rec4, ok := snap.Get("doc-4")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc4"), rec4.Attributes["title"]))

	hits, _, err := snap.Search("default", []float32{1.0, 0.0}, 5, cloudyneighpb.DistanceMetric_DISTANCE_METRIC_COSINE, nil)
	require.NoError(t, err)
	require.Len(t, hits, 3)
	require.Equal(t, "doc-1", hits[0].Record.Id)
}
