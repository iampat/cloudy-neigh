package namespace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
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

	mu         sync.Mutex
	catalog    *namespacepb.TenantCatalog
	generation string
}

func ReadTenantCatalog(ctx context.Context, store objectstore.Store) (*namespacepb.TenantCatalog, string, error) {
	if store == nil {
		return nil, "", ErrNilStore
	}
	rc, obj, err := store.Get(ctx, CatalogFile)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("namespace: read catalog: %w", err)
	}
	var catalog namespacepb.TenantCatalog
	if err := unmarshalOpts.Unmarshal(data, &catalog); err != nil {
		return nil, "", fmt.Errorf("namespace: unmarshal catalog: %w", err)
	}
	if catalog.Namespaces == nil {
		catalog.Namespaces = make(map[string]*namespacepb.NamespaceMetadata)
	}
	return &catalog, obj.Generation, nil
}

func ReadTenantCatalogIfGeneration(ctx context.Context, store objectstore.Store, generation string) (*namespacepb.TenantCatalog, string, error) {
	catalog, got, err := ReadTenantCatalog(ctx, store)
	if err != nil {
		return nil, "", err
	}
	if generation != got {
		return nil, got, ErrGenerationMismatch
	}
	return catalog, got, nil
}

func CreateNamespace(ctx context.Context, store objectstore.Store, name string, now time.Time) (*namespacepb.NamespaceMetadata, string, error) {
	if store == nil {
		return nil, "", ErrNilStore
	}
	if err := ValidateName(name); err != nil {
		return nil, "", err
	}
	createdAt := max(now.Unix(), 0)

	for {
		catalog, generation, err := readCatalogOrEmpty(ctx, store)
		if err != nil {
			return nil, "", err
		}
		if _, exists := catalog.GetNamespaces()[name]; exists {
			return nil, generation, ErrNamespaceAlreadyExists
		}

		meta := &namespacepb.NamespaceMetadata{
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

func DeleteNamespace(ctx context.Context, store objectstore.Store, name string, now time.Time) (*namespacepb.NamespaceMetadata, string, error) {
	if store == nil {
		return nil, "", ErrNilStore
	}
	if err := ValidateName(name); err != nil {
		return nil, "", err
	}
	deletedAt := max(now.Unix(), 1)

	for {
		catalog, generation, err := ReadTenantCatalog(ctx, store)
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
		if meta.DeletedAt != 0 {
			return proto.Clone(meta).(*namespacepb.NamespaceMetadata), generation, nil
		}

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

func ActiveNamespaces(ctx context.Context, store objectstore.Store) ([]string, error) {
	cat, _, err := ReadTenantCatalog(ctx, store)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return []string{DefaultNamespace}, nil
		}
		return nil, err
	}
	var names []string
	for name, meta := range cat.GetNamespaces() {
		if meta.GetDeletedAt() == 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return []string{DefaultNamespace}, nil
	}
	slices.Sort(names)
	return names, nil
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
	}, nil
}

func (c *CatalogCache) LookupNamespace(ctx context.Context, name string) (*namespacepb.NamespaceMetadata, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	if meta, ok := c.lookupCached(name); ok {
		if meta.DeletedAt != 0 {
			return nil, ErrNamespaceDeleted
		}
		return meta, nil
	}

	cat, _, err := c.refresh(ctx)
	if err != nil {
		return nil, err
	}
	meta, ok := cat.GetNamespaces()[name]
	if !ok {
		return nil, ErrNamespaceNotFound
	}
	if meta.DeletedAt != 0 {
		return nil, ErrNamespaceDeleted
	}
	return proto.Clone(meta).(*namespacepb.NamespaceMetadata), nil
}

func (c *CatalogCache) Refresh(ctx context.Context) (*namespacepb.TenantCatalog, string, error) {
	cat, gen, err := c.refresh(ctx)
	if err != nil {
		return nil, "", err
	}
	return proto.Clone(cat).(*namespacepb.TenantCatalog), gen, nil
}

func (c *CatalogCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog = nil
	c.generation = ""
}

func (c *CatalogCache) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.syncInterval)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, _, err := c.refresh(ctx); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
			}
		}
	}
}

func (c *CatalogCache) lookupCached(name string) (*namespacepb.NamespaceMetadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.catalog == nil {
		return nil, false
	}
	meta, ok := c.catalog.GetNamespaces()[name]
	if !ok {
		return nil, false
	}
	return proto.Clone(meta).(*namespacepb.NamespaceMetadata), true
}

func (c *CatalogCache) refresh(ctx context.Context) (*namespacepb.TenantCatalog, string, error) {
	catalog, generation, err := readCatalogOrEmpty(ctx, c.store)
	if err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog = catalog
	c.generation = generation
	return catalog, generation, nil
}

func readCatalogOrEmpty(ctx context.Context, store objectstore.Store) (*namespacepb.TenantCatalog, string, error) {
	catalog, generation, err := ReadTenantCatalog(ctx, store)
	if err == nil {
		return catalog, generation, nil
	}
	if !errors.Is(err, objectstore.ErrNotFound) {
		return nil, "", err
	}
	return &namespacepb.TenantCatalog{
		Namespaces: make(map[string]*namespacepb.NamespaceMetadata),
	}, "", nil
}

func putTenantCatalog(ctx context.Context, store objectstore.Store, catalog *namespacepb.TenantCatalog, generation string) (string, error) {
	data, err := marshalOpts.Marshal(catalog)
	if err != nil {
		return "", fmt.Errorf("namespace: marshal catalog: %w", err)
	}
	cond := objectstore.Condition{Absent: true}
	if generation != "" {
		cond = objectstore.Condition{GenerationMatch: generation}
	}
	return store.Put(ctx, CatalogFile, bytes.NewReader(data), cond)
}
