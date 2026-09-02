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

var ErrInvalidHash = errors.New("kvfs: invalid CAS hash")

const (
	casPrefix      = "cas/"
	manifestPrefix = "manifests/"
	hashHexLen     = sha256.Size * 2
)

func validateHash(hash string) error {
	if len(hash) != hashHexLen {
		return fmt.Errorf("%w: length %d, want %d", ErrInvalidHash, len(hash), hashHexLen)
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("%w: invalid hex character %q", ErrInvalidHash, c)
		}
	}
	return nil
}

func shardedKey(prefix, hash string) string {
	return prefix + hash[:2] + "/" + hash[2:4] + "/" + hash
}

func putCAS(ctx context.Context, store objectstore.Store, prefix string, r io.Reader) (string, int64, error) {
	var buf bytes.Buffer
	h := sha256.New()
	w := io.MultiWriter(&buf, h)
	n, err := io.Copy(w, r)
	if err != nil {
		return "", 0, fmt.Errorf("kvfs: read payload: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	key := shardedKey(prefix, hash)

	_, err = store.Put(ctx, key, &buf, objectstore.Condition{Absent: true})
	if err != nil && !errors.Is(err, objectstore.ErrPreconditionFailed) {
		return "", 0, fmt.Errorf("kvfs: put %s %s: %w", prefix, hash, err)
	}
	return hash, n, nil
}

func getCAS(ctx context.Context, store objectstore.Store, prefix, hash string) (io.ReadCloser, int64, error) {
	if err := validateHash(hash); err != nil {
		return nil, 0, err
	}
	rc, obj, err := store.Get(ctx, shardedKey(prefix, hash))
	if err != nil {
		return nil, 0, err
	}
	return rc, obj.Size, nil
}

func existsCAS(ctx context.Context, store objectstore.Store, prefix, hash string) (bool, error) {
	if err := validateHash(hash); err != nil {
		return false, err
	}
	return store.Exists(ctx, shardedKey(prefix, hash))
}

func PutBlob(ctx context.Context, store objectstore.Store, r io.Reader) (string, int64, error) {
	return putCAS(ctx, store, casPrefix, r)
}

func GetBlob(ctx context.Context, store objectstore.Store, hash string) (io.ReadCloser, int64, error) {
	return getCAS(ctx, store, casPrefix, hash)
}

func ExistsBlob(ctx context.Context, store objectstore.Store, hash string) (bool, error) {
	return existsCAS(ctx, store, casPrefix, hash)
}
