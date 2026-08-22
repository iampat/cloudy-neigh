package condstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

var (
	ErrNotFound           = errors.New("condstore: not found")
	ErrPreconditionFailed = errors.New("condstore: precondition failed")
)

type Locker interface {
	Lock() error
	Unlock() error
}

type MutexLocker struct{ mu sync.Mutex }

func (l *MutexLocker) Lock() error   { l.mu.Lock(); return nil }
func (l *MutexLocker) Unlock() error { l.mu.Unlock(); return nil }

// flock is advisory per open file description: two goroutines sharing one
// fd both "hold" it, so the in-process mutex must serialize first.
type FlockLocker struct {
	mu sync.Mutex
	f  *os.File
}

func NewFlockLocker(path string) (*FlockLocker, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return &FlockLocker{f: f}, nil
}

func (l *FlockLocker) Lock() error {
	l.mu.Lock()
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX); err != nil {
		l.mu.Unlock()
		return err
	}
	return nil
}

func (l *FlockLocker) Unlock() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.mu.Unlock()
	return err
}

func (l *FlockLocker) Close() error { return l.f.Close() }

const genKey = "generation"

type Store struct {
	b *blob.Bucket
	l Locker
}

func New(b *blob.Bucket, l Locker) *Store { return &Store{b: b, l: l} }

func (s *Store) liveGeneration(ctx context.Context, key string) (int64, error) {
	attrs, err := s.b.Attributes(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return 0, ErrNotFound
		}
		return 0, err
	}
	g, err := strconv.ParseInt(attrs.Metadata[genKey], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("condstore: bad generation metadata for %q: %w", key, err)
	}
	return g, nil
}

func (s *Store) write(ctx context.Context, key string, r io.Reader, gen int64, ifNotExist bool) error {
	w, err := s.b.NewWriter(ctx, key, &blob.WriterOptions{
		Metadata:   map[string]string{genKey: strconv.FormatInt(gen, 10)},
		IfNotExist: ifNotExist,
	})
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		if gcerrors.Code(err) == gcerrors.FailedPrecondition {
			return ErrPreconditionFailed
		}
		return err
	}
	return nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	if err := s.l.Lock(); err != nil {
		return 0, err
	}
	defer s.l.Unlock()
	g, err := s.liveGeneration(ctx, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	next := g + 1
	if err := s.write(ctx, key, r, next, false); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) PutIfAbsent(ctx context.Context, key string, r io.Reader) (int64, error) {
	if err := s.l.Lock(); err != nil {
		return 0, err
	}
	defer s.l.Unlock()
	// IfNotExist alone is check-then-act in fileblob; safe only because
	// the lock excludes every other writer that goes through condstore.
	if err := s.write(ctx, key, r, 1, true); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Store) PutIfGenerationMatch(ctx context.Context, key string, r io.Reader, generation int64) (int64, error) {
	if err := s.l.Lock(); err != nil {
		return 0, err
	}
	defer s.l.Unlock()
	g, err := s.liveGeneration(ctx, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, ErrPreconditionFailed
		}
		return 0, err
	}
	if g != generation {
		return 0, ErrPreconditionFailed
	}
	next := g + 1
	if err := s.write(ctx, key, r, next, false); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := s.l.Lock(); err != nil {
		return nil, 0, err
	}
	defer s.l.Unlock()
	g, err := s.liveGeneration(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	r, err := s.b.NewReader(ctx, key, nil)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return r, g, nil
}
