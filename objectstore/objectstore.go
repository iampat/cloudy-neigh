package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

var (
	ErrNotFound           = errors.New("objectstore: not found")
	ErrPreconditionFailed = errors.New("objectstore: precondition failed")
)

type Object struct {
	Key        string
	Generation string
	Size       int64
}

// Condition states what the live object must satisfy for a write to apply.
// The zero Condition, like a nil *Condition, writes unconditionally.
type Condition struct {
	Absent          bool
	GenerationMatch string
}

func (c *Condition) validate(key string) error {
	if c != nil && c.Absent && c.GenerationMatch != "" {
		return fmt.Errorf("objectstore: key %q: Condition sets both Absent and GenerationMatch", key)
	}
	return nil
}

type Store struct {
	b      *blob.Bucket
	bucket bucket
}

func (s *Store) Close() error {
	return s.b.Close()
}

// Get takes no lock on any backend: the generation rides the reader itself.
// Under a concurrent overwrite on file:// the token can lag the bytes by one
// generation, which only makes a CAS with it fail and retry -- the reverse
// pairing, which would lose an update, cannot occur.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, string, error) {
	r, err := s.b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", translate(key, err)
	}
	generation, err := s.bucket.generation(r)
	if err != nil {
		r.Close()
		return nil, "", err
	}
	return r, generation, nil
}

// Put writes r to key and returns the generation it created. A nil cond
// overwrites unconditionally.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, cond *Condition) (string, error) {
	if err := cond.validate(key); err != nil {
		return "", err
	}
	if cond != nil && *cond == (Condition{}) {
		cond = nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	defer s.bucket.lock()()
	opts, generation, err := s.bucket.writeOptions(ctx, key, cond)
	if err != nil {
		return "", err
	}
	w, err := s.b.NewWriter(ctx, key, opts)
	if err != nil {
		return "", translate(key, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", translate(key, err)
	}
	return generation()
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer s.bucket.lock()()
	if err := s.b.Delete(ctx, key); err != nil {
		return translate(key, err)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	var out []Object
	it := s.b.List(&blob.ListOptions{Prefix: prefix})
	for {
		if limit > 0 && len(out) == limit {
			return out, nil
		}
		obj, err := it.Next(ctx)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if obj.Key <= startAfter {
			continue
		}
		out = append(out, Object{
			Key:        obj.Key,
			Generation: s.bucket.listGeneration(obj),
			Size:       obj.Size,
		})
	}
}

func translate(key string, err error) error {
	switch gcerrors.Code(err) {
	case gcerrors.NotFound:
		return fmt.Errorf("key %q: %w", key, ErrNotFound)
	case gcerrors.FailedPrecondition:
		return fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
	}
	return err
}

func errPrecondition(key string) error {
	return fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
}
