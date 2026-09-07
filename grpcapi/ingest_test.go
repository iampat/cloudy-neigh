package grpcapi_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/iampat/cloudy-neigh/grpcapi"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/recordio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func setupTestEnv(t *testing.T) (cloudyneighpb.IngestServiceClient, *logstream.Log) {
	t.Helper()
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	srv, err := grpcapi.NewIngestServer(log)
	require.NoError(t, err)

	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	cloudyneighpb.RegisterIngestServiceServer(s, srv)

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

	return cloudyneighpb.NewIngestServiceClient(conn), log
}

func TestNewIngestServer_NilLog(t *testing.T) {
	_, err := grpcapi.NewIngestServer(nil)
	assert.ErrorIs(t, err, grpcapi.ErrNilLog)
}

func TestUpsert_Success(t *testing.T) {
	client, log := setupTestEnv(t)
	ctx := context.Background()

	doc1 := &cloudyneighpb.Document{
		Id:     "doc-1",
		Vector: []float32{0.1, 0.2, 0.3},
		Attributes: map[string]string{
			"title": "Document One",
			"lang":  "en",
		},
	}
	doc2 := &cloudyneighpb.Document{
		Id:     "doc-2",
		Vector: []float32{0.4, 0.5, 0.6},
		Attributes: map[string]string{
			"title": "Document Two",
			"lang":  "fr",
		},
	}

	resp, err := client.Upsert(ctx, &cloudyneighpb.UpsertRequest{
		Namespace: "main",
		Documents: []*cloudyneighpb.Document{doc1, doc2},
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(2), resp.UpsertedCount)

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, records, 2)

	wantDocs := []*cloudyneighpb.Document{doc1, doc2}
	for i, r := range records {
		var rec storagepb.WalRecord
		require.NoError(t, proto.Unmarshal(r, &rec))

		mutation := rec.GetMutation()
		require.NotNil(t, mutation)
		assert.Equal(t, "main", mutation.Branch)
		assert.Equal(t, wantDocs[i].Id, mutation.DocId)
		assert.Equal(t, storagepb.MutationOp_PUT, mutation.Op)

		var gotDoc cloudyneighpb.Document
		require.NoError(t, proto.Unmarshal(mutation.Payload, &gotDoc))
		assert.True(t, proto.Equal(wantDocs[i], &gotDoc))
	}
}

func TestUpsert_Empty(t *testing.T) {
	client, log := setupTestEnv(t)
	ctx := context.Background()

	resp, err := client.Upsert(ctx, &cloudyneighpb.UpsertRequest{
		Namespace: "main",
		Documents: nil,
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(0), resp.UpsertedCount)

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tail)
}

func TestUpsert_Validation(t *testing.T) {
	client, _ := setupTestEnv(t)
	ctx := context.Background()

	validDoc := &cloudyneighpb.Document{Id: "doc-1"}

	tests := []struct {
		name string
		req  *cloudyneighpb.UpsertRequest
	}{
		{
			name: "empty namespace",
			req: &cloudyneighpb.UpsertRequest{
				Namespace: "",
				Documents: []*cloudyneighpb.Document{validDoc},
			},
		},
		{
			name: "invalid namespace starting with digit",
			req: &cloudyneighpb.UpsertRequest{
				Namespace: "123branch",
				Documents: []*cloudyneighpb.Document{validDoc},
			},
		},
		{
			name: "invalid namespace with slash",
			req: &cloudyneighpb.UpsertRequest{
				Namespace: "feature/branch",
				Documents: []*cloudyneighpb.Document{validDoc},
			},
		},
		{
			name: "nil document in batch",
			req: &cloudyneighpb.UpsertRequest{
				Namespace: "main",
				Documents: []*cloudyneighpb.Document{nil},
			},
		},
		{
			name: "empty document id",
			req: &cloudyneighpb.UpsertRequest{
				Namespace: "main",
				Documents: []*cloudyneighpb.Document{{Id: ""}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Upsert(ctx, tc.req)
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
		})
	}
}

func TestDelete_Success(t *testing.T) {
	client, log := setupTestEnv(t)
	ctx := context.Background()

	resp, err := client.Delete(ctx, &cloudyneighpb.DeleteRequest{
		Namespace: "main",
		Ids:       []string{"doc-1", "doc-2"},
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(2), resp.DeletedCount)

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, records, 2)

	wantIds := []string{"doc-1", "doc-2"}
	for i, r := range records {
		var rec storagepb.WalRecord
		require.NoError(t, proto.Unmarshal(r, &rec))

		mutation := rec.GetMutation()
		require.NotNil(t, mutation)
		assert.Equal(t, "main", mutation.Branch)
		assert.Equal(t, wantIds[i], mutation.DocId)
		assert.Equal(t, storagepb.MutationOp_DELETE, mutation.Op)
		assert.Empty(t, mutation.Payload)
	}
}

func TestDelete_Empty(t *testing.T) {
	client, log := setupTestEnv(t)
	ctx := context.Background()

	resp, err := client.Delete(ctx, &cloudyneighpb.DeleteRequest{
		Namespace: "main",
		Ids:       nil,
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(0), resp.DeletedCount)

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tail)
}

func TestDelete_Validation(t *testing.T) {
	client, _ := setupTestEnv(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  *cloudyneighpb.DeleteRequest
	}{
		{
			name: "empty namespace",
			req: &cloudyneighpb.DeleteRequest{
				Namespace: "",
				Ids:       []string{"doc-1"},
			},
		},
		{
			name: "invalid namespace",
			req: &cloudyneighpb.DeleteRequest{
				Namespace: "123branch",
				Ids:       []string{"doc-1"},
			},
		},
		{
			name: "empty id in list",
			req: &cloudyneighpb.DeleteRequest{
				Namespace: "main",
				Ids:       []string{"doc-1", ""},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Delete(ctx, tc.req)
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, st.Code())
		})
	}
}

func TestUpsert_CanceledContext(t *testing.T) {
	client, _ := setupTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Upsert(ctx, &cloudyneighpb.UpsertRequest{
		Namespace: "main",
		Documents: []*cloudyneighpb.Document{{Id: "doc-1"}},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Canceled, st.Code())
}

func TestLocalFSBackend(t *testing.T) {
	dir := t.TempDir()
	store, err := objectstore.Open(context.Background(), "file://"+dir+"?create_dir=true")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	srv, err := grpcapi.NewIngestServer(log)
	require.NoError(t, err)

	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	cloudyneighpb.RegisterIngestServiceServer(s, srv)

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

	client := cloudyneighpb.NewIngestServiceClient(conn)
	ctx := context.Background()

	doc := &cloudyneighpb.Document{
		Id:     "wiki-101",
		Vector: []float32{0.123, 0.456, 0.789},
		Attributes: map[string]string{
			"title": "Machine Learning",
			"lang":  "en",
		},
	}

	uResp, err := client.Upsert(ctx, &cloudyneighpb.UpsertRequest{
		Namespace: "wiki",
		Documents: []*cloudyneighpb.Document{doc},
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(1), uResp.UpsertedCount)

	dResp, err := client.Delete(ctx, &cloudyneighpb.DeleteRequest{
		Namespace: "wiki",
		Ids:       []string{"wiki-102"},
	})
	require.NoError(t, err)
	assert.Equal(t, uint32(1), dResp.DeletedCount)

	seg1Path := filepath.Join(dir, "wal", "00000000000000000001.recordio")
	seg2Path := filepath.Join(dir, "wal", "00000000000000000002.recordio")

	info1, err := os.Stat(seg1Path)
	require.NoError(t, err)
	assert.Greater(t, info1.Size(), int64(0))

	info2, err := os.Stat(seg2Path)
	require.NoError(t, err)
	assert.Greater(t, info2.Size(), int64(0))

	f1, err := os.Open(seg1Path)
	require.NoError(t, err)
	defer f1.Close()

	scanner1 := recordio.NewScanner(f1)
	require.True(t, scanner1.Scan())
	var rec1 storagepb.WalRecord
	require.NoError(t, proto.Unmarshal(scanner1.Record(), &rec1))
	m1 := rec1.GetMutation()
	require.NotNil(t, m1)
	assert.Equal(t, "wiki", m1.Branch)
	assert.Equal(t, "wiki-101", m1.DocId)
	assert.Equal(t, storagepb.MutationOp_PUT, m1.Op)

	var doc1 cloudyneighpb.Document
	require.NoError(t, proto.Unmarshal(m1.Payload, &doc1))
	assert.True(t, proto.Equal(doc, &doc1))
	assert.False(t, scanner1.Scan())
	require.NoError(t, scanner1.Err())
	t.Logf("Validated segment 1: %s (%d bytes), doc_id=%s, vector_dims=%d", seg1Path, info1.Size(), doc1.Id, len(doc1.Vector))

	f2, err := os.Open(seg2Path)
	require.NoError(t, err)
	defer f2.Close()

	scanner2 := recordio.NewScanner(f2)
	require.True(t, scanner2.Scan())
	var rec2 storagepb.WalRecord
	require.NoError(t, proto.Unmarshal(scanner2.Record(), &rec2))
	m2 := rec2.GetMutation()
	require.NotNil(t, m2)
	assert.Equal(t, "wiki", m2.Branch)
	assert.Equal(t, "wiki-102", m2.DocId)
	assert.Equal(t, storagepb.MutationOp_DELETE, m2.Op)
	assert.Empty(t, m2.Payload)
	assert.False(t, scanner2.Scan())
	require.NoError(t, scanner2.Err())
	t.Logf("Validated segment 2: %s (%d bytes), deleted doc_id=%s", seg2Path, info2.Size(), m2.DocId)
}
