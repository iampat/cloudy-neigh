package objectstore

import (
	"context"

	"gocloud.dev/blob"
)

type bucket interface {
	lock() (unlock func())
	listOptions(prefix, startAfter string) *blob.ListOptions
	writeOptions(ctx context.Context, key string, cond *Condition) (*blob.WriterOptions, func() (string, error), error)
	generation(r *blob.Reader) (string, error)
	listGeneration(o *blob.ListObject) string
}
