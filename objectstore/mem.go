package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type memEntry struct {
	data       []byte
	generation string
}

type memStore struct {
	mu      sync.Mutex
	objects map[string]memEntry
	genSeq  int64
}

func newMemStore() *memStore {
	return &memStore{
		objects: make(map[string]memEntry),
	}
}

func (m *memStore) Close() error {
	return nil
}

func (m *memStore) Stat(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.objects[key]
	if !ok {
		return Object{}, fmt.Errorf("key %q: %w", key, ErrNotFound)
	}
	return Object{
		Key:        key,
		Generation: e.generation,
		Size:       int64(len(e.data)),
	}, nil
}

func (m *memStore) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.objects[key]
	if !ok {
		return nil, Object{}, fmt.Errorf("key %q: %w", key, ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(e.data))), Object{
		Key:        key,
		Generation: e.generation,
		Size:       int64(len(e.data)),
	}, nil
}

func (m *memStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok, nil
}

func (m *memStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[key]; !ok {
		return fmt.Errorf("key %q: %w", key, ErrNotFound)
	}
	delete(m.objects, key)
	return nil
}

func (m *memStore) Put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := cond.validate(key); err != nil {
		return "", err
	}
	if cond.GenerationMatch != "" && !validLocalGeneration(cond.GenerationMatch) {
		return "", fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	e, exists := m.objects[key]
	if cond.Absent {
		if exists {
			return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
		}
	} else if cond.GenerationMatch != "" {
		if !exists || e.generation != cond.GenerationMatch {
			return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
		}
	}

	m.genSeq++
	gen := localGeneration(m.genSeq, int64(len(data)))
	m.objects[key] = memEntry{data: data, generation: gen}
	return gen, nil
}

func (m *memStore) List(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) && k > startAfter {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}

	out := make([]Object, len(keys))
	for i, k := range keys {
		e := m.objects[k]
		out[i] = Object{
			Key:        k,
			Generation: e.generation,
			Size:       int64(len(e.data)),
		}
	}
	return out, nil
}
