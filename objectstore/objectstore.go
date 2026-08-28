package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
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

type Condition struct {
	Absent          bool
	GenerationMatch string
}

func (c Condition) validate(key string) error {
	if c.Absent && c.GenerationMatch != "" {
		return fmt.Errorf("objectstore: key %q: Condition sets both Absent and GenerationMatch", key)
	}
	return nil
}

type Store struct {
	d driver
}

func (s *Store) Close() error {
	return s.d.Close()
}

func (s *Store) Head(ctx context.Context, key string) (Object, error) {
	return s.d.head(ctx, key)
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.d.get(ctx, key)
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error) {
	if err := cond.validate(key); err != nil {
		return "", err
	}
	return s.d.put(ctx, key, r, cond)
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.d.delete(ctx, key)
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	return s.d.exists(ctx, key)
}

func (s *Store) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	return s.d.list(ctx, prefix, startAfter, limit)
}
