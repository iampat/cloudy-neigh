package ingestion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ErrExists is returned when WriteIfNotExist fails because the key already exists.
var ErrExists = errors.New("object already exists")

// Store defines the basic blob storage interface needed for WAL bins.
type Store interface {
	// WriteIfNotExist writes data to key path iff path does not exist. Returns ErrExists on collision.
	WriteIfNotExist(ctx context.Context, path string, data []byte) error
	// Read fetches the raw content of path.
	Read(ctx context.Context, path string) ([]byte, error)
	// List returns keys under prefix lexicographically sorted, starting strictly after startAfter (if set).
	List(ctx context.Context, prefix string, startAfter string) ([]string, error)
}

// MemoryStore is an in-memory implementation of Store suitable for fast unit testing.
type MemoryStore struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		blobs: make(map[string][]byte),
	}
}

func (m *MemoryStore) WriteIfNotExist(ctx context.Context, path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.blobs[path]; exists {
		return ErrExists
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.blobs[path] = cp
	return nil
}

func (m *MemoryStore) Read(ctx context.Context, path string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, exists := m.blobs[path]
	if !exists {
		return nil, os.ErrNotExist
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *MemoryStore) List(ctx context.Context, prefix string, startAfter string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var keys []string
	for k := range m.blobs {
		if strings.HasPrefix(k, prefix) {
			if startAfter == "" || k > startAfter {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// FileStore is a POSIX filesystem implementation of Store.
type FileStore struct {
	rootDir string
}

func NewFileStore(rootDir string) (*FileStore, error) {
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root dir: %w", err)
	}
	return &FileStore{rootDir: rootDir}, nil
}

func (f *FileStore) WriteIfNotExist(ctx context.Context, path string, data []byte) error {
	fullPath := filepath.Join(f.rootDir, path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create dir %s: %w", dir, err)
	}

	file, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return ErrExists
		}
		return err
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func (f *FileStore) Read(ctx context.Context, path string) ([]byte, error) {
	fullPath := filepath.Join(f.rootDir, path)
	return os.ReadFile(fullPath)
}

func (f *FileStore) List(ctx context.Context, prefix string, startAfter string) ([]string, error) {
	prefixDir := filepath.Join(f.rootDir, filepath.Dir(prefix))
	basePrefix := filepath.Base(prefix)

	var result []string
	err := filepath.WalkDir(prefixDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(f.rootDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, prefix) || basePrefix == "." || basePrefix == "" {
			if startAfter == "" || rel > startAfter {
				result = append(result, rel)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}
