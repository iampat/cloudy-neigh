package kvfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"google.golang.org/protobuf/proto"
)

var (
	ErrBranchAlreadyExists = errors.New("kvfs: branch already exists")
	ErrNilManifest         = errors.New("kvfs: nil manifest")
)

const (
	refPrefix        = "refs/heads/"
	DefaultListLimit = 1000
)

func branchKey(branch string) string {
	return refPrefix + branch
}

func ListBranches(ctx context.Context, store objectstore.Store, startAfter string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	var startKey string
	if startAfter != "" {
		startKey = branchKey(startAfter)
	}
	objs, err := store.List(ctx, refPrefix, startKey, limit)
	if err != nil {
		return nil, fmt.Errorf("kvfs: list branches: %w", err)
	}
	branches := make([]string, 0, len(objs))
	for _, obj := range objs {
		branch := strings.TrimPrefix(obj.Key, refPrefix)
		if branch != "" {
			branches = append(branches, branch)
		}
	}
	return branches, nil
}

func ResolveBranch(ctx context.Context, store objectstore.Store, branch string) (*storagepb.BranchManifest, string, error) {
	if err := namespace.ValidateBranch(branch); err != nil {
		return nil, "", err
	}

	rc, obj, err := store.Get(ctx, branchKey(branch))
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("kvfs: read branch ref %s: %w", branch, err)
	}

	var m storagepb.BranchManifest
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, "", fmt.Errorf("kvfs: corrupt branch manifest %s: %w", branch, err)
	}
	return &m, obj.Generation, nil
}

func UpdateBranch(ctx context.Context, store objectstore.Store, branch string, m *storagepb.BranchManifest, expectedGen string) (string, error) {
	if err := namespace.ValidateBranch(branch); err != nil {
		return "", err
	}
	if m == nil {
		return "", ErrNilManifest
	}

	data, err := proto.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("kvfs: marshal branch manifest %s: %w", branch, err)
	}

	var cond objectstore.Condition
	if expectedGen == "" {
		cond = objectstore.Condition{Absent: true}
	} else {
		cond = objectstore.Condition{GenerationMatch: expectedGen}
	}

	gen, err := store.Put(ctx, branchKey(branch), bytes.NewReader(data), cond)
	if err != nil {
		return "", err
	}
	return gen, nil
}

func CreateBranch(ctx context.Context, store objectstore.Store, newBranch, parentBranch string) (*storagepb.BranchManifest, string, error) {
	if err := namespace.ValidateBranch(newBranch); err != nil {
		return nil, "", err
	}
	if err := namespace.ValidateBranch(parentBranch); err != nil {
		return nil, "", err
	}

	parentManifest, _, err := ResolveBranch(ctx, store, parentBranch)
	if err != nil {
		return nil, "", fmt.Errorf("kvfs: resolve parent branch %s: %w", parentBranch, err)
	}

	data, err := proto.Marshal(parentManifest)
	if err != nil {
		return nil, "", fmt.Errorf("kvfs: marshal fork manifest %s: %w", newBranch, err)
	}

	gen, err := store.Put(ctx, branchKey(newBranch), bytes.NewReader(data), objectstore.Condition{Absent: true})
	if err != nil {
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			return nil, "", ErrBranchAlreadyExists
		}
		return nil, "", fmt.Errorf("kvfs: create branch %s: %w", newBranch, err)
	}
	return parentManifest, gen, nil
}

func DeleteBranch(ctx context.Context, store objectstore.Store, branch string) error {
	if err := namespace.ValidateBranch(branch); err != nil {
		return err
	}
	return store.Delete(ctx, branchKey(branch))
}
