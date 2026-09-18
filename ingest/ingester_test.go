package ingest_test

import (
	"context"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

func TestNewBatchIngester_NilStore(t *testing.T) {
	_, err := ingest.NewBatchIngester(nil, nil, ingest.BatchConfig{})
	assert.ErrorIs(t, err, ingest.ErrNilStore)
}

func TestNewBatchIngester_NilLog(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	_, err = ingest.NewBatchIngester(store, nil, ingest.BatchConfig{})
	assert.ErrorIs(t, err, ingest.ErrNilLog)
}

func TestBatchIngester_BatchDocThreshold(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	b, err := ingest.NewBatchIngester(store, log, ingest.BatchConfig{
		MaxDocs:     10,
		MaxInterval: 1 * time.Second,
	})
	require.NoError(t, err)
	defer b.Close()

	ctx := context.Background()
	var g errgroup.Group

	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		g.Go(func() error {
			return b.Upsert(ctx, "main", []*cloudyneighpb.Record{
				{Id: docID + "1"},
				{Id: docID + "2"},
			})
		})
	}

	require.NoError(t, g.Wait())

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, records, 10)
}

func TestBatchIngester_BatchTimeThreshold(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	b, err := ingest.NewBatchIngester(store, log, ingest.BatchConfig{
		MaxDocs:     1000,
		MaxInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)
	defer b.Close()

	ctx := context.Background()
	start := time.Now()
	err = b.Upsert(ctx, "main", []*cloudyneighpb.Record{
		{Id: "doc-1"},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond)

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)

	var walRec storagepb.WalRecord
	require.NoError(t, proto.Unmarshal(records[0], &walRec))
	assert.Equal(t, "doc-1", walRec.GetMutation().DocId)
}

func TestBatchIngester_Delete(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	b, err := ingest.NewBatchIngester(store, log, ingest.BatchConfig{
		MaxDocs:     2,
		MaxInterval: 1 * time.Second,
	})
	require.NoError(t, err)
	defer b.Close()

	ctx := context.Background()
	err = b.Delete(ctx, "main", []string{"doc-1", "doc-2"})
	require.NoError(t, err)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, records, 2)

	var walRec storagepb.WalRecord
	require.NoError(t, proto.Unmarshal(records[0], &walRec))
	assert.Equal(t, storagepb.MutationOp_DELETE, walRec.GetMutation().Op)
	assert.Equal(t, "doc-1", walRec.GetMutation().DocId)
}

func TestBatchIngester_CreateNamespace(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	b, err := ingest.NewBatchIngester(store, log, ingest.BatchConfig{})
	require.NoError(t, err)
	defer b.Close()

	ctx := context.Background()
	require.NoError(t, b.CreateNamespace(ctx, "tenant-a"))
	assert.Error(t, b.CreateNamespace(ctx, "tenant-a"))
}

func TestBatchIngester_Fork(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	b, err := ingest.NewBatchIngester(store, log, ingest.BatchConfig{
		MaxDocs:     1,
		MaxInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer b.Close()

	ctx := context.Background()
	assert.Error(t, b.Fork(ctx, "nonexistent", "child"))

	require.NoError(t, b.CreateNamespace(ctx, "parent"))
	require.NoError(t, b.Fork(ctx, "parent", "child"))
	assert.Error(t, b.Fork(ctx, "parent", "child"))

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, records, 1)

	var walRec storagepb.WalRecord
	require.NoError(t, proto.Unmarshal(records[0], &walRec))
	evt := walRec.GetBranchEvent()
	require.NotNil(t, evt)
	assert.Equal(t, storagepb.BranchLifecycleEvent_FORK, evt.Type)
	assert.Equal(t, "child", evt.Branch)
	assert.Equal(t, "parent", evt.ParentBranch)
}
