package namespace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/iampat/cloudy-neigh/objectstore"
	namespacepb "github.com/iampat/cloudy-neigh/proto/namespace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type Registry struct {
	stores  map[string]objectstore.Store
	tenants []string
}

func NewRegistry(stores map[string]objectstore.Store) (*Registry, error) {
	if len(stores) == 0 {
		return nil, errors.New("namespace: empty stores map")
	}
	copied := make(map[string]objectstore.Store, len(stores))
	tenants := make([]string, 0, len(stores))
	for tenant, store := range stores {
		if err := ValidateName(tenant); err != nil {
			return nil, err
		}
		if store == nil {
			return nil, fmt.Errorf("namespace: nil store for tenant %q", tenant)
		}
		copied[tenant] = store
		tenants = append(tenants, tenant)
	}
	slices.Sort(tenants)
	return &Registry{
		stores:  copied,
		tenants: tenants,
	}, nil
}

func LoadRegistry(ctx context.Context, path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("namespace: read tenants file: %w", err)
	}

	var cfg namespacepb.TenantsConfigFile
	if err := protojson.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("namespace: unmarshal tenants file: %w", err)
	}
	if len(cfg.GetTenants()) == 0 {
		return nil, fmt.Errorf("namespace: no tenants defined in %s", path)
	}

	opened := make(map[string]objectstore.Store, len(cfg.GetTenants()))
	for tenant, tcfg := range cfg.GetTenants() {
		storageURL := tcfg.GetStorageUrl()
		if storageURL == "" {
			closeOpenedStores(opened)
			return nil, fmt.Errorf("namespace: tenant %q missing storage_url", tenant)
		}
		store, err := objectstore.Open(ctx, storageURL)
		if err != nil {
			closeOpenedStores(opened)
			return nil, fmt.Errorf("namespace: open store for tenant %q: %w", tenant, err)
		}
		opened[tenant] = store
	}

	reg, err := NewRegistry(opened)
	if err != nil {
		closeOpenedStores(opened)
		return nil, err
	}
	return reg, nil
}

func closeOpenedStores(stores map[string]objectstore.Store) {
	for _, store := range stores {
		_ = store.Close()
	}
}

func (r *Registry) Store(tenant string) (objectstore.Store, bool) {
	store, ok := r.stores[tenant]
	return store, ok
}

func (r *Registry) Stores() map[string]objectstore.Store {
	return r.stores
}

func (r *Registry) Tenants() []string {
	return r.tenants
}

func (r *Registry) Close() error {
	var err error
	for _, store := range r.stores {
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	return err
}
