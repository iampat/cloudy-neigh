package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/iampat/cloudy-neigh/ingest"
	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNewIngester_NilStore(t *testing.T) {
	_, err := ingest.NewIngester(nil)
	assert.ErrorIs(t, err, ingest.ErrNilStore)
}

func TestIngester_Upsert(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	records := []*cloudyneighpb.Record{
		{Id: "doc-1"},
		{Id: "doc-2"},
		{Id: "doc-3"},
	}

	require.NoError(t, ing.Upsert(ctx, "main", records))

	log, err := ing.Log("main")
	require.NoError(t, err)

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

	require.NoError(t, ing.Upsert(ctx, "main", nil))
}

func TestIngester_Delete(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	ids := []string{"doc-1", "doc-2"}
	require.NoError(t, ing.Delete(ctx, "main", ids))

	log, err := ing.Log("main")
	require.NoError(t, err)

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

	require.NoError(t, ing.Delete(ctx, "main", nil))
}

func TestIngester_Fork(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	assert.Error(t, ing.Fork(ctx, "parent", "child"))

	_, err = manifest.Write(ctx, store, "parent", &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)
	require.NoError(t, ing.Fork(ctx, "parent", "child"))
	assert.Error(t, ing.Fork(ctx, "parent", "child"))

	log, err := ing.Log("child")
	require.NoError(t, err)

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

func TestIngester_MultiTenantRouting(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	branchA := namespace.BranchRef("tenant-a", "ns-1", "main")
	branchB := namespace.BranchRef("tenant-b", "ns-2", "main")

	scopeA, _ := namespace.ScopeFromRef(branchA)
	scopeB, _ := namespace.ScopeFromRef(branchB)

	require.NoError(t, ing.Upsert(ctx, branchA, []*cloudyneighpb.Record{{Id: "a-1"}}))
	require.NoError(t, ing.Upsert(ctx, branchB, []*cloudyneighpb.Record{{Id: "b-1"}, {Id: "b-2"}}))

	logA, err := ing.Log(branchA)
	require.NoError(t, err)
	assert.Equal(t, scopeA.WALPrefix(), "tenant-a/ns/ns-1/wal")

	logB, err := ing.Log(branchB)
	require.NoError(t, err)
	assert.Equal(t, scopeB.WALPrefix(), "tenant-b/ns/ns-2/wal")

	recsA, err := logA.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, recsA, 1)

	recsB, err := logB.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, recsB, 2)
}

func TestIngester_ConcurrentMultiTenant(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	const numTenants = 8
	const writesPerTenant = 10

	var wg sync.WaitGroup
	wg.Add(numTenants)
	for i := 0; i < numTenants; i++ {
		tenantID := fmt.Sprintf("tenant-%d", i)
		branch := namespace.BranchRef(tenantID, "default", "main")
		go func(br string) {
			defer wg.Done()
			for j := 0; j < writesPerTenant; j++ {
				err := ing.Upsert(ctx, br, []*cloudyneighpb.Record{
					{Id: fmt.Sprintf("doc-%d", j)},
				})
				if err != nil {
					t.Errorf("upsert failed for branch %s: %v", br, err)
				}
			}
		}(branch)
	}
	wg.Wait()

	for i := 0; i < numTenants; i++ {
		tenantID := fmt.Sprintf("tenant-%d", i)
		branch := namespace.BranchRef(tenantID, "default", "main")
		log, err := ing.Log(branch)
		require.NoError(t, err)

		tail, err := log.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(writesPerTenant), tail)
	}
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

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	_, err = manifest.Write(ctx, store, "parent", &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)

	err = ing.Fork(ctx, "parent", "child")
	require.Error(t, err)

	_, _, err = manifest.Read(ctx, store, "child")
	require.ErrorIs(t, err, objectstore.ErrNotFound)
}
