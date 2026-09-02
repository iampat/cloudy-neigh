package kvfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
	"google.golang.org/protobuf/proto"
)

const manifestPrefix = "manifests/"

func manifestKey(hash string) string {
	return manifestPrefix + hash[:2] + "/" + hash[2:4] + "/" + hash
}

func PutManifest(ctx context.Context, store objectstore.Store, m *kvfspb.Manifest) (string, error) {
	data, err := proto.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("kvfs: marshal manifest: %w", err)
	}

	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])
	key := manifestKey(hash)

	_, err = store.Put(ctx, key, bytes.NewReader(data), objectstore.Condition{Absent: true})
	if err != nil && !errors.Is(err, objectstore.ErrPreconditionFailed) {
		return "", fmt.Errorf("kvfs: put manifest %s: %w", hash, err)
	}
	return hash, nil
}

func GetManifest(ctx context.Context, store objectstore.Store, hash string) (*kvfspb.Manifest, error) {
	if err := validateHash(hash); err != nil {
		return nil, err
	}

	rc, _, err := store.Get(ctx, manifestKey(hash))
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
