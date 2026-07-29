package ingestion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
	"gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
	"gocloud.dev/blob/memblob"
	_ "gocloud.dev/blob/s3blob"
	"gocloud.dev/gcerrors"
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
	// Close releases bucket resources.
	Close() error
}

// GoCloudStore implements Store using Go CDK's gocloud.dev/blob.Bucket abstraction.
// This provides out-of-the-box support for AWS S3, Google Cloud Storage (GCS), Local Filesystem, and In-Memory storage.
type GoCloudStore struct {
	bucket *blob.Bucket
	mu     sync.Mutex
}

// NewGoCloudStore wraps any gocloud.dev/blob.Bucket.
func NewGoCloudStore(b *blob.Bucket) *GoCloudStore {
	return &GoCloudStore{bucket: b}
}

// OpenBucket opens a bucket from a URL string (e.g. "mem://", "file:///path/to/dir", "gs://my-bucket", "s3://my-bucket").
func OpenBucket(ctx context.Context, url string) (*GoCloudStore, error) {
	b, err := blob.OpenBucket(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to open blob bucket at %s: %w", url, err)
	}
	return NewGoCloudStore(b), nil
}

// NewMemoryStore creates an in-memory Store backed by gocloud.dev/blob/memblob.
func NewMemoryStore() *GoCloudStore {
	b := memblob.OpenBucket(nil)
	return NewGoCloudStore(b)
}

// NewFileStore creates a local filesystem Store backed by gocloud.dev/blob/fileblob.
func NewFileStore(dir string) (*GoCloudStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	b, err := fileblob.OpenBucket(dir, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open fileblob bucket at %s: %w", dir, err)
	}
	return NewGoCloudStore(b), nil
}

func (s *GoCloudStore) WriteIfNotExist(ctx context.Context, path string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	exists, err := s.bucket.Exists(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to check existence for %s: %w", path, err)
	}
	if exists {
		return ErrExists
	}

	w, err := s.bucket.NewWriter(ctx, path, nil)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		if gcerrors.Code(err) == gcerrors.AlreadyExists {
			return ErrExists
		}
		return err
	}
	return nil
}

func (s *GoCloudStore) Read(ctx context.Context, path string) ([]byte, error) {
	return s.bucket.ReadAll(ctx, path)
}

func (s *GoCloudStore) List(ctx context.Context, prefix string, startAfter string) ([]string, error) {
	iter := s.bucket.List(&blob.ListOptions{
		Prefix: prefix,
	})

	var keys []string
	for {
		obj, err := iter.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if startAfter == "" || obj.Key > startAfter {
			keys = append(keys, obj.Key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *GoCloudStore) Close() error {
	if s.bucket != nil {
		return s.bucket.Close()
	}
	return nil
}
