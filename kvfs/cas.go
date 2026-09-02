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
)

const (
	CASPrefix  = "cas/"
	HashHexLen = sha256.Size * 2
)

func CASKey(hash string) string {
	if len(hash) != HashHexLen {
		return CASPrefix + hash
	}
	return CASPrefix + hash[:2] + "/" + hash[2:4] + "/" + hash
}

func PutBlob(ctx context.Context, store objectstore.Store, r io.Reader) (string, int64, error) {
	var buf bytes.Buffer
	h := sha256.New()
	w := io.MultiWriter(&buf, h)
	n, err := io.Copy(w, r)
	if err != nil {
		return "", 0, fmt.Errorf("kvfs: read blob payload: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	key := CASKey(hash)

	_, err = store.Put(ctx, key, &buf, objectstore.Condition{Absent: true})
	if err != nil && !errors.Is(err, objectstore.ErrPreconditionFailed) {
		return "", 0, fmt.Errorf("kvfs: put blob %s: %w", hash, err)
	}
	return hash, n, nil
}

func GetBlob(ctx context.Context, store objectstore.Store, hash string) (io.ReadCloser, int64, error) {
	key := CASKey(hash)
	rc, obj, err := store.Get(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return rc, obj.Size, nil
}

func ExistsBlob(ctx context.Context, store objectstore.Store, hash string) (bool, error) {
	return store.Exists(ctx, CASKey(hash))
}
