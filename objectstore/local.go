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
	dir     string
	mu      sync.Mutex
	lastMod int64
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
	b *blob.Bucket
	l *diskLock
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
	prevLive, _ := d.live(ctx, key)
	if prevLive != "" {
		if mod, _, ok := strings.Cut(prevLive, "-"); ok {
			if m, err := strconv.ParseInt(mod, 16, 64); err == nil && m > d.l.lastMod {
				d.l.lastMod = m
			}
		}
	}
	for now := time.Now().UnixNano(); now <= d.l.lastMod+int64(2*time.Millisecond); now = time.Now().UnixNano() {
		time.Sleep(time.Millisecond)
	}
	d.l.lastMod = time.Now().UnixNano()
	switch {
	case cond == nil:
	case cond.Absent:
		exists, err := d.b.Exists(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			return nil, nil, errPrecondition(key)
		}
	default:
		if !validLocalGeneration(cond.GenerationMatch) {
			return nil, nil, fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
		}
		if prevLive != cond.GenerationMatch {
			return nil, nil, errPrecondition(key)
		}
	}
	return nil, func() (string, error) {
		live, err := d.live(ctx, key)
		if err != nil {
			return "", err
		}
		if mod, _, ok := strings.Cut(live, "-"); ok {
			if m, err := strconv.ParseInt(mod, 16, 64); err == nil && m > d.l.lastMod {
				d.l.lastMod = m
			}
		}
		return live, nil
	}, nil
}
