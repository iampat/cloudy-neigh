package manifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"google.golang.org/protobuf/proto"
)

var (
	ErrBranchAlreadyExists = errors.New("manifest: branch already exists")
	ErrNilManifest         = errors.New("manifest: nil manifest")
)

func Read(ctx context.Context, store objectstore.Store, key string) (*storagepb.BranchManifest, string, error) {
	rc, obj, err := store.Get(ctx, key)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("manifest: read %s: %w", key, err)
	}

	var m storagepb.BranchManifest
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, "", fmt.Errorf("manifest: corrupt manifest %s: %w", key, err)
	}
	return &m, obj.Generation, nil
}

func Write(ctx context.Context, store objectstore.Store, key string, m *storagepb.BranchManifest, expectedGen string) (string, error) {
	if m == nil {
		return "", ErrNilManifest
	}

	data, err := proto.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("manifest: marshal manifest %s: %w", key, err)
	}

	var cond objectstore.Condition
	if expectedGen == "" {
		cond = objectstore.Condition{Absent: true}
	} else {
		cond = objectstore.Condition{GenerationMatch: expectedGen}
	}

	return store.Put(ctx, key, bytes.NewReader(data), cond)
}
