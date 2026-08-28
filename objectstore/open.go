package objectstore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"cloud.google.com/go/storage"
)

func Open(ctx context.Context, rawURL string) (*Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "mem":
		return &Store{d: newMemDriver()}, nil
	case "file":
		path := u.Path
		if path == "" && u.Host != "" {
			path = u.Host
		}
		if path == "" {
			return nil, fmt.Errorf("objectstore: file URL requires a path: %q", rawURL)
		}
		if u.Query().Get("create_dir") == "true" {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return nil, err
			}
		}
		d, err := newLocalDriver(path)
		if err != nil {
			return nil, err
		}
		return &Store{d: d}, nil
	case "gs":
		bucket := u.Host
		if bucket == "" {
			bucket = strings.Trim(u.Path, "/")
		}
		if bucket == "" {
			return nil, fmt.Errorf("objectstore: gs URL requires a bucket name: %q", rawURL)
		}
		client, err := storage.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		return &Store{d: &gcsDriver{client: client, bucket: bucket}}, nil
	default:
		return nil, fmt.Errorf("objectstore: unsupported scheme %q in %q (supported: file, gs, mem)", u.Scheme, rawURL)
	}
}
