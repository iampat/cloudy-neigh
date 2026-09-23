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

func ReadBranchesJSON(ctx context.Context, store objectstore.Store, path string) ([]string, string, error) {
	rc, obj, err := store.Get(ctx, path)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, "", nil
		}
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("namespace: read branches %s: %w", path, err)
	}
	var branches []string
	if err := json.Unmarshal(data, &branches); err != nil {
		return nil, "", fmt.Errorf("namespace: unmarshal branches %s: %w", path, err)
	}
	return branches, obj.Generation, nil
}

func WriteBranchesJSON(ctx context.Context, store objectstore.Store, path string, branches []string, expectedGen string) error {
	data, err := json.Marshal(branches)
	if err != nil {
		return fmt.Errorf("namespace: marshal branches: %w", err)
	}
	var cond objectstore.Condition
	if expectedGen == "" {
		cond = objectstore.Condition{Absent: true}
	} else {
		cond = objectstore.Condition{GenerationMatch: expectedGen}
	}
	_, err = store.Put(ctx, path, bytes.NewReader(data), cond)
	return err
}

func (s Scope) ListBranches(ctx context.Context, store objectstore.Store) ([]string, error) {
	branches, _, err := ReadBranchesJSON(ctx, store, s.BranchesPath())
	if err != nil {
		return nil, fmt.Errorf("namespace: list branches: %w", err)
	}
	if len(branches) == 0 {
		return []string{s.BranchRef(DefaultBranch)}, nil
	}
	return branches, nil
}

func (s Scope) AddBranch(ctx context.Context, store objectstore.Store, branch string) error {
	p := s.BranchesPath()
	for {
		branches, gen, err := ReadBranchesJSON(ctx, store, p)
		if err != nil {
			return fmt.Errorf("namespace: add branch %s: %w", branch, err)
		}
		for _, b := range branches {
			if b == branch {
				return nil
			}
		}
		branches = append(branches, branch)
		err = WriteBranchesJSON(ctx, store, p, branches, gen)
		if err == nil {
			return nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return fmt.Errorf("namespace: write branches %s: %w", p, err)
	}
}

func (s Scope) RemoveBranch(ctx context.Context, store objectstore.Store, branch string) error {
	p := s.BranchesPath()
	for {
		branches, gen, err := ReadBranchesJSON(ctx, store, p)
		if err != nil {
			return fmt.Errorf("namespace: remove branch %s: %w", branch, err)
		}
		if len(branches) == 0 {
			return nil
		}
		newBranches := make([]string, 0, len(branches))
		for _, b := range branches {
			if b != branch {
				newBranches = append(newBranches, b)
			}
		}
		err = WriteBranchesJSON(ctx, store, p, newBranches, gen)
		if err == nil {
			return nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return fmt.Errorf("namespace: write branches %s: %w", p, err)
	}
}

const BranchesFile = "branches.json"

func ListBranches(ctx context.Context, store objectstore.Store, branchesPath string) ([]string, error) {
	if branchesPath == "" {
		branchesPath = BranchesFile
	}
	branches, _, err := ReadBranchesJSON(ctx, store, branchesPath)
	if err != nil {
		return nil, fmt.Errorf("namespace: list branches: %w", err)
	}
	if len(branches) == 0 {
		return []string{BranchRef("", "", DefaultBranch)}, nil
	}
	return branches, nil
}

func AddBranch(ctx context.Context, store objectstore.Store, branchesPath, branch string) error {
	if branchesPath == "" {
		branchesPath = BranchesFile
	}
	for {
		branches, gen, err := ReadBranchesJSON(ctx, store, branchesPath)
		if err != nil {
			return fmt.Errorf("namespace: add branch %s: %w", branch, err)
		}
		for _, b := range branches {
			if b == branch {
				return nil
			}
		}
		branches = append(branches, branch)
		err = WriteBranchesJSON(ctx, store, branchesPath, branches, gen)
		if err == nil {
			return nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return fmt.Errorf("namespace: write branches %s: %w", branchesPath, err)
	}
}

func RemoveBranch(ctx context.Context, store objectstore.Store, branchesPath, branch string) error {
	if branchesPath == "" {
		branchesPath = BranchesFile
	}
	for {
		branches, gen, err := ReadBranchesJSON(ctx, store, branchesPath)
		if err != nil {
			return fmt.Errorf("namespace: remove branch %s: %w", branch, err)
		}
		if len(branches) == 0 {
			return nil
		}
		newBranches := make([]string, 0, len(branches))
		for _, b := range branches {
			if b != branch {
				newBranches = append(newBranches, b)
			}
		}
		err = WriteBranchesJSON(ctx, store, branchesPath, newBranches, gen)
		if err == nil {
			return nil
		}
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			continue
		}
		return fmt.Errorf("namespace: write branches %s: %w", branchesPath, err)
	}
}
