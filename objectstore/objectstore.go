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
	Put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error)
}

type client struct {
	d driver
}

func (c *client) Close() error {
	return c.d.Close()
}

func (c *client) Stat(ctx context.Context, key string) (Object, error) {
	return c.d.stat(ctx, key)
}

func (c *client) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	return c.d.get(ctx, key)
}

func (c *client) Put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error) {
	if err := cond.validate(key); err != nil {
		return "", err
	}
	return c.d.put(ctx, key, r, cond)
}

func (c *client) Delete(ctx context.Context, key string) error {
	return c.d.delete(ctx, key)
}

func (c *client) Exists(ctx context.Context, key string) (bool, error) {
	return c.d.exists(ctx, key)
}

func (c *client) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	return c.d.list(ctx, prefix, startAfter, limit)
}
