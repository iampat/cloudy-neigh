package namespace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/iampat/cloudy-neigh/objectstore"
	namespacepb "github.com/iampat/cloudy-neigh/proto/namespace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNilStore               = errors.New("namespace: nil store")
	ErrNamespaceAlreadyExists = errors.New("namespace: namespace already exists")
	ErrNamespaceNotFound      = errors.New("namespace: namespace not found")
	ErrNamespaceDeleted       = errors.New("namespace: namespace deleted")
	ErrGenerationMismatch     = errors.New("namespace: generation mismatch")

	marshalOpts = protojson.MarshalOptions{
		UseProtoNames: true,
	}
	unmarshalOpts = protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}
)

const defaultSyncInterval = time.Minute

type CatalogCache struct {
	store        objectstore.Store
	syncInterval time.Duration

	mu      sync.Mutex
	tenants map[string]cacheEntry
}

type cacheEntry struct {
	catalog    *namespacepb.TenantCatalog
	generation string
}

func ReadTenantCatalog(ctx context.Context, store objectstore.Store, tenant string) (*namespacepb.TenantCatalog, string, error) {
	if store == nil {
		return nil, "", ErrNilStore
	}
	if err := ValidateTenant(tenant); err != nil {
		return nil, "", err
	}
	rc, obj, err := store.Get(ctx, CatalogPath(tenant))
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("namespace: read catalog %s: %w", tenant, err)
	}
	var catalog namespacepb.TenantCatalog
	if err := unmarshalOpts.Unmarshal(data, &catalog); err != nil {
		return nil, "", fmt.Errorf("namespace: unmarshal catalog %s: %w", tenant, err)
	}
	if catalog.Tenant != tenant {
		return nil, "", fmt.Errorf("namespace: catalog tenant %q does not match key tenant %q", catalog.Tenant, tenant)
	}
	if catalog.Namespaces == nil {
		catalog.Namespaces = make(map[string]*namespacepb.NamespaceMetadata)
	}
	return &catalog, obj.Generation, nil
}

func ReadTenantCatalogIfGeneration(ctx context.Context, store objectstore.Store, tenant, generation string) (*namespacepb.TenantCatalog, string, error) {
	catalog, got, err := ReadTenantCatalog(ctx, store, tenant)
	if err != nil {
		return nil, "", err
	}
	if generation != got {
		return nil, got, ErrGenerationMismatch
	}
	return catalog, got, nil
}

func CreateNamespace(ctx context.Context, store objectstore.Store, tenant, name string, now time.Time) (*namespacepb.NamespaceMetadata, string, error) {
	if store == nil {
		return nil, "", ErrNilStore
	}
	if err := ValidateTenant(tenant); err != nil {
		return nil, "", err
	}
	if err := ValidateNamespace(name); err != nil {
		return nil, "", err
	}
	createdAt := max(now.Unix(), 0)

	for {
		catalog, generation, err := readCatalogOrEmpty(ctx, store, tenant)
		if err != nil {
			return nil, "", err
		}
		if _, exists := catalog.GetNamespaces()[name]; exists {
			return nil, generation, ErrNamespaceAlreadyExists
		}

		catalog.Version++
		meta := &namespacepb.NamespaceMetadata{
			Status:    namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE,
			Epoch:     0,
			CreatedAt: createdAt,
		}
		catalog.Namespaces[name] = meta

		newGen, err := putTenantCatalog(ctx, store, catalog, generation)
		if err == nil {
			return proto.Clone(meta).(*namespacepb.NamespaceMetadata), newGen, nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return nil, "", err
	}
}

func DeleteNamespace(ctx context.Context, store objectstore.Store, tenant, name string, now time.Time) (*namespacepb.NamespaceMetadata, string, error) {
	if store == nil {
		return nil, "", ErrNilStore
	}
	if err := ValidateTenant(tenant); err != nil {
		return nil, "", err
	}
	if err := ValidateNamespace(name); err != nil {
		return nil, "", err
	}
	deletedAt := max(now.Unix(), 0)

	for {
		catalog, generation, err := ReadTenantCatalog(ctx, store, tenant)
		if err != nil {
			if errors.Is(err, objectstore.ErrNotFound) {
				return nil, "", ErrNamespaceNotFound
			}
			return nil, "", err
		}
		meta, ok := catalog.GetNamespaces()[name]
		if !ok {
			return nil, generation, ErrNamespaceNotFound
		}
		if meta.Status == namespacepb.NamespaceStatus_NAMESPACE_STATUS_DELETED {
			return proto.Clone(meta).(*namespacepb.NamespaceMetadata), generation, nil
		}

		catalog.Version++
		meta.Status = namespacepb.NamespaceStatus_NAMESPACE_STATUS_DELETED
		meta.DeletedAt = deletedAt

		newGen, err := putTenantCatalog(ctx, store, catalog, generation)
		if err == nil {
			return proto.Clone(meta).(*namespacepb.NamespaceMetadata), newGen, nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return nil, "", err
	}
}

func NewCatalogCache(store objectstore.Store, syncInterval time.Duration) (*CatalogCache, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if syncInterval <= 0 {
		syncInterval = defaultSyncInterval
	}
	return &CatalogCache{
		store:        store,
		syncInterval: syncInterval,
		tenants:      make(map[string]cacheEntry),
	}, nil
}

func (c *CatalogCache) LookupNamespace(ctx context.Context, tenant, name string) (*namespacepb.NamespaceMetadata, error) {
	if err := ValidateTenant(tenant); err != nil {
		return nil, err
	}
	if err := ValidateNamespace(name); err != nil {
		return nil, err
	}

	if meta, ok := c.lookupCached(tenant, name); ok {
		if meta.Status != namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE {
			return nil, ErrNamespaceDeleted
		}
		return meta, nil
	}

	entry, err := c.refreshTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	meta, ok := entry.catalog.GetNamespaces()[name]
	if !ok {
		return nil, ErrNamespaceNotFound
	}
	if meta.Status != namespacepb.NamespaceStatus_NAMESPACE_STATUS_ACTIVE {
		return nil, ErrNamespaceDeleted
	}
	return proto.Clone(meta).(*namespacepb.NamespaceMetadata), nil
}

func (c *CatalogCache) RefreshTenant(ctx context.Context, tenant string) (*namespacepb.TenantCatalog, string, error) {
	if err := ValidateTenant(tenant); err != nil {
		return nil, "", err
	}
	entry, err := c.refreshTenant(ctx, tenant)
	if err != nil {
		return nil, "", err
	}
	return proto.Clone(entry.catalog).(*namespacepb.TenantCatalog), entry.generation, nil
}

func (c *CatalogCache) InvalidateTenant(tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tenants, tenant)
}

func (c *CatalogCache) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.syncInterval)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for _, tenant := range c.snapshotTenants() {
				if err := ctx.Err(); err != nil {
					return err
				}
				if _, err := c.refreshTenant(ctx, tenant); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}
				}
			}
		}
	}
}

func (c *CatalogCache) lookupCached(tenant, name string) (*namespacepb.NamespaceMetadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.tenants[tenant]
	if !ok {
		return nil, false
	}
	meta, ok := entry.catalog.GetNamespaces()[name]
	if !ok {
		return nil, false
	}
	return proto.Clone(meta).(*namespacepb.NamespaceMetadata), true
}

func (c *CatalogCache) refreshTenant(ctx context.Context, tenant string) (cacheEntry, error) {
	catalog, generation, err := readCatalogOrEmpty(ctx, c.store, tenant)
	if err != nil {
		return cacheEntry{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tenants[tenant] = cacheEntry{
		catalog:    catalog,
		generation: generation,
	}
	return cacheEntry{
		catalog:    catalog,
		generation: generation,
	}, nil
}

func (c *CatalogCache) snapshotTenants() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	tenants := make([]string, 0, len(c.tenants))
	for tenant := range c.tenants {
		tenants = append(tenants, tenant)
	}
	return tenants
}

func readCatalogOrEmpty(ctx context.Context, store objectstore.Store, tenant string) (*namespacepb.TenantCatalog, string, error) {
	catalog, generation, err := ReadTenantCatalog(ctx, store, tenant)
	if err == nil {
		return catalog, generation, nil
	}
	if !errors.Is(err, objectstore.ErrNotFound) {
		return nil, "", err
	}
	return &namespacepb.TenantCatalog{
		Tenant:     tenant,
		Namespaces: make(map[string]*namespacepb.NamespaceMetadata),
	}, "", nil
}

func putTenantCatalog(ctx context.Context, store objectstore.Store, catalog *namespacepb.TenantCatalog, generation string) (string, error) {
	data, err := marshalOpts.Marshal(catalog)
	if err != nil {
		return "", fmt.Errorf("namespace: marshal catalog %s: %w", catalog.Tenant, err)
	}
	cond := objectstore.Condition{Absent: true}
	if generation != "" {
		cond = objectstore.Condition{GenerationMatch: generation}
	}
	return store.Put(ctx, CatalogPath(catalog.Tenant), bytes.NewReader(data), cond)
}
