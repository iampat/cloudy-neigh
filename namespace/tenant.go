package namespace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/iampat/cloudy-neigh/objectstore"
)

const TenantsFile = "tenants.json"

func ReadTenantsJSON(ctx context.Context, store objectstore.Store) ([]string, string, error) {
	rc, obj, err := store.Get(ctx, TenantsFile)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("namespace: read tenants: %w", err)
	}
	var tenants []string
	if err := json.Unmarshal(data, &tenants); err != nil {
		return nil, "", fmt.Errorf("namespace: unmarshal tenants: %w", err)
	}
	return tenants, obj.Generation, nil
}

func WriteTenantsJSON(ctx context.Context, store objectstore.Store, tenants []string, expectedGen string) error {
	data, err := json.Marshal(tenants)
	if err != nil {
		return fmt.Errorf("namespace: marshal tenants: %w", err)
	}
	var cond objectstore.Condition
	if expectedGen == "" {
		cond = objectstore.Condition{Absent: true}
	} else {
		cond = objectstore.Condition{GenerationMatch: expectedGen}
	}
	_, err = store.Put(ctx, TenantsFile, bytes.NewReader(data), cond)
	return err
}

func ListTenants(ctx context.Context, store objectstore.Store) ([]string, error) {
	tenants, _, err := ReadTenantsJSON(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("namespace: list tenants: %w", err)
	}
	if len(tenants) == 0 {
		return []string{DefaultTenant}, nil
	}
	return tenants, nil
}

func AddTenant(ctx context.Context, store objectstore.Store, tenant string) error {
	if err := ValidateName(tenant); err != nil {
		return err
	}
	for {
		tenants, gen, err := ReadTenantsJSON(ctx, store)
		if err != nil {
			return fmt.Errorf("namespace: add tenant %s: %w", tenant, err)
		}
		for _, t := range tenants {
			if t == tenant {
				return nil
			}
		}
		tenants = append(tenants, tenant)
		err = WriteTenantsJSON(ctx, store, tenants, gen)
		if err == nil {
			return nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return fmt.Errorf("namespace: write tenants: %w", err)
	}
}
