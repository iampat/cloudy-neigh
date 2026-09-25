package namespace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/iampat/cloudy-neigh/objectstore"
	namespacepb "github.com/iampat/cloudy-neigh/proto/namespace/v1"
)

func (s Scope) ListBranches(ctx context.Context, store objectstore.Store) ([]string, error) {
	catalog, _, err := readBranchCatalog(ctx, store, s.branchesKey())
	if err != nil {
		return nil, fmt.Errorf("namespace: list branches: %w", err)
	}
	if len(catalog.Branches) == 0 {
		return []string{DefaultBranch}, nil
	}
	return slices.Sorted(maps.Keys(catalog.Branches)), nil
}

func (s Scope) AddBranch(ctx context.Context, store objectstore.Store, branch string) error {
	return s.updateBranches(ctx, store, func(branches map[string]*namespacepb.BranchMetadata) bool {
		if _, ok := branches[branch]; ok {
			return false
		}
		branches[branch] = &namespacepb.BranchMetadata{}
		return true
	})
}

func (s Scope) RemoveBranch(ctx context.Context, store objectstore.Store, branch string) error {
	return s.updateBranches(ctx, store, func(branches map[string]*namespacepb.BranchMetadata) bool {
		if _, ok := branches[branch]; !ok {
			return false
		}
		delete(branches, branch)
		return true
	})
}

func (s Scope) updateBranches(ctx context.Context, store objectstore.Store, change func(map[string]*namespacepb.BranchMetadata) bool) error {
	key := s.branchesKey()
	for {
		catalog, gen, err := readBranchCatalog(ctx, store, key)
		if err != nil {
			return err
		}
		if !change(catalog.Branches) {
			return nil
		}
		data, err := marshalOpts.Marshal(catalog)
		if err != nil {
			return fmt.Errorf("namespace: marshal branches: %w", err)
		}
		cond := objectstore.Condition{Absent: true}
		if gen != "" {
			cond = objectstore.Condition{GenerationMatch: gen}
		}
		_, err = store.Put(ctx, key, bytes.NewReader(data), cond)
		if err == nil {
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return fmt.Errorf("namespace: write branches %s: %w", key, err)
		}
	}
}

func readBranchCatalog(ctx context.Context, store objectstore.Store, key string) (*namespacepb.BranchCatalog, string, error) {
	catalog := &namespacepb.BranchCatalog{Branches: make(map[string]*namespacepb.BranchMetadata)}
	rc, obj, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return catalog, "", nil
		}
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("namespace: read branches %s: %w", key, err)
	}
	if err := unmarshalOpts.Unmarshal(data, catalog); err != nil {
		return nil, "", fmt.Errorf("namespace: unmarshal branches %s: %w", key, err)
	}
	if catalog.Branches == nil {
		catalog.Branches = make(map[string]*namespacepb.BranchMetadata)
	}
	return catalog, obj.Generation, nil
}
