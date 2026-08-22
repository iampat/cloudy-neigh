package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"gocloud.dev/blob"
)

const generationKey = "generation"

// Every writer to the bucket must go through this store. A write that
// bypasses the lock breaks both PutIfAbsent and the generation metadata.
type condStore struct {
	b     *blob.Bucket
	l     locker
	close func() error
}

func (s *condStore) Close() error {
	err := s.b.Close()
	if s.close != nil {
		if cerr := s.close(); err == nil {
			err = cerr
		}
	}
	return err
}

func (s *condStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, translate(key, err)
	}
	return r, nil
}

func (s *condStore) liveGeneration(ctx context.Context, key string) (string, error) {
	attrs, err := s.b.Attributes(ctx, key)
	if err != nil {
		return "", translate(key, err)
	}
	g := attrs.Metadata[generationKey]
	if _, err := strconv.ParseInt(g, 10, 64); err != nil {
		return "", fmt.Errorf("objectstore: key %q: corrupt generation metadata %q", key, g)
	}
	return g, nil
}

// The next generation exceeds the wall clock so that a delete and re-create
// cannot revive an old token. That holds only while the clock is monotonic
// across the processes that share the bucket.
func nextGeneration(current string) string {
	cur, _ := strconv.ParseInt(current, 10, 64)
	next := time.Now().UnixNano()
	if cur+1 > next {
		next = cur + 1
	}
	return strconv.FormatInt(next, 10)
}

func (s *condStore) write(ctx context.Context, key string, r io.Reader, generation string) error {
	w, err := s.b.NewWriter(ctx, key, &blob.WriterOptions{
		Metadata: map[string]string{generationKey: generation},
	})
	if err != nil {
		return translate(key, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return translate(key, err)
	}
	return nil
}

func (s *condStore) Put(ctx context.Context, key string, r io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.l.lock(); err != nil {
		return "", err
	}
	defer s.l.unlock()
	current, err := s.liveGeneration(ctx, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		// An unconditional overwrite repairs corrupt metadata.
		current = ""
	}
	next := nextGeneration(current)
	if err := s.write(ctx, key, r, next); err != nil {
		return "", err
	}
	return next, nil
}

func (s *condStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.l.lock(); err != nil {
		return "", err
	}
	defer s.l.unlock()
	exists, err := s.b.Exists(ctx, key)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
	}
	next := nextGeneration("")
	if err := s.write(ctx, key, r, next); err != nil {
		return "", err
	}
	return next, nil
}

func (s *condStore) PutIfGenerationMatch(ctx context.Context, key string, r io.Reader, generation string) (string, error) {
	if generation == "" {
		return "", errEmptyToken(key)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.l.lock(); err != nil {
		return "", err
	}
	defer s.l.unlock()
	live, err := s.liveGeneration(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
	}
	if err != nil {
		return "", err
	}
	if live != generation {
		return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
	}
	next := nextGeneration(live)
	if err := s.write(ctx, key, r, next); err != nil {
		return "", err
	}
	return next, nil
}

func (s *condStore) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := s.l.lock(); err != nil {
		return nil, "", err
	}
	defer s.l.unlock()
	generation, err := s.liveGeneration(ctx, key)
	if err != nil {
		return nil, "", err
	}
	r, err := s.b.NewReader(ctx, key, nil)
	if err != nil {
		return nil, "", translate(key, err)
	}
	return r, generation, nil
}

func (s *condStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.l.lock(); err != nil {
		return err
	}
	defer s.l.unlock()
	if err := s.b.Delete(ctx, key); err != nil {
		return translate(key, err)
	}
	return nil
}

func (s *condStore) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
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
		out = append(out, Object{Key: obj.Key, Size: obj.Size})
	}
}
