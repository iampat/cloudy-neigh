package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

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

type Store interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, string, error)
	Put(ctx context.Context, key string, r io.Reader) (string, error)
	PutIfAbsent(ctx context.Context, key string, r io.Reader) (string, error)
	PutIfGenerationMatch(ctx context.Context, key string, r io.Reader, generation string) (string, error)
	List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error)
	Delete(ctx context.Context, key string) error
	Close() error
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

func errEmptyToken(key string) error {
	return fmt.Errorf("objectstore: key %q: empty generation token", key)
}
