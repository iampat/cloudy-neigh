package objectstore

import (
	"context"
	"io"
)

type driver interface {
	io.Closer
	stat(ctx context.Context, key string) (Object, error)
	get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error)
	delete(ctx context.Context, key string) error
	exists(ctx context.Context, key string) (bool, error)
	list(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error)
}
