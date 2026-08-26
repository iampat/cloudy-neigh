package objectstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"cloud.google.com/go/storage"
	"gocloud.dev/blob"
)

var errNotGCS = errors.New("objectstore: bucket is not backed by GCS")

type gcsBucket struct{}

func (gcsBucket) lock() func() { return func() {} }

func (gcsBucket) generation(r *blob.Reader) (string, error) {
	var sr *storage.Reader
	if !r.As(&sr) {
		return "", errNotGCS
	}
	return strconv.FormatInt(sr.Attrs.Generation, 10), nil
}

func (gcsBucket) listGeneration(o *blob.ListObject) string {
	var oa storage.ObjectAttrs
	if !o.As(&oa) {
		return ""
	}
	return strconv.FormatInt(oa.Generation, 10)
}

func (gcsBucket) writeOptions(ctx context.Context, key string, cond *Condition) (*blob.WriterOptions, func() (string, error), error) {
	var sw *storage.Writer
	capture := func(as func(any) bool) error {
		if !as(&sw) {
			return errNotGCS
		}
		return nil
	}
	opts := &blob.WriterOptions{BeforeWrite: capture}
	generation := func() (string, error) {
		return strconv.FormatInt(sw.Attrs().Generation, 10), nil
	}
	switch {
	case cond == nil:
	case cond.Absent:
		opts.IfNotExist = true
	default:
		g, err := strconv.ParseInt(cond.GenerationMatch, 10, 64)
		// GCS treats GenerationMatch 0 as no condition, so a non-positive
		// generation must fail here instead of writing unconditionally.
		if err != nil || g <= 0 {
			return nil, nil, fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
		}
		opts.BeforeWrite = func(as func(any) bool) error {
			// Asking for the writer materializes it and freezes the
			// conditions, so capture must stay last.
			var objp **storage.ObjectHandle
			if !as(&objp) {
				return errNotGCS
			}
			*objp = (*objp).If(storage.Conditions{GenerationMatch: g})
			return capture(as)
		}
	}
	return opts, generation, nil
}
