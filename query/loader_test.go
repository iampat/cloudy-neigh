package query_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/kvfs"
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
	segKey := fmt.Sprintf("segments/%s/%s.recordio", branch, segID)
	_, err := store.Put(context.Background(), segKey, bytes.NewReader(buf.Bytes()), objectstore.Condition{Absent: true})
	require.NoError(t, err)
}

func putMutation(branch, id string, vec []float32, attrs map[string]*cloudyneighpb.AttributeValue) *storagepb.DocumentMutation {
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
	if err != nil {
		panic(err)
	}
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
		})
	}
	m := &storagepb.BranchManifest{
		SchemaVersion: 1,
		Segments:      segs,
	}
	gen, err := kvfs.UpdateBranch(context.Background(), store, branch, m, expectedGen)
	require.NoError(t, err)
	return gen
}

func TestLoader_SyncAndDeduplication(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	table := query.NewTable(0)
	loader, err := query.NewLoader(store, table)
	require.NoError(t, err)

	writeSegment(t, store, "main", "seg-1", []*storagepb.DocumentMutation{
		putMutation("main", "doc-1", []float32{1.0, 2.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc1")}),
		putMutation("main", "doc-2", []float32{3.0, 4.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc2")}),
	})
	gen := updateManifest(t, store, "main", []string{"seg-1"}, "")

	loaded, err := loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)
	require.True(t, loader.IsLoaded("seg-1"))
	require.Equal(t, 2, table.Len())

	rec1, ok := table.Get("doc-1")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc1"), rec1.Attributes["title"]))
	require.Equal(t, []float32{1.0, 2.0}, rec1.Vectors["default"].Values)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 0, loaded)
	require.Equal(t, 2, table.Len())

	writeSegment(t, store, "main", "seg-2", []*storagepb.DocumentMutation{
		putMutation("main", "doc-1", nil, map[string]*cloudyneighpb.AttributeValue{"category": stringAttr("tech")}),
		deleteMutation("main", "doc-2"),
		putMutation("main", "doc-3", []float32{5.0, 6.0}, map[string]*cloudyneighpb.AttributeValue{"title": stringAttr("doc3")}),
	})
	updateManifest(t, store, "main", []string{"seg-1", "seg-2"}, gen)

	loaded, err = loader.Sync(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, 1, loaded)
	require.True(t, loader.IsLoaded("seg-2"))
	require.Equal(t, 2, table.Len())

	rec1, ok = table.Get("doc-1")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("doc1"), rec1.Attributes["title"]))
	require.True(t, proto.Equal(stringAttr("tech"), rec1.Attributes["category"]))
	require.Equal(t, []float32{1.0, 2.0}, rec1.Vectors["default"].Values)

	_, ok = table.Get("doc-2")
	require.False(t, ok)

	rec3, ok := table.Get("doc-3")
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

	table := query.NewTable(0)
	loader, err := query.NewLoader(store, table)
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

	table := query.NewTable(0)
	loader, err := query.NewLoader(store, table)
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

	table := query.NewTable(0)

	_, err = query.NewLoader(nil, table)
	require.Error(t, err)

	_, err = query.NewLoader(store, nil)
	require.Error(t, err)

	loader, err := query.NewLoader(store, table)
	require.NoError(t, err)

	_, err = loader.Sync(ctx, "invalid/branch/name")
	require.Error(t, err)

	err = loader.Run(ctx, "invalid/branch/name", 10*time.Millisecond)
	require.Error(t, err)

	err = loader.Run(ctx, "main", 0)
	require.Error(t, err)

	err = loader.Run(ctx, "main", -1*time.Millisecond)
	require.Error(t, err)
}

func TestLoader_Run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	table := query.NewTable(0)
	loader, err := query.NewLoader(store, table)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- loader.Run(ctx, "main", 10*time.Millisecond)
	}()

	writeSegment(t, store, "main", "seg-bg", []*storagepb.DocumentMutation{
		putMutation("main", "doc-bg", []float32{9.9}, map[string]*cloudyneighpb.AttributeValue{"name": stringAttr("background")}),
	})
	updateManifest(t, store, "main", []string{"seg-bg"}, "")

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(3 * time.Second)

	for {
		if loader.IsLoaded("seg-bg") {
			break
		}
		select {
		case <-timeout:
			t.Fatal("timed out waiting for background loader to sync segment")
		case <-ticker.C:
		}
	}

	require.Equal(t, 1, table.Len())
	rec, ok := table.Get("doc-bg")
	require.True(t, ok)
	require.True(t, proto.Equal(stringAttr("background"), rec.Attributes["name"]))

	cancel()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for loader.Run to exit")
	}
}
