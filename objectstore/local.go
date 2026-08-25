package objectstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gocloud.dev/blob"
)

var diskMus sync.Map

func diskMu(dir string) (*sync.Mutex, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	mu, _ := diskMus.LoadOrStore(abs, &sync.Mutex{})
	return mu.(*sync.Mutex), nil
}

type local struct {
	b  *blob.Bucket
	mu *sync.Mutex
}

func (d *local) lock() func() {
	d.mu.Lock()
	return d.mu.Unlock
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
		live, err := d.live(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		if live != cond.GenerationMatch {
			return nil, nil, errPrecondition(key)
		}
	}
	return nil, func() (string, error) { return d.live(ctx, key) }, nil
}
