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

const DefaultListLimit = 1000

func ListBranches(ctx context.Context, store objectstore.Store) ([]string, error) {
	var branches []string
	var currStart string

	for {
		objs, err := store.List(ctx, "", currStart, DefaultListLimit)
		if err != nil {
			return nil, fmt.Errorf("kvfs: list branches: %w", err)
		}
		if len(objs) == 0 {
			break
		}
		for _, obj := range objs {
			if strings.Contains(obj.Key, "/"+namespace.RefHead+"/") || strings.HasPrefix(obj.Key, namespace.RefHead+"/") {
				branches = append(branches, obj.Key)
			}
		}
		if len(objs) < DefaultListLimit {
			break
		}
		currStart = objs[len(objs)-1].Key
	}
	return branches, nil
}

func ResolveBranch(ctx context.Context, store objectstore.Store, key string) (*storagepb.BranchManifest, string, error) {
	rc, obj, err := store.Get(ctx, key)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("kvfs: read branch ref %s: %w", key, err)
	}

	var m storagepb.BranchManifest
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, "", fmt.Errorf("kvfs: corrupt branch manifest %s: %w", key, err)
	}
	return &m, obj.Generation, nil
}

func UpdateBranch(ctx context.Context, store objectstore.Store, key string, m *storagepb.BranchManifest, expectedGen string) (string, error) {
	if m == nil {
		return "", ErrNilManifest
	}

	data, err := proto.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("kvfs: marshal branch manifest %s: %w", key, err)
	}

	var cond objectstore.Condition
	if expectedGen == "" {
		cond = objectstore.Condition{Absent: true}
	} else {
		cond = objectstore.Condition{GenerationMatch: expectedGen}
	}

	gen, err := store.Put(ctx, key, bytes.NewReader(data), cond)
	if err != nil {
		return "", err
	}
	return gen, nil
}

func CreateBranch(ctx context.Context, store objectstore.Store, newKey, parentKey string) (*storagepb.BranchManifest, string, error) {
	parentManifest, _, err := ResolveBranch(ctx, store, parentKey)
	if err != nil {
		return nil, "", fmt.Errorf("kvfs: resolve parent branch %s: %w", parentKey, err)
	}

	data, err := proto.Marshal(parentManifest)
	if err != nil {
		return nil, "", fmt.Errorf("kvfs: marshal fork manifest %s: %w", newKey, err)
	}

	gen, err := store.Put(ctx, newKey, bytes.NewReader(data), objectstore.Condition{Absent: true})
	if err != nil {
		if errors.Is(err, objectstore.ErrPreconditionFailed) {
			return nil, "", ErrBranchAlreadyExists
		}
		return nil, "", fmt.Errorf("kvfs: create branch %s: %w", newKey, err)
	}
	return parentManifest, gen, nil
}

func DeleteBranch(ctx context.Context, store objectstore.Store, key string) error {
	return store.Delete(ctx, key)
}
