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
	casPrefix  = "cas/"
	hashHexLen = sha256.Size * 2
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

func casKey(hash string) string {
	return casPrefix + hash[:2] + "/" + hash[2:4] + "/" + hash
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
	key := casKey(hash)

	_, err = store.Put(ctx, key, &buf, objectstore.Condition{Absent: true})
	if err != nil && !errors.Is(err, objectstore.ErrPreconditionFailed) {
		return "", 0, fmt.Errorf("kvfs: put blob %s: %w", hash, err)
	}
	return hash, n, nil
}

func GetBlob(ctx context.Context, store objectstore.Store, hash string) (io.ReadCloser, int64, error) {
	if err := validateHash(hash); err != nil {
		return nil, 0, err
	}
	rc, obj, err := store.Get(ctx, casKey(hash))
	if err != nil {
		return nil, 0, err
	}
	return rc, obj.Size, nil
}

func ExistsBlob(ctx context.Context, store objectstore.Store, hash string) (bool, error) {
	if err := validateHash(hash); err != nil {
		return false, err
	}
	return store.Exists(ctx, casKey(hash))
}
