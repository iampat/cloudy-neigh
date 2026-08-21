package cas

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Put takes no lock. Every write lands in a distinct temporary file and is then
// renamed to a content-addressed name, so two writers storing the same bytes
// produce the same file and cannot conflict.
type Disk struct {
	blobs string
	tmp   string
	root  string
}

func OpenDisk(dir string) (*Disk, error) {
	d := &Disk{
		blobs: filepath.Join(dir, "blobs"),
		tmp:   filepath.Join(dir, "tmp"),
		root:  filepath.Join(dir, "root"),
	}
	for _, p := range []string{dir, d.blobs, d.tmp} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", p, err)
		}
	}

	// A crash leaves temporary files behind. Nothing can reach them, so
	// clearing them here is what stops them accumulating forever.
	entries, err := os.ReadDir(d.tmp)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", d.tmp, err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(d.tmp, e.Name())); err != nil {
			return nil, fmt.Errorf("remove stale temporary file: %w", err)
		}
	}
	return d, nil
}

func (d *Disk) Put(data []byte) (Digest, error) {
	dg := Digest(sha256.Sum256(data))
	final := filepath.Join(d.blobs, dg.String())

	_, err := os.Stat(final)
	switch {
	case err == nil:
		// Sync the directory even so: the writer that created the entry may
		// have crashed before its own sync, and a root that names this blob
		// must never reach the disk ahead of it.
		return dg, syncDir(d.blobs)
	case !errors.Is(err, fs.ErrNotExist):
		return Digest{}, fmt.Errorf("stat blob %s: %w", dg, err)
	}

	if err := d.replace(final, data); err != nil {
		return Digest{}, err
	}
	return dg, nil
}

func (d *Disk) Get(dg Digest) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(d.blobs, dg.String()))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, dg)
	}
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", dg, err)
	}

	if Digest(sha256.Sum256(data)) != dg {
		return nil, fmt.Errorf("cas: blob %s is corrupt", dg)
	}
	return data, nil
}

func (d *Disk) SetRoot(dg Digest) error {
	return d.replace(d.root, []byte(dg.String()))
}

func (d *Disk) Root() (Digest, bool, error) {
	data, err := os.ReadFile(d.root)
	if errors.Is(err, fs.ErrNotExist) {
		return Digest{}, false, nil
	}
	if err != nil {
		return Digest{}, false, fmt.Errorf("read root: %w", err)
	}
	dg, err := ParseDigest(string(data))
	if err != nil {
		return Digest{}, false, err
	}
	return dg, true, nil
}

// replace returns only once data is durable at final. A concurrent reader sees
// the old contents or the new ones.
func (d *Disk) replace(final string, data []byte) error {
	f, err := os.CreateTemp(d.tmp, "blob-")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename to %s: %w", final, err)
	}
	return syncDir(filepath.Dir(final))
}

// syncDir makes a rename into path durable. Syncing the file alone leaves the
// directory entry in the page cache, so a crash can lose a file whose contents
// already reached the platter.
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}
