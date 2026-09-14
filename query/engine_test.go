package query_test

import (
	"bytes"
	"context"
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

func writeEngineSegment(t *testing.T, ctx context.Context, store objectstore.Store, branch, segID string, mutations []*storagepb.DocumentMutation) {
	t.Helper()
	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, m := range mutations {
		require.NoError(t, w.Write(m))
	}
	require.NoError(t, w.Close())
	segKey := segment.Key(branch, segID)
	_, err := store.Put(ctx, segKey, bytes.NewReader(buf.Bytes()), objectstore.Condition{Absent: true})
	require.NoError(t, err)
}

func updateEngineManifest(t *testing.T, ctx context.Context, store objectstore.Store, branch string, segIDs []string, expectedGen string) string {
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
	gen, err := kvfs.UpdateBranch(ctx, store, branch, m, expectedGen)
	require.NoError(t, err)
	return gen
}

func TestEngine_NewEngine_Validation(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	tests := []struct {
		name         string
		store        objectstore.Store
		syncInterval time.Duration
	}{
		{
			name:         "nil store",
			store:        nil,
			syncInterval: time.Second,
		},
		{
			name:         "zero interval",
			store:        store,
			syncInterval: 0,
		},
		{
			name:         "negative interval",
			store:        store,
			syncInterval: -time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := query.NewEngine(tc.store, tc.syncInterval)
			require.Error(t, err)
		})
	}
}

func TestEngine_Query_UnknownBranch(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	eng, err := query.NewEngine(store, time.Second)
	require.NoError(t, err)

	hits, err := eng.Query(ctx, query.QueryRequest{
		Namespace: "nonexistent",
		Vector:    []float32{1.0, 2.0},
		TopK:      10,
	})
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestEngine_Query_SuccessAndDefaultColumn(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	payload, err := proto.Marshal(&cloudyneighpb.Record{
		Id: "doc-1",
		Vectors: map[string]*cloudyneighpb.Vector{
			"default": {Values: []float32{1.0, 0.0}},
		},
	})
	require.NoError(t, err)

	writeEngineSegment(t, ctx, store, "main", "seg-1", []*storagepb.DocumentMutation{
		{
			Branch:  "main",
			DocId:   "doc-1",
			Op:      storagepb.MutationOp_PUT,
			Payload: payload,
		},
	})
	updateEngineManifest(t, ctx, store, "main", []string{"seg-1"}, "")

	eng, err := query.NewEngine(store, 20*time.Millisecond)
	require.NoError(t, err)

	require.NoError(t, eng.SyncOnce(ctx))

	hits, err := eng.Query(ctx, query.QueryRequest{
		Namespace: "main",
		Vector:    []float32{1.0, 0.0},
		TopK:      10,
	})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "doc-1", hits[0].Record.Id)
}

func TestEngine_Run_Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	eng, err := query.NewEngine(store, 10*time.Millisecond)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	cancel()
	err = <-errCh
	require.NoError(t, err)
}
