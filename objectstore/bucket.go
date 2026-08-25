package objectstore

import (
	"context"

	"gocloud.dev/blob"
)

// bucket is the internal seam between Store and a backend. It is unexported
// and exists for developers who add a backend, not for users of the package.
//
// Store.Put drives a bucket in this order:
//
//	defer s.bucket.lock()()
//	opts, generation, err := s.bucket.writeOptions(ctx, key, cond)
//	w, err := s.b.NewWriter(ctx, key, opts)
//	io.Copy(w, r)
//	w.Close()
//	return generation() // the token of the completed write
//
// GCS puts the precondition in opts, and the server checks it inside the
// write. The local backend has no server check. It checks the precondition
// itself in writeOptions, under the lock, and returns nil opts.
//
// Store.Get and Store.List take no lock. The token rides the read itself:
//
//	r, err := s.b.NewReader(ctx, key, nil)
//	gen, err := s.bucket.generation(r)
type bucket interface {
	lock() (unlock func())
	writeOptions(ctx context.Context, key string, cond *Condition) (*blob.WriterOptions, func() (string, error), error)
	generation(r *blob.Reader) (string, error)
	listGeneration(o *blob.ListObject) string
}
