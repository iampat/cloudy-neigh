package objectstore

import (
	"os"
	"sync"
	"syscall"
)

type locker interface {
	lock() error
	unlock() error
}

type mutexLocker struct {
	mu sync.Mutex
}

func (l *mutexLocker) lock() error   { l.mu.Lock(); return nil }
func (l *mutexLocker) unlock() error { l.mu.Unlock(); return nil }

// flock is advisory per open file description, so goroutines sharing the
// descriptor all hold it at once. The mutex serializes them first.
type flockLocker struct {
	mu sync.Mutex
	f  *os.File
}

func newFlockLocker(path string) (*flockLocker, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return &flockLocker{f: f}, nil
}

func (l *flockLocker) lock() error {
	l.mu.Lock()
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX); err != nil {
		l.mu.Unlock()
		return err
	}
	return nil
}

func (l *flockLocker) unlock() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.mu.Unlock()
	return err
}

// The lock file is never unlinked: a fresh opener would lock the new inode
// while an old holder still holds the removed one.
func (l *flockLocker) close() error { return l.f.Close() }
