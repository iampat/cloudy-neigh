package namespace_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	namespacepb "github.com/iampat/cloudy-neigh/proto/namespace/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestTenantCatalogJSONRoundTrip(t *testing.T) {
	catalog := &namespacepb.TenantCatalog{
		Tenant: "acme-corp",
		Namespaces: map[string]*namespacepb.NamespaceMetadata{
			"analytics": {
				Epoch:     1,
				CreatedAt: 1773000000,
				DeletedAt: 1773500000,
			},
			"catalog": {
				Epoch:     0,
				CreatedAt: 1774000000,
			},
		},
	}

	opts := protojson.MarshalOptions{UseProtoNames: true}
	data, err := opts.Marshal(catalog)
	require.NoError(t, err)

	assert.Contains(t, string(data), `"created_at"`)
	assert.Contains(t, string(data), `"deleted_at"`)

	var roundTrip namespacepb.TenantCatalog
	err = protojson.Unmarshal(data, &roundTrip)
	require.NoError(t, err)
	assert.True(t, proto.Equal(catalog, &roundTrip))
}

func TestTenantCatalogProtoRoundTrip(t *testing.T) {
	catalog := &namespacepb.TenantCatalog{
		Tenant: "acme",
		Namespaces: map[string]*namespacepb.NamespaceMetadata{
			"catalog": {
				Epoch:     0,
				CreatedAt: 1774000000,
			},
		},
	}

	data, err := proto.Marshal(catalog)
	require.NoError(t, err)

	var roundTrip namespacepb.TenantCatalog
	err = proto.Unmarshal(data, &roundTrip)
	require.NoError(t, err)
	assert.True(t, proto.Equal(catalog, &roundTrip))
}

func TestReadTenantCatalogIfGeneration(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, gen, err := namespace.CreateNamespace(ctx, store, "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)

	catalog, gotGen, err := namespace.ReadTenantCatalogIfGeneration(ctx, store, gen)
	require.NoError(t, err)
	assert.Equal(t, gen, gotGen)
	assert.Contains(t, catalog.Namespaces, "catalog")

	_, _, err = namespace.ReadTenantCatalogIfGeneration(ctx, store, "stale")
	assert.ErrorIs(t, err, namespace.ErrGenerationMismatch)
}

func TestCreateNamespaceRetriesCASConflict(t *testing.T) {
	ctx := context.Background()
	base := openMem(t)

	store := &injectConflictStore{Store: base}
	meta, _, err := namespace.CreateNamespace(ctx, store, "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)
	assert.Equal(t, int64(1774000000), meta.CreatedAt)

	catalog, _, err := namespace.ReadTenantCatalog(ctx, base)
	require.NoError(t, err)
	assert.Contains(t, catalog.Namespaces, "catalog")
	assert.Contains(t, catalog.Namespaces, "other")
}

func TestDeleteNamespaceCannotBeRecreated(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	created, _, err := namespace.CreateNamespace(ctx, store, "analytics", time.Unix(1773000000, 0))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), created.Epoch)
	assert.Zero(t, created.DeletedAt)

	deleted, _, err := namespace.DeleteNamespace(ctx, store, "analytics", time.Unix(1773500000, 0))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), deleted.Epoch)
	assert.Equal(t, int64(1773500000), deleted.DeletedAt)

	deletedAgain, _, err := namespace.DeleteNamespace(ctx, store, "analytics", time.Unix(1773600000, 0))
	require.NoError(t, err)
	assert.Equal(t, int64(1773500000), deletedAgain.DeletedAt)

	_, _, err = namespace.CreateNamespace(ctx, store, "analytics", time.Unix(1773700000, 0))
	assert.ErrorIs(t, err, namespace.ErrNamespaceAlreadyExists)
}

func TestCatalogCacheLookupAndInvalidate(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, store, "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)

	cache, err := namespace.NewCatalogCache(store, time.Hour)
	require.NoError(t, err)

	_, err = cache.LookupNamespace(ctx, "catalog")
	require.NoError(t, err)

	_, _, err = namespace.DeleteNamespace(ctx, store, "catalog", time.Unix(1774100000, 0))
	require.NoError(t, err)

	_, err = cache.LookupNamespace(ctx, "catalog")
	require.NoError(t, err)

	cache.Invalidate()
	_, err = cache.LookupNamespace(ctx, "catalog")
	assert.ErrorIs(t, err, namespace.ErrNamespaceDeleted)
}

func TestCatalogCacheRunRefreshesCachedTenant(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, store, "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)

	cache, err := namespace.NewCatalogCache(store, 10*time.Millisecond)
	require.NoError(t, err)

	_, err = cache.LookupNamespace(ctx, "catalog")
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- cache.Run(runCtx)
	}()

	_, _, err = namespace.DeleteNamespace(ctx, store, "catalog", time.Unix(1774100000, 0))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := cache.LookupNamespace(ctx, "catalog")
		return errors.Is(err, namespace.ErrNamespaceDeleted)
	}, time.Second, 10*time.Millisecond)

	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
}

func TestCatalogCacheConcurrentReads(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, store, "ns-0", time.Unix(1774000000, 0))
	require.NoError(t, err)

	cache, err := namespace.NewCatalogCache(store, time.Hour)
	require.NoError(t, err)

	const goroutines = 16
	const iterations = 50

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for range iterations {
				if _, err := cache.LookupNamespace(ctx, "ns-0"); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, nil, "test", time.Now())
	assert.ErrorIs(t, err, namespace.ErrNilStore)

	_, _, err = namespace.CreateNamespace(ctx, store, "1bad", time.Now())
	assert.ErrorIs(t, err, namespace.ErrInvalidName)

	_, _, err = namespace.DeleteNamespace(ctx, store, "missing", time.Now())
	assert.ErrorIs(t, err, namespace.ErrNamespaceNotFound)

	cache, err := namespace.NewCatalogCache(nil, time.Minute)
	assert.Nil(t, cache)
	assert.ErrorIs(t, err, namespace.ErrNilStore)
}

func BenchmarkCatalogCacheLookup(b *testing.B) {
	ctx := context.Background()
	store, err := objectstore.Open(ctx, "mem://")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	if _, _, err := namespace.CreateNamespace(ctx, store, "catalog", time.Now()); err != nil {
		b.Fatal(err)
	}
	cache, err := namespace.NewCatalogCache(store, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := cache.LookupNamespace(ctx, "catalog"); err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if _, err := cache.LookupNamespace(ctx, "catalog"); err != nil {
			b.Fatal(err)
		}
	}
}

type injectConflictStore struct {
	objectstore.Store

	mu       sync.Mutex
	injected bool
}

func (s *injectConflictStore) Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.injected && key == namespace.CatalogFile && cond.Absent {
		s.injected = true
		_, err := s.Store.Put(ctx, key, strings.NewReader(`{"namespaces":{"other":{"epoch":0,"created_at":1773990000}}}`), objectstore.Condition{Absent: true})
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("key %q: %w", key, objectstore.ErrPreconditionFailed)
	}
	return s.Store.Put(ctx, key, bytes.NewReader(data), cond)
}

func openMem(t *testing.T) objectstore.Store {
	t.Helper()
	store, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestActiveNamespaces(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	// Uninitialized store returns default namespace.
	active, err := namespace.ActiveNamespaces(ctx, store)
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, active)

	// Add namespaces.
	_, _, err = namespace.CreateNamespace(ctx, store, "catalog", time.Now())
	require.NoError(t, err)
	_, _, err = namespace.CreateNamespace(ctx, store, "analytics", time.Now())
	require.NoError(t, err)

	active, err = namespace.ActiveNamespaces(ctx, store)
	require.NoError(t, err)
	assert.Equal(t, []string{"analytics", "catalog"}, active)

	// Deleted namespace is not active.
	_, _, err = namespace.DeleteNamespace(ctx, store, "analytics", time.Now())
	require.NoError(t, err)

	active, err = namespace.ActiveNamespaces(ctx, store)
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog"}, active)
}
