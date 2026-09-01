package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

type gcsDriver struct {
	client *storage.Client
	bucket string
}

func (g *gcsDriver) Close() error {
	return g.client.Close()
}

func (g *gcsDriver) bkt() *storage.BucketHandle {
	return g.client.Bucket(g.bucket)
}

func (g *gcsDriver) stat(ctx context.Context, key string) (Object, error) {
	attrs, err := g.bkt().Object(key).Attrs(ctx)
	if err != nil {
		return Object{}, translateGCS(key, err)
	}
	return Object{
		Key:        key,
		Generation: strconv.FormatInt(attrs.Generation, 10),
		Size:       attrs.Size,
	}, nil
}

func (g *gcsDriver) get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	r, err := g.bkt().Object(key).NewReader(ctx)
	if err != nil {
		return nil, Object{}, translateGCS(key, err)
	}
	return r, Object{
		Key:        key,
		Generation: strconv.FormatInt(r.Attrs.Generation, 10),
		Size:       r.Attrs.Size,
	}, nil
}

func (g *gcsDriver) exists(ctx context.Context, key string) (bool, error) {
	_, err := g.bkt().Object(key).Attrs(ctx)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

func (g *gcsDriver) delete(ctx context.Context, key string) error {
	err := g.bkt().Object(key).Delete(ctx)
	if err != nil {
		return translateGCS(key, err)
	}
	return nil
}

func (g *gcsDriver) put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error) {
	obj := g.bkt().Object(key)
	switch {
	case cond.Absent:
		obj = obj.If(storage.Conditions{DoesNotExist: true})
	case cond.GenerationMatch != "":
		gen, err := strconv.ParseInt(cond.GenerationMatch, 10, 64)
		if err != nil || gen <= 0 {
			return "", fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
		}
		obj = obj.If(storage.Conditions{GenerationMatch: gen})
	}
	w := obj.NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", translateGCS(key, err)
	}
	return strconv.FormatInt(w.Attrs().Generation, 10), nil
}

func (g *gcsDriver) list(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	query := &storage.Query{Prefix: prefix}
	if startAfter != "" {
		query.StartOffset = startAfter
	}
	it := g.bkt().Objects(ctx, query)
	var out []Object
	for {
		if limit > 0 && len(out) == limit {
			return out, nil
		}
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if attrs.Name <= startAfter {
			continue
		}
		out = append(out, Object{
			Key:        attrs.Name,
			Generation: strconv.FormatInt(attrs.Generation, 10),
			Size:       attrs.Size,
		})
	}
}

func translateGCS(key string, err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("key %q: %w", key, ErrNotFound)
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case http.StatusNotFound:
			return fmt.Errorf("key %q: %w", key, ErrNotFound)
		case http.StatusPreconditionFailed:
			return fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
		}
	}
	return err
}
