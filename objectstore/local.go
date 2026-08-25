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

// diskMus hands every Store opened on one directory the same mutex, so two
// handles on one bucket cannot race each other past the conditional-write
// checks. Entries are never removed; the map is bounded by the distinct
// bucket directories a process opens. Nested bucket roots and other
// processes remain uncoordinated.
var diskMus sync.Map

func diskMu(dir string) (*sync.Mutex, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// Symlink aliases of one directory must share the mutex; on macOS even
	// os.TempDir is one (/var -> /private/var). The directory exists by now,
	// so resolution only fails on exotic paths, which keep the absolute form.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	mu, _ := diskMus.LoadOrStore(abs, &sync.Mutex{})
	return mu.(*sync.Mutex), nil
}

// local emulates conditional writes for backends whose storage has none. The
// generation is derived from (ModTime, Size), which every reader and list
// entry already carries, so reads and lists take no lock. Mutations serialize
// on one in-process mutex, which makes check-then-write atomic; a file bucket
// shared across processes is out of scope.
//
// Token uniqueness assumes the wall clock advances between successive writes
// to one key. Writes cost more than the clock tick on the supported backends
// (memory, APFS, ext4), and the mutex serializes them; filesystems with
// second-granularity timestamps are out of scope.
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

// live returns the generation of key's current object, or ErrPreconditionFailed
// when the key is absent -- a conditional write against a missing key is a
// failed precondition, not a lookup error.
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
	// The driver's own IfNotExist is not used: fileblob's is a stat-then-rename
	// race, and a losing write overwrites the winner's attrs sidecar. Every
	// check runs here instead, under the mutex.
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
