package objectstore

import "gocloud.dev/blob/memblob"

func OpenMem() Store {
	return &condStore{b: memblob.OpenBucket(nil), l: &mutexLocker{}}
}
