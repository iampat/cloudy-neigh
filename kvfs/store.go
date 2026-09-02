package kvfs

import (
	"context"
	"errors"
	"io"
	"maps"

	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
)

var (
	ErrInvalidKey = errors.New("kvfs: empty key")
	ErrNilStore   = errors.New("kvfs: nil objectstore")
	ErrNilReader  = errors.New("kvfs: nil reader")
)

type Value struct {
	Data io.ReadCloser
	Size int64
}

type Store interface {
	Get(ctx context.Context, branch, key string) (Value, error)
	Set(ctx context.Context, branch, key string, r io.Reader) error
	Delete(ctx context.Context, branch, key string) error
	Branch(ctx context.Context, newBranch, parentBranch string) error
	Close() error
}

type client struct {
	store objectstore.Store
}

func Open(store objectstore.Store) (Store, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	return &client{store: store}, nil
}

func (c *client) Close() error {
	return c.store.Close()
}

func (c *client) Branch(ctx context.Context, newBranch, parentBranch string) error {
	_, _, err := createBranch(ctx, c.store, newBranch, parentBranch)
	return err
}

func (c *client) Get(ctx context.Context, branch, key string) (Value, error) {
	if key == "" {
		return Value{}, ErrInvalidKey
	}

	manifestHash, _, err := resolveBranch(ctx, c.store, branch)
	if err != nil {
		return Value{}, err
	}

	manifest, err := getManifest(ctx, c.store, manifestHash)
	if err != nil {
		return Value{}, err
	}

	entry, ok := manifest.Entries[key]
	if !ok {
		return Value{}, objectstore.ErrNotFound
	}

	rc, size, err := getBlob(ctx, c.store, entry.CasHash)
	if err != nil {
		return Value{}, err
	}
	return Value{Data: rc, Size: size}, nil
}

func (c *client) Set(ctx context.Context, branch, key string, r io.Reader) error {
	if err := validateBranch(branch); err != nil {
		return err
	}
	if key == "" {
		return ErrInvalidKey
	}
	if r == nil {
		return ErrNilReader
	}

	casHash, size, err := putBlob(ctx, c.store, r)
	if err != nil {
		return err
	}

	entry := &kvfspb.ManifestEntry{
		CasHash:   casHash,
		SizeBytes: uint64(size),
	}

	for {
		manifestHash, gen, err := resolveBranch(ctx, c.store, branch)
		if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			return err
		}

		var (
			newEntries map[string]*kvfspb.ManifestEntry
			lastWalSeq uint64
		)

		if errors.Is(err, objectstore.ErrNotFound) {
			newEntries = map[string]*kvfspb.ManifestEntry{key: entry}
		} else {
			parentManifest, err := getManifest(ctx, c.store, manifestHash)
			if err != nil {
				return err
			}
			newEntries = make(map[string]*kvfspb.ManifestEntry, len(parentManifest.Entries)+1)
			maps.Copy(newEntries, parentManifest.Entries)
			newEntries[key] = entry
			lastWalSeq = parentManifest.LastWalSeq
		}

		newManifest := &kvfspb.Manifest{
			LastWalSeq: lastWalSeq,
			Entries:    newEntries,
		}

		newManifestHash, err := putManifest(ctx, c.store, newManifest)
		if err != nil {
			return err
		}

		_, err = updateBranch(ctx, c.store, branch, newManifestHash, gen)
		if err == nil {
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return err
		}
	}
}

func (c *client) Delete(ctx context.Context, branch, key string) error {
	if key == "" {
		return ErrInvalidKey
	}

	for {
		manifestHash, gen, err := resolveBranch(ctx, c.store, branch)
		if err != nil {
			return err
		}

		parentManifest, err := getManifest(ctx, c.store, manifestHash)
		if err != nil {
			return err
		}

		if _, ok := parentManifest.Entries[key]; !ok {
			return objectstore.ErrNotFound
		}

		newEntries := make(map[string]*kvfspb.ManifestEntry, len(parentManifest.Entries)-1)
		for k, v := range parentManifest.Entries {
			if k != key {
				newEntries[k] = v
			}
		}

		newManifest := &kvfspb.Manifest{
			LastWalSeq: parentManifest.LastWalSeq,
			Entries:    newEntries,
		}

		newManifestHash, err := putManifest(ctx, c.store, newManifest)
		if err != nil {
			return err
		}

		_, err = updateBranch(ctx, c.store, branch, newManifestHash, gen)
		if err == nil {
			return nil
		}
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			return err
		}
	}
}
