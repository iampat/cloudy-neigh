package grpcapi_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/grpcapi"
	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/segment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func writeSegment(t *testing.T, ctx context.Context, store objectstore.Store, branch, segID string, mutations []*storagepb.DocumentMutation) {
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

func updateManifest(t *testing.T, ctx context.Context, store objectstore.Store, branch string, segIDs []string, expectedGen string) string {
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

func setupQueryTestEnv(t *testing.T, store objectstore.Store) (cloudyneighpb.QueryServiceClient, *grpcapi.QueryServer) {
	t.Helper()
	srv, err := grpcapi.NewQueryServer(store, 20*time.Millisecond)
	require.NoError(t, err)

	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	cloudyneighpb.RegisterQueryServiceServer(s, srv)

	go func() {
		_ = s.Serve(lis)
	}()
	t.Cleanup(func() {
		s.GracefulStop()
		lis.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return cloudyneighpb.NewQueryServiceClient(conn), srv
}

func TestQuery_NewQueryServer_Validation(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	_, err = grpcapi.NewQueryServer(nil, time.Second)
	require.Error(t, err)

	_, err = grpcapi.NewQueryServer(store, 0)
	require.Error(t, err)

	_, err = grpcapi.NewQueryServer(store, -time.Second)
	require.Error(t, err)
}

func TestQuery_NilRequest(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	_, srv := setupQueryTestEnv(t, store)
	_, err = srv.Query(ctx, nil)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestQuery_UnknownNamespace(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	client, _ := setupQueryTestEnv(t, store)

	resp, err := client.Query(ctx, &cloudyneighpb.QueryRequest{
		Namespace: "nonexistent",
		Vector:    []float32{1.0, 2.0},
		TopK:      10,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Hits)
}

func TestQuery_Validation(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	client, querySrv := setupQueryTestEnv(t, store)

	writeSegment(t, ctx, store, "main", "seg-1", []*storagepb.DocumentMutation{
		{
			Branch: "main",
			DocId:  "doc-1",
			Op:     storagepb.MutationOp_PUT,
			Payload: func() []byte {
				b, _ := proto.Marshal(&cloudyneighpb.Record{
					Id: "doc-1",
					Vectors: map[string]*cloudyneighpb.Vector{
						"default": {Values: []float32{1.0, 2.0, 3.0}},
					},
				})
				return b
			}(),
		},
	})
	updateManifest(t, ctx, store, "main", []string{"seg-1"}, "")
	require.NoError(t, querySrv.SyncOnce(ctx))

	tests := []struct {
		name string
		req  *cloudyneighpb.QueryRequest
	}{
		{
			name: "empty namespace",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "",
				Vector:    []float32{1.0, 2.0, 3.0},
				TopK:      10,
			},
		},
		{
			name: "invalid namespace starting with digit",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "123branch",
				Vector:    []float32{1.0, 2.0, 3.0},
				TopK:      10,
			},
		},
		{
			name: "invalid namespace with slash",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "feature/branch",
				Vector:    []float32{1.0, 2.0, 3.0},
				TopK:      10,
			},
		},
		{
			name: "empty query vector",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "main",
				Vector:    nil,
				TopK:      10,
			},
		},
		{
			name: "top_k is zero",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "main",
				Vector:    []float32{1.0, 2.0, 3.0},
				TopK:      0,
			},
		},
		{
			name: "zero query vector",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "main",
				Vector:    []float32{0.0, 0.0, 0.0},
				TopK:      10,
			},
		},
		{
			name: "dimension mismatch from search",
			req: &cloudyneighpb.QueryRequest{
				Namespace: "main",
				Vector:    []float32{1.0, 2.0},
				TopK:      10,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Query(ctx, tc.req)
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
		})
	}
}

func TestQuery_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	ingestSrv, err := grpcapi.NewIngestServer(log)
	require.NoError(t, err)

	flusher, err := ingest.NewFlusher(store, log, ingest.Config{
		DocThreshold: 1000,
	})
	require.NoError(t, err)

	_, err = ingestSrv.Upsert(ctx, &cloudyneighpb.UpsertRequest{
		Namespace: "main",
		Records: []*cloudyneighpb.Record{
			{
				Id: "doc-1",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": {Values: []float32{1.0, 0.0}},
				},
				Attributes: map[string]*cloudyneighpb.AttributeValue{
					"genre": stringAttr("sci-fi"),
				},
			},
			{
				Id: "doc-2",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": {Values: []float32{0.6, 0.8}},
				},
				Attributes: map[string]*cloudyneighpb.AttributeValue{
					"genre": stringAttr("sci-fi"),
				},
			},
			{
				Id: "doc-3",
				Vectors: map[string]*cloudyneighpb.Vector{
					"default": {Values: []float32{0.0, 1.0}},
				},
				Attributes: map[string]*cloudyneighpb.AttributeValue{
					"genre": stringAttr("fantasy"),
				},
			},
		},
	})
	require.NoError(t, err)

	flusherCtx, cancelFlusher := context.WithCancel(ctx)
	cancelFlusher()
	require.NoError(t, flusher.Run(flusherCtx))

	client, querySrv := setupQueryTestEnv(t, store)

	require.NoError(t, querySrv.SyncOnce(ctx))

	resp, err := client.Query(ctx, &cloudyneighpb.QueryRequest{
		Namespace: "main",
		Vector:    []float32{1.0, 0.0},
		TopK:      2,
	})
	require.NoError(t, err)
	require.Len(t, resp.Hits, 2)
	require.Equal(t, "doc-1", resp.Hits[0].Record.Id)
	require.Equal(t, "doc-2", resp.Hits[1].Record.Id)

	resp, err = client.Query(ctx, &cloudyneighpb.QueryRequest{
		Namespace: "main",
		Vector:    []float32{1.0, 0.0},
		TopK:      10,
		Filter: &cloudyneighpb.EqualityFilter{
			Field: "genre",
			Value: stringAttr("fantasy"),
		},
	})
	require.NoError(t, err)
	require.Len(t, resp.Hits, 1)
	require.Equal(t, "doc-3", resp.Hits[0].Record.Id)

	_, err = ingestSrv.Delete(ctx, &cloudyneighpb.DeleteRequest{
		Namespace: "main",
		Ids:       []string{"doc-1"},
	})
	require.NoError(t, err)

	flusherCtx2, cancelFlusher2 := context.WithCancel(ctx)
	cancelFlusher2()
	require.NoError(t, flusher.Run(flusherCtx2))

	require.NoError(t, querySrv.SyncOnce(ctx))

	resp, err = client.Query(ctx, &cloudyneighpb.QueryRequest{
		Namespace: "main",
		Vector:    []float32{1.0, 0.0},
		TopK:      2,
	})
	require.NoError(t, err)
	require.Len(t, resp.Hits, 2)
	require.Equal(t, "doc-2", resp.Hits[0].Record.Id)
	require.Equal(t, "doc-3", resp.Hits[1].Record.Id)
}

func TestQuery_ConcurrentSyncAndQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	client, querySrv := setupQueryTestEnv(t, store)

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- querySrv.Run(ctx)
	}()

	const numBatches = 50
	const numReaders = 4
	done := make(chan struct{})
	readerErrCh := make(chan error, numReaders)
	var wg sync.WaitGroup

	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					resp, err := client.Query(ctx, &cloudyneighpb.QueryRequest{
						Namespace: "main",
						Vector:    []float32{1.0, 0.0},
						TopK:      5,
					})
					if err != nil {
						readerErrCh <- err
						return
					}
					_ = resp
				}
			}
		}()
	}

	var gen string
	var segIDs []string
	for i := 0; i < numBatches; i++ {
		segID := fmt.Sprintf("seg-%d", i)
		rec := &cloudyneighpb.Record{
			Id: fmt.Sprintf("doc-%d", i),
			Vectors: map[string]*cloudyneighpb.Vector{
				"default": {Values: []float32{float32(i + 1), 0.0}},
			},
		}
		payload, err := proto.Marshal(rec)
		require.NoError(t, err)

		mut := &storagepb.DocumentMutation{
			Branch:  "main",
			DocId:   rec.Id,
			Op:      storagepb.MutationOp_PUT,
			Payload: payload,
		}
		writeSegment(t, ctx, store, "main", segID, []*storagepb.DocumentMutation{mut})
		segIDs = append(segIDs, segID)
		gen = updateManifest(t, ctx, store, "main", segIDs, gen)
	}

	close(done)
	wg.Wait()
	close(readerErrCh)

	for err := range readerErrCh {
		require.NoError(t, err)
	}

	require.NoError(t, querySrv.SyncOnce(ctx))

	resp, err := client.Query(ctx, &cloudyneighpb.QueryRequest{
		Namespace: "main",
		Vector:    []float32{1.0, 0.0},
		TopK:      100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Hits, numBatches)

	cancel()
	err = <-runErrCh
	require.NoError(t, err)
}
