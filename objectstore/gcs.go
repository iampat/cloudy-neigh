package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"cloud.google.com/go/storage"
	"gocloud.dev/blob"
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/gcp"
	"golang.org/x/oauth2"
)

var errNotGCS = errors.New("objectstore: bucket is not backed by the GCS driver")

type gcsStore struct {
	b *blob.Bucket
}

func OpenGCS(ctx context.Context, bucket string, ts oauth2.TokenSource) (Store, error) {
	if ts == nil {
		creds, err := gcp.DefaultCredentials(ctx)
		if err != nil {
			return nil, err
		}
		ts = gcp.CredentialsTokenSource(creds)
	}
	client, err := gcp.NewHTTPClient(gcp.DefaultTransport(), ts)
	if err != nil {
		return nil, err
	}
	b, err := gcsblob.OpenBucket(ctx, client, bucket, nil)
	if err != nil {
		return nil, err
	}
	return &gcsStore{b: b}, nil
}

func (s *gcsStore) Close() error { return s.b.Close() }

func (s *gcsStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, translate(key, err)
	}
	return r, nil
}

func (s *gcsStore) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, string, error) {
	r, err := s.b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", translate(key, err)
	}
	var sr *storage.Reader
	if !r.As(&sr) {
		r.Close()
		return nil, "", errNotGCS
	}
	return r, strconv.FormatInt(sr.Attrs.Generation, 10), nil
}

// write returns the generation of the object it created, read from the
// storage.Writer after Close. generationMatch < 0 means unconditional.
func (s *gcsStore) write(ctx context.Context, key string, r io.Reader, generationMatch int64, ifAbsent bool) (string, error) {
	var sw *storage.Writer
	opts := &blob.WriterOptions{
		IfNotExist: ifAbsent,
		BeforeWrite: func(as func(any) bool) error {
			// The handle must be conditioned before the writer exists.
			// Asking for the writer materializes it, so the order of the
			// two as calls cannot flip.
			if generationMatch >= 0 {
				var objp **storage.ObjectHandle
				if !as(&objp) {
					return errNotGCS
				}
				*objp = (*objp).If(storage.Conditions{GenerationMatch: generationMatch})
			}
			if !as(&sw) {
				return errNotGCS
			}
			return nil
		},
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
	return strconv.FormatInt(sw.Attrs().Generation, 10), nil
}

func (s *gcsStore) Put(ctx context.Context, key string, r io.Reader) (string, error) {
	return s.write(ctx, key, r, -1, false)
}

func (s *gcsStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) (string, error) {
	return s.write(ctx, key, r, -1, true)
}

func (s *gcsStore) PutIfGenerationMatch(ctx context.Context, key string, r io.Reader, generation string) (string, error) {
	if generation == "" {
		return "", errEmptyToken(key)
	}
	g, err := strconv.ParseInt(generation, 10, 64)
	// GCS treats GenerationMatch 0 as no condition, so a non-positive
	// token must fail here instead of writing unconditionally.
	if err != nil || g <= 0 {
		return "", fmt.Errorf("objectstore: key %q: malformed generation token %q", key, generation)
	}
	return s.write(ctx, key, r, g, false)
}

func (s *gcsStore) Delete(ctx context.Context, key string) error {
	if err := s.b.Delete(ctx, key); err != nil {
		return translate(key, err)
	}
	return nil
}

func (s *gcsStore) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
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
		o := Object{Key: obj.Key, Size: obj.Size}
		var oa storage.ObjectAttrs
		if obj.As(&oa) {
			o.Generation = strconv.FormatInt(oa.Generation, 10)
		}
		out = append(out, o)
	}
}
