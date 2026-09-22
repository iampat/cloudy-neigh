package ingest_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNewIngester_NilStore(t *testing.T) {
	_, err := ingest.NewIngester(nil, nil)
	assert.ErrorIs(t, err, ingest.ErrNilStore)
}

func TestNewIngester_NilLog(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	_, err = ingest.NewIngester(store, nil)
	assert.ErrorIs(t, err, ingest.ErrNilLog)
}

func TestIngester_Upsert(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	in, err := ingest.NewIngester(store, log)
	require.NoError(t, err)

	ctx := context.Background()
	records := []*cloudyneighpb.Record{
		{Id: "doc-1"},
		{Id: "doc-2"},
		{Id: "doc-3"},
	}

	require.NoError(t, in.Upsert(ctx, "main", records))

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	walRecords, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, walRecords, 3)

	for i, r := range walRecords {
		var walRec storagepb.WalRecord
		require.NoError(t, proto.Unmarshal(r, &walRec))
		mut := walRec.GetMutation()
		require.NotNil(t, mut)
		assert.Equal(t, "main", mut.Branch)
		assert.Equal(t, storagepb.MutationOp_PUT, mut.Op)
		assert.Equal(t, records[i].Id, mut.DocId)
	}

	require.NoError(t, in.Upsert(ctx, "main", nil))
}

func TestIngester_Delete(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	in, err := ingest.NewIngester(store, log)
	require.NoError(t, err)

	ctx := context.Background()
	ids := []string{"doc-1", "doc-2"}
	require.NoError(t, in.Delete(ctx, "main", ids))

	tail, err := log.Tail(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tail)

	walRecords, err := log.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, walRecords, 2)

	for i, r := range walRecords {
		var walRec storagepb.WalRecord
		require.NoError(t, proto.Unmarshal(r, &walRec))
		mut := walRec.GetMutation()
		require.NotNil(t, mut)
		assert.Equal(t, "main", mut.Branch)
		assert.Equal(t, storagepb.MutationOp_DELETE, mut.Op)
		assert.Equal(t, ids[i], mut.DocId)
	}

	require.NoError(t, in.Delete(ctx, "main", nil))
}

func TestIngester_Fork(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	in, err := ingest.NewIngester(store, log)
	require.NoError(t, err)

	ctx := context.Background()
	assert.Error(t, in.Fork(ctx, "nonexistent", "child"))

	_, err = kvfs.UpdateBranch(ctx, store, "parent", &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)
	require.NoError(t, in.Fork(ctx, "parent", "child"))
	assert.Error(t, in.Fork(ctx, "parent", "child"))

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

type failAppendStore struct {
	objectstore.Store
	failKey string
}

func (s *failAppendStore) Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
	if strings.Contains(key, s.failKey) {
		return "", errors.New("simulated append failure")
	}
	return s.Store.Put(ctx, key, r, cond)
}

func TestIngester_Fork_AppendFailureRollback(t *testing.T) {
	ctx := context.Background()
	memStore, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer memStore.Close()

	store := &failAppendStore{
		Store:   memStore,
		failKey: "wal",
	}

	log, err := logstream.New(store, "wal")
	require.NoError(t, err)

	in, err := ingest.NewIngester(store, log)
	require.NoError(t, err)

	_, err = kvfs.UpdateBranch(ctx, store, "parent", &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)

	err = in.Fork(ctx, "parent", "child")
	require.Error(t, err)

	_, _, err = kvfs.ResolveBranch(ctx, store, "child")
	require.ErrorIs(t, err, objectstore.ErrNotFound)
}
