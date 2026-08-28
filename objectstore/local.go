package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type diskLock struct {
	dir string
	mu  sync.Mutex
}

var diskMus sync.Map

func diskMu(dir string) (*diskLock, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	val, _ := diskMus.LoadOrStore(abs, &diskLock{dir: abs})
	return val.(*diskLock), nil
}

func (l *diskLock) lock() func() {
	l.mu.Lock()
	if l.dir != "" {
		if f, err := os.Open(l.dir); err == nil {
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err == nil {
				return func() {
					_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					_ = f.Close()
					l.mu.Unlock()
				}
			}
			_ = f.Close()
		}
	}
	return l.mu.Unlock
}

func localGeneration(modTime int64, size int64) string {
	return strconv.FormatInt(modTime, 16) + "-" + strconv.FormatInt(size, 16)
}

func validLocalGeneration(g string) bool {
	mod, size, ok := strings.Cut(g, "-")
	if !ok {
		return false
	}
	if _, err := strconv.ParseInt(mod, 16, 64); err != nil {
		return false
	}
	_, err := strconv.ParseInt(size, 16, 64)
	return err == nil
}

var lastLocalModTime atomic.Int64

func advanceLocalModTime(ns int64) {
	for {
		last := lastLocalModTime.Load()
		if ns <= last {
			return
		}
		if lastLocalModTime.CompareAndSwap(last, ns) {
			return
		}
	}
}

func nextLocalModTime() time.Time {
	for {
		now := time.Now().UnixNano()
		last := lastLocalModTime.Load()
		next := now
		if next <= last {
			next = last + 1
		}
		if lastLocalModTime.CompareAndSwap(last, next) {
			return time.Unix(0, next)
		}
	}
}

type localDriver struct {
	dir string
	l   *diskLock
}

func newLocalDriver(dir string) (*localDriver, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	l, err := diskMu(abs)
	if err != nil {
		return nil, err
	}
	return &localDriver{dir: abs, l: l}, nil
}

func (d *localDriver) Close() error {
	return nil
}

func (d *localDriver) path(key string) (string, error) {
	if key == "" {
		return "", errors.New("objectstore: empty key")
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("objectstore: invalid key %q", key)
	}
	return filepath.Join(d.dir, clean), nil
}

func (d *localDriver) head(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	target, err := d.path(key)
	if err != nil {
		return Object{}, err
	}
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return Object{}, fmt.Errorf("key %q: %w", key, ErrNotFound)
		}
		return Object{}, err
	}
	if fi.IsDir() {
		return Object{}, fmt.Errorf("key %q: %w", key, ErrNotFound)
	}
	return Object{
		Key:        key,
		Generation: localGeneration(fi.ModTime().UnixNano(), fi.Size()),
		Size:       fi.Size(),
	}, nil
}

func (d *localDriver) get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := d.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("key %q: %w", key, ErrNotFound)
		}
		return nil, err
	}
	return f, nil
}

func (d *localDriver) exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	target, err := d.path(key)
	if err != nil {
		return false, err
	}
	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !fi.IsDir(), nil
}

func (d *localDriver) delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := d.path(key)
	if err != nil {
		return err
	}
	unlock := d.l.lock()
	defer unlock()

	fi, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("key %q: %w", key, ErrNotFound)
		}
		return err
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	advanceLocalModTime(fi.ModTime().UnixNano())
	return nil
}

func (d *localDriver) put(ctx context.Context, key string, r io.Reader, cond Condition) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target, err := d.path(key)
	if err != nil {
		return "", err
	}
	if cond.GenerationMatch != "" && !validLocalGeneration(cond.GenerationMatch) {
		return "", fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
	}

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(parent, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if cond.Absent {
		t := nextLocalModTime()
		if err := os.Chtimes(tmpPath, t, t); err != nil {
			return "", err
		}
		if err := os.Link(tmpPath, target); err != nil {
			if errors.Is(err, os.ErrExist) || os.IsExist(err) {
				return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
			}
			return "", err
		}
		fi, err := os.Stat(target)
		if err != nil {
			return "", err
		}
		return localGeneration(fi.ModTime().UnixNano(), fi.Size()), nil
	}

	unlock := d.l.lock()
	defer unlock()

	fi, err := os.Stat(target)
	var prevGen string
	if err == nil {
		prevGen = localGeneration(fi.ModTime().UnixNano(), fi.Size())
		advanceLocalModTime(fi.ModTime().UnixNano())
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if cond.GenerationMatch != "" {
		if prevGen == "" || prevGen != cond.GenerationMatch {
			return "", fmt.Errorf("key %q: %w", key, ErrPreconditionFailed)
		}
	}

	t := nextLocalModTime()
	if err := os.Chtimes(tmpPath, t, t); err != nil {
		return "", err
	}

	if err := os.Rename(tmpPath, target); err != nil {
		return "", err
	}
	newFi, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	return localGeneration(newFi.ModTime().UnixNano(), newFi.Size()), nil
}

func (d *localDriver) list(ctx context.Context, prefix, startAfter string, limit int) ([]Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var objs []Object
	err := filepath.WalkDir(d.dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			if entry.Name() != "." && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(d.dir, p)
		if err != nil {
			return err
		}
		k := filepath.ToSlash(rel)
		if strings.HasPrefix(k, prefix) && k > startAfter {
			info, err := entry.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			objs = append(objs, Object{
				Key:        k,
				Generation: localGeneration(info.ModTime().UnixNano(), info.Size()),
				Size:       info.Size(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(objs, func(i, j int) bool {
		return objs[i].Key < objs[j].Key
	})
	if limit > 0 && len(objs) > limit {
		objs = objs[:limit]
	}
	return objs, nil
}
