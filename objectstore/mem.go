package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type memEntry struct {
	data       []byte
	generation string
}

type memDriver struct {
	mu      sync.RWMutex
	objects map[string]memEntry
	genSeq  atomic.Int64
}

func newMemDriver() *memDriver {
	return &memDriver{
		objects: make(map[string]memEntry),
	}
}

func (m *memDriver) Close() error {
	return nil
}

func (m *memDriver) head(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
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

func (m *memDriver) get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("key %q: %w", key, ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(e.data)), nil
}

func (m *memDriver) exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.objects[key]
	return ok, nil
}

func (m *memDriver) delete(ctx context.Context, key string) error {
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

func (m *memDriver) put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cond.GenerationMatch != "" && !validLocalGeneration(cond.GenerationMatch) {
		return "", fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
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

	gen := localGeneration(m.genSeq.Add(1), int64(len(data)))
	m.objects[key] = memEntry{data: data, generation: gen}
	return gen, nil
}

func (m *memDriver) list(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

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
