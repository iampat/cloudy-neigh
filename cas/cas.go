package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

type Digest [sha256.Size]byte

func (d Digest) String() string {
	return hex.EncodeToString(d[:])
}

func (d Digest) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Digest) UnmarshalText(text []byte) error {
	parsed, err := ParseDigest(string(text))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func ParseDigest(s string) (Digest, error) {
	var d Digest
	if len(s) != hex.EncodedLen(sha256.Size) {
		return Digest{}, fmt.Errorf("cas: digest %q has %d characters, want %d",
			s, len(s), hex.EncodedLen(sha256.Size))
	}
	if _, err := hex.Decode(d[:], []byte(s)); err != nil {
		return Digest{}, fmt.Errorf("cas: parse digest %q: %w", s, err)
	}
	return d, nil
}

var ErrNotFound = errors.New("cas: blob not found")

// The root is the only cell that changes. An update to the whole store is one
// swap of it, and a caller that reaches the root reaches a complete state.
type Store interface {
	Put(data []byte) (Digest, error)
	Get(d Digest) ([]byte, error)
	// Concurrent calls race. The caller serializes them.
	SetRoot(d Digest) error
	Root() (Digest, bool, error)
}
