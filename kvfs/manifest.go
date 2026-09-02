package kvfs

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
	"google.golang.org/protobuf/proto"
)

const manifestPrefix = "manifests/"

func putManifest(ctx context.Context, store objectstore.Store, m *kvfspb.Manifest) (string, error) {
	data, err := proto.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("kvfs: marshal manifest: %w", err)
	}
	hash, _, err := putCAS(ctx, store, manifestPrefix, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	return hash, nil
}

func getManifest(ctx context.Context, store objectstore.Store, hash string) (*kvfspb.Manifest, error) {
	rc, _, err := getCAS(ctx, store, manifestPrefix, hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("kvfs: read manifest %s: %w", hash, err)
	}

	var m kvfspb.Manifest
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("kvfs: unmarshal manifest %s: %w", hash, err)
	}
	return &m, nil
}
