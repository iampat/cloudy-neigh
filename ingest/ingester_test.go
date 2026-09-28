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
	"github.com/iampat/cloudy-neigh/logstream"
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
	ns := namespace.DefaultNamespace
	branch := "main"
	records := []*cloudyneighpb.Record{
		{Id: "doc-1"},
		{Id: "doc-2"},
		{Id: "doc-3"},
	}

	require.NoError(t, ing.Upsert(ctx, ns, branch, records))

	log, err := logstream.New(store, namespace.Scope{Namespace: ns}.WALPrefix())
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
		assert.Equal(t, branch, mut.Branch)
		assert.Equal(t, storagepb.MutationOp_PUT, mut.Op)
		assert.Equal(t, records[i].Id, mut.DocId)
	}

	require.NoError(t, ing.Upsert(ctx, ns, branch, nil))
}

func TestIngester_Delete(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	ns := namespace.DefaultNamespace
	branch := "main"
	ids := []string{"doc-1", "doc-2"}
	require.NoError(t, ing.Delete(ctx, ns, branch, ids))

	log, err := logstream.New(store, namespace.Scope{Namespace: ns}.WALPrefix())
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
		assert.Equal(t, branch, mut.Branch)
		assert.Equal(t, storagepb.MutationOp_DELETE, mut.Op)
		assert.Equal(t, ids[i], mut.DocId)
	}

	require.NoError(t, ing.Delete(ctx, ns, branch, nil))
}

func TestIngester_Fork(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	ns := namespace.DefaultNamespace
	parent := "parent"
	child := "child"
	assert.Error(t, ing.Fork(ctx, ns, parent, child))

	_, err = manifest.Write(ctx, store, defaultKey(parent), &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)
	require.NoError(t, ing.Fork(ctx, ns, parent, child))
	assert.Error(t, ing.Fork(ctx, ns, parent, child))

	log, err := logstream.New(store, namespace.Scope{Namespace: ns}.WALPrefix())
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
	assert.Equal(t, child, evt.Branch)
	assert.Equal(t, parent, evt.ParentBranch)
}

func TestIngester_MultiNamespaceRouting(t *testing.T) {
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, ing.Upsert(ctx, "ns-1", "main", []*cloudyneighpb.Record{{Id: "a-1"}}))
	require.NoError(t, ing.Upsert(ctx, "ns-2", "main", []*cloudyneighpb.Record{{Id: "b-1"}, {Id: "b-2"}}))

	logA, err := logstream.New(store, namespace.Scope{Namespace: "ns-1"}.WALPrefix())
	require.NoError(t, err)

	logB, err := logstream.New(store, namespace.Scope{Namespace: "ns-2"}.WALPrefix())
	require.NoError(t, err)

	recsA, err := logA.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, recsA, 1)

	recsB, err := logB.Read(ctx, 1)
	require.NoError(t, err)
	require.Len(t, recsB, 2)
}

func TestIngester_ConcurrentMultiNamespace(t *testing.T) {
	const numNamespaces = 8
	const writesPerNamespace = 10

	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	defer store.Close()

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(numNamespaces)
	for i := 0; i < numNamespaces; i++ {
		ns := fmt.Sprintf("ns-%d", i)
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerNamespace; j++ {
				err := ing.Upsert(ctx, ns, "main", []*cloudyneighpb.Record{
					{Id: fmt.Sprintf("doc-%d", j)},
				})
				if err != nil {
					t.Errorf("upsert failed for namespace %s: %v", ns, err)
				}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < numNamespaces; i++ {
		log, err := logstream.New(store, namespace.Scope{Namespace: fmt.Sprintf("ns-%d", i)}.WALPrefix())
		require.NoError(t, err)

		tail, err := log.Tail(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(writesPerNamespace), tail)
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

	ns := namespace.DefaultNamespace
	parent := "parent"
	child := "child"

	_, err = manifest.Write(ctx, store, defaultKey(parent), &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)

	err = ing.Fork(ctx, ns, parent, child)
	require.Error(t, err)

	_, _, err = manifest.Read(ctx, store, defaultKey(child))
	require.ErrorIs(t, err, objectstore.ErrNotFound)
}

func TestIngester_Fork_AddBranchFailureRollback(t *testing.T) {
	ctx := context.Background()
	memStore, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	defer memStore.Close()

	scope := namespace.Scope{Namespace: namespace.DefaultNamespace}

	store := &failAppendStore{
		Store:   memStore,
		failKey: "branches.json",
	}

	ing, err := ingest.NewIngester(store)
	require.NoError(t, err)

	ns := namespace.DefaultNamespace
	parent := "parent"
	child := "child"

	_, err = manifest.Write(ctx, memStore, defaultKey(parent), &storagepb.BranchManifest{CheckpointSeq: 1}, "")
	require.NoError(t, err)

	err = ing.Fork(ctx, ns, parent, child)
	require.Error(t, err)

	_, _, err = manifest.Read(ctx, memStore, defaultKey(child))
	require.ErrorIs(t, err, objectstore.ErrNotFound)

	branches, err := scope.ListBranches(ctx, memStore)
	require.NoError(t, err)
	require.NotContains(t, branches, child)

	retryIng, err := ingest.NewIngester(memStore)
	require.NoError(t, err)
	require.NoError(t, retryIng.Fork(ctx, ns, parent, child))

	_, _, err = manifest.Read(ctx, memStore, defaultKey(child))
	require.NoError(t, err)

	branches, err = scope.ListBranches(ctx, memStore)
	require.NoError(t, err)
	require.Contains(t, branches, child)
}
