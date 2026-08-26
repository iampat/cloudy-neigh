package objectstore

import (
	"context"
	"fmt"
	"net/url"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
	_ "gocloud.dev/blob/gcsblob"
	_ "gocloud.dev/blob/memblob"
)

func Open(ctx context.Context, rawURL string) (*Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		q := u.Query()
		if !q.Has("metadata") {
			q.Set("metadata", "skip")
			u.RawQuery = q.Encode()
			rawURL = u.String()
		}
	}
	b, err := blob.OpenBucket(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "gs":
		return &Store{b: b, bucket: gcsBucket{}}, nil
	case "file":
		lock, err := diskMu(u.Path)
		if err != nil {
			b.Close()
			return nil, err
		}
		return &Store{b: b, bucket: &local{b: b, l: lock}}, nil
	case "mem":
		return &Store{b: b, bucket: &local{b: b, l: &diskLock{}}}, nil
	default:
		b.Close()
		return nil, fmt.Errorf("objectstore: unsupported scheme %q in %q (supported: file, gs, mem)", u.Scheme, rawURL)
	}
}
