package kvfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/iampat/cloudy-neigh/objectstore"
)

var (
	ErrBranchAlreadyExists = errors.New("kvfs: branch already exists")
	ErrInvalidBranchName   = errors.New("kvfs: invalid branch name")
)

const refPrefix = "refs/heads/"

func validateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("%w: empty branch name", ErrInvalidBranchName)
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") {
		return fmt.Errorf("%w: %q", ErrInvalidBranchName, branch)
	}
	return nil
}

func branchKey(branch string) string {
	return refPrefix + branch
}

func ResolveBranch(ctx context.Context, store objectstore.Store, branch string) (string, string, error) {
	if err := validateBranch(branch); err != nil {
		return "", "", err
	}

	rc, obj, err := store.Get(ctx, branchKey(branch))
	if err != nil {
		return "", "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", "", fmt.Errorf("kvfs: read branch ref %s: %w", branch, err)
	}

	hash := strings.TrimSpace(string(data))
	if err := validateHash(hash); err != nil {
		return "", "", fmt.Errorf("kvfs: corrupt branch ref %s: %w", branch, err)
	}
	return hash, obj.Generation, nil
}

func UpdateBranch(ctx context.Context, store objectstore.Store, branch, manifestHash, expectedGen string) (string, error) {
	if err := validateBranch(branch); err != nil {
		return "", err
	}
	if err := validateHash(manifestHash); err != nil {
		return "", err
	}

	var cond objectstore.Condition
	if expectedGen == "" {
		cond = objectstore.Condition{Absent: true}
	} else {
		cond = objectstore.Condition{GenerationMatch: expectedGen}
	}

	gen, err := store.Put(ctx, branchKey(branch), strings.NewReader(manifestHash), cond)
	if err != nil {
		return "", err
	}
	return gen, nil
}

func CreateBranch(ctx context.Context, store objectstore.Store, newBranch, parentBranch string) (string, string, error) {
	if err := validateBranch(newBranch); err != nil {
		return "", "", err
	}
	if err := validateBranch(parentBranch); err != nil {
		return "", "", err
	}

	parentHash, _, err := ResolveBranch(ctx, store, parentBranch)
	if err != nil {
		return "", "", fmt.Errorf("kvfs: resolve parent branch %s: %w", parentBranch, err)
	}

	gen, err := store.Put(ctx, branchKey(newBranch), strings.NewReader(parentHash), objectstore.Condition{Absent: true})
	if err != nil {
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			return "", "", ErrBranchAlreadyExists
		}
		return "", "", fmt.Errorf("kvfs: create branch %s: %w", newBranch, err)
	}
	return parentHash, gen, nil
}
