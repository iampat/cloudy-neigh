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
		Tenant:  "acme-corp",
		Version: 4,
		Namespaces: map[string]*namespacepb.NamespaceMetadata{
			"analytics": {
				Status:    namespacepb.NamespaceStatus_NAMESPACE_STATUS_DELETED,
				Epoch:     1,
				CreatedAt: 1773000000,
				DeletedAt: 1773500000,
			},
			"catalog": {
				Status:    namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE,
				Epoch:     0,
				CreatedAt: 1774000000,
			},
		},
	}

	opts := protojson.MarshalOptions{UseProtoNames: true}
	data, err := opts.Marshal(catalog)
	require.NoError(t, err)

	assert.Contains(t, string(data), `"status":"NAMESPACE_STATUS_ACTIVE"`)
	assert.Contains(t, string(data), `"created_at"`)
	assert.Contains(t, string(data), `"deleted_at"`)

	var roundTrip namespacepb.TenantCatalog
	err = protojson.Unmarshal(data, &roundTrip)
	require.NoError(t, err)
	assert.True(t, proto.Equal(catalog, &roundTrip))
}

func TestTenantCatalogProtoRoundTrip(t *testing.T) {
	catalog := &namespacepb.TenantCatalog{
		Tenant:  "acme",
		Version: 2,
		Namespaces: map[string]*namespacepb.NamespaceMetadata{
			"catalog": {
				Status:    namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE,
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

	_, gen, err := namespace.CreateNamespace(ctx, store, "acme", "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)

	catalog, gotGen, err := namespace.ReadTenantCatalogIfGeneration(ctx, store, "acme", gen)
	require.NoError(t, err)
	assert.Equal(t, gen, gotGen)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, catalog.Namespaces["catalog"].Status)

	_, _, err = namespace.ReadTenantCatalogIfGeneration(ctx, store, "acme", "stale")
	assert.ErrorIs(t, err, namespace.ErrGenerationMismatch)
}

func TestCreateNamespaceRetriesCASConflict(t *testing.T) {
	ctx := context.Background()
	base := openMem(t)

	store := &injectConflictStore{Store: base, tenant: "acme"}
	meta, _, err := namespace.CreateNamespace(ctx, store, "acme", "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, meta.Status)
	assert.Equal(t, uint64(0), meta.Epoch)

	catalog, _, err := namespace.ReadTenantCatalog(ctx, base, "acme")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), catalog.Version)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, catalog.Namespaces["catalog"].Status)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, catalog.Namespaces["other"].Status)
}

func TestDeleteNamespaceCannotBeRecreated(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	created, _, err := namespace.CreateNamespace(ctx, store, "acme", "analytics", time.Unix(1773000000, 0))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), created.Epoch)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, created.Status)

	deleted, _, err := namespace.DeleteNamespace(ctx, store, "acme", "analytics", time.Unix(1773500000, 0))
	require.NoError(t, err)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_DELETED, deleted.Status)
	assert.Equal(t, uint64(0), deleted.Epoch)
	assert.Equal(t, int64(1773500000), deleted.DeletedAt)

	deletedAgain, _, err := namespace.DeleteNamespace(ctx, store, "acme", "analytics", time.Unix(1773600000, 0))
	require.NoError(t, err)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_DELETED, deletedAgain.Status)

	_, _, err = namespace.CreateNamespace(ctx, store, "acme", "analytics", time.Unix(1773700000, 0))
	assert.ErrorIs(t, err, namespace.ErrNamespaceAlreadyExists)
}

func TestCatalogCacheLookupAndInvalidate(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, store, "acme", "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)

	cache, err := namespace.NewCatalogCache(store, time.Hour)
	require.NoError(t, err)

	meta, err := cache.LookupNamespace(ctx, "acme", "catalog")
	require.NoError(t, err)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, meta.Status)

	_, _, err = namespace.DeleteNamespace(ctx, store, "acme", "catalog", time.Unix(1774100000, 0))
	require.NoError(t, err)

	meta, err = cache.LookupNamespace(ctx, "acme", "catalog")
	require.NoError(t, err)
	assert.Equal(t, namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE, meta.Status)

	cache.InvalidateTenant("acme")
	_, err = cache.LookupNamespace(ctx, "acme", "catalog")
	assert.ErrorIs(t, err, namespace.ErrNamespaceDeleted)
}

func TestCatalogCacheRunRefreshesCachedTenant(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, store, "acme", "catalog", time.Unix(1774000000, 0))
	require.NoError(t, err)

	cache, err := namespace.NewCatalogCache(store, 10*time.Millisecond)
	require.NoError(t, err)

	_, err = cache.LookupNamespace(ctx, "acme", "catalog")
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- cache.Run(runCtx)
	}()

	_, _, err = namespace.DeleteNamespace(ctx, store, "acme", "catalog", time.Unix(1774100000, 0))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := cache.LookupNamespace(ctx, "acme", "catalog")
		return errors.Is(err, namespace.ErrNamespaceDeleted)
	}, time.Second, 10*time.Millisecond)

	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
}

func TestCatalogCacheConcurrentReads(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	_, _, err := namespace.CreateNamespace(ctx, store, "acme", "ns-0", time.Unix(1774000000, 0))
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
				meta, err := cache.LookupNamespace(ctx, "acme", "ns-0")
				if err != nil {
					errCh <- err
					return
				}
				if meta.Status != namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE {
					errCh <- fmt.Errorf("unexpected status: %v", meta.Status)
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

	_, _, err := namespace.CreateNamespace(ctx, nil, "acme", "test", time.Now())
	assert.ErrorIs(t, err, namespace.ErrNilStore)

	_, _, err = namespace.CreateNamespace(ctx, store, "1bad", "test", time.Now())
	assert.ErrorIs(t, err, namespace.ErrInvalidName)

	_, _, err = namespace.CreateNamespace(ctx, store, "acme", "1bad", time.Now())
	assert.ErrorIs(t, err, namespace.ErrInvalidName)

	_, _, err = namespace.DeleteNamespace(ctx, store, "acme", "missing", time.Now())
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

	if _, _, err := namespace.CreateNamespace(ctx, store, "acme", "catalog", time.Now()); err != nil {
		b.Fatal(err)
	}
	cache, err := namespace.NewCatalogCache(store, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := cache.LookupNamespace(ctx, "acme", "catalog"); err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if _, err := cache.LookupNamespace(ctx, "acme", "catalog"); err != nil {
			b.Fatal(err)
		}
	}
}

type injectConflictStore struct {
	objectstore.Store
	tenant string

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
	if !s.injected && key == pathForTenant(s.tenant) && cond.Absent {
		s.injected = true
		_, err := s.Store.Put(ctx, key, strings.NewReader(`{"tenant":"acme","version":1,"namespaces":{"other":{"status":"NAMESPACE_STATUS_ACTIVE","epoch":0,"created_at":1773990000}}}`), objectstore.Condition{Absent: true})
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

func pathForTenant(tenant string) string {
	return tenant + "/ns.json"
}

func TestActiveNamespaces(t *testing.T) {
	ctx := context.Background()
	store := openMem(t)

	// Uninitialized store returns default namespace.
	active, err := namespace.ActiveNamespaces(ctx, store, "cloudy")
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, active)

	// Add namespaces for tenant.
	_, _, err = namespace.CreateNamespace(ctx, store, "acme", "catalog", time.Now())
	require.NoError(t, err)
	_, _, err = namespace.CreateNamespace(ctx, store, "acme", "analytics", time.Now())
	require.NoError(t, err)

	active, err = namespace.ActiveNamespaces(ctx, store, "acme")
	require.NoError(t, err)
	assert.Equal(t, []string{"analytics", "catalog"}, active)

	// Deleted namespace is not active.
	_, _, err = namespace.DeleteNamespace(ctx, store, "acme", "analytics", time.Now())
	require.NoError(t, err)

	active, err = namespace.ActiveNamespaces(ctx, store, "acme")
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog"}, active)
}
