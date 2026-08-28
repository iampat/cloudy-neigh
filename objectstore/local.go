package objectstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gocloud.dev/blob"
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

type local struct {
	b            *blob.Bucket
	l            *diskLock
	nativeAbsent bool
}

func (d *local) lock() func() {
	d.l.mu.Lock()
	if d.l.dir != "" {
		if f, err := os.Open(d.l.dir); err == nil {
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err == nil {
				return func() {
					_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					_ = f.Close()
					d.l.mu.Unlock()
				}
			}
			_ = f.Close()
		}
	}
	return d.l.mu.Unlock
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

func (d *local) generation(r *blob.Reader) (string, error) {
	return localGeneration(r.ModTime().UnixNano(), r.Size()), nil
}

func (d *local) listGeneration(o *blob.ListObject) string {
	return localGeneration(o.ModTime.UnixNano(), o.Size)
}

func (d *local) live(ctx context.Context, key string) (string, error) {
	attrs, err := d.b.Attributes(ctx, key)
	if err != nil {
		if err := translate(key, err); errors.Is(err, ErrNotFound) {
			return "", errPrecondition(key)
		}
		return "", err
	}
	return localGeneration(attrs.ModTime.UnixNano(), attrs.Size), nil
}

func (d *local) writeOptions(ctx context.Context, key string, cond *Condition) (*blob.WriterOptions, func() (string, error), error) {
	live := func() (string, error) { return d.live(ctx, key) }

	if cond != nil && cond.Absent {
		if d.nativeAbsent {
			return &blob.WriterOptions{IfNotExist: true}, live, nil
		}
		exists, err := d.b.Exists(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			return nil, nil, errPrecondition(key)
		}
		return nil, live, nil
	}

	prevLive, err := d.live(ctx, key)
	if err != nil && !errors.Is(err, ErrPreconditionFailed) {
		return nil, nil, err
	}
	if cond != nil {
		if !validLocalGeneration(cond.GenerationMatch) {
			return nil, nil, fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
		}
		if prevLive != cond.GenerationMatch {
			return nil, nil, errPrecondition(key)
		}
	}
	waitPastGeneration(prevLive)
	return nil, live, nil
}

func waitPastGeneration(prev string) {
	mod, _, ok := strings.Cut(prev, "-")
	if !ok {
		return
	}
	m, err := strconv.ParseInt(mod, 16, 64)
	if err != nil {
		return
	}
	for time.Now().UnixNano() <= m+int64(2*time.Millisecond) {
		time.Sleep(time.Millisecond)
	}
}
