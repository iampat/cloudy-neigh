// Package index answers which document a namespace holds under an ID.
//
// It keeps a manifest of ID to digest, stores it as a blob like any other, and
// points the store's root at it. Every write is one swap of that root.
package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/iampat/cloudy-neigh/cas"
	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
)

var ErrNotFound = errors.New("index: document not found")

type manifest map[string]map[string]cas.Digest

func (m manifest) clone() manifest {
	next := make(manifest, len(m))
	for namespace, docs := range m {
		next[namespace] = maps.Clone(docs)
	}
	return next
}

// Protobuf leaves attribute order unstable, so a write of unchanged content
// stores another blob.
//
// Protobuf promises the order only within a build. A toolchain upgrade can give
// a stored document a new digest, which costs deduplication rather than
// correctness: the manifest records whatever digest a write produced, and
// nothing here reads equal digests as proof that two documents are equal.
var docCodec = proto.MarshalOptions{Deterministic: true}

type Store struct {
	blobs cas.Store

	// mu serialises the read-modify-write of the manifest. A published
	// manifest is never mutated, so a reader holds mu only for the map read
	// and does its blob read outside.
	mu       sync.Mutex
	manifest manifest
}

func Open(blobs cas.Store) (*Store, error) {
	root, ok, err := blobs.Root()
	if err != nil {
		return nil, fmt.Errorf("read root: %w", err)
	}

	m := manifest{}
	if ok {
		body, err := blobs.Get(root)
		if err != nil {
			return nil, fmt.Errorf("read manifest %s: %w", root, err)
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("decode manifest %s: %w", root, err)
		}
	}
	return &Store{blobs: blobs, manifest: m}, nil
}

// Upsert applies every document or none of them.
//
// The order below is the durability argument. Document blobs reach the disk
// first, then the manifest that names them, and only then the root that names
// the manifest. A crash before the root swap leaves the namespace exactly as it
// was, with unreachable blobs as the only trace.
func (s *Store) Upsert(ctx context.Context, namespace string, docs []*cloudyneigh.Document) error {
	if len(docs) == 0 {
		return nil
	}

	// Keep the bulk I/O off the commit lock.
	digests := make(map[string]cas.Digest, len(docs))
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := docCodec.Marshal(doc)
		if err != nil {
			return fmt.Errorf("marshal document %q: %w", doc.GetId(), err)
		}
		digest, err := s.blobs.Put(body)
		if err != nil {
			return fmt.Errorf("store document %q: %w", doc.GetId(), err)
		}
		digests[doc.GetId()] = digest
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// A writer that waited for the lock may have lost its caller by now. Commit
	// nothing on its behalf.
	if err := ctx.Err(); err != nil {
		return err
	}

	next := s.manifest.clone()
	entries, ok := next[namespace]
	if !ok {
		entries = make(map[string]cas.Digest, len(digests))
		next[namespace] = entries
	}
	maps.Copy(entries, digests)

	body, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	root, err := s.blobs.Put(body)
	if err != nil {
		return fmt.Errorf("store manifest: %w", err)
	}
	if err := s.blobs.SetRoot(root); err != nil {
		return fmt.Errorf("set root: %w", err)
	}
	s.manifest = next
	return nil
}

func (s *Store) Lookup(ctx context.Context, namespace, id string) (*cloudyneigh.Document, error) {
	s.mu.Lock()
	digest, ok := s.manifest[namespace][id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, namespace, id)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	body, err := s.blobs.Get(digest)
	if err != nil {
		return nil, fmt.Errorf("read document %q: %w", id, err)
	}
	doc := &cloudyneigh.Document{}
	if err := proto.Unmarshal(body, doc); err != nil {
		return nil, fmt.Errorf("decode document %q: %w", id, err)
	}
	return doc, nil
}
