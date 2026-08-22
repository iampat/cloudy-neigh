package objectstore

import (
	"path/filepath"

	"gocloud.dev/blob/fileblob"
)

func OpenDisk(dir string) (Store, error) {
	b, err := fileblob.OpenBucket(dir, &fileblob.Options{CreateDir: true})
	if err != nil {
		return nil, err
	}
	// The lock file sits beside the bucket directory. Inside it, the file
	// would surface as a key in List.
	fl, err := newFlockLocker(filepath.Clean(dir) + ".lock")
	if err != nil {
		b.Close()
		return nil, err
	}
	return &condStore{b: b, l: fl, close: fl.close}, nil
}
