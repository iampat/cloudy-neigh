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

type Store interface {
	io.Closer
	Stat(ctx context.Context, key string) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	ReadRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, Object, error)
	Put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error)
}
