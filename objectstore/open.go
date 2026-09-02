package objectstore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"cloud.google.com/go/storage"
)

func Open(ctx context.Context, rawURL string) (Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "mem":
		return newMemStore(), nil
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
		return newLocalStore(path)
	case "gs":
		bucket := u.Host
		if bucket == "" {
			bucket = strings.Trim(u.Path, "/")
		}
		if bucket == "" {
			return nil, fmt.Errorf("objectstore: gs URL requires a bucket name: %q", rawURL)
		}
		clientHandle, err := storage.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		return &gcsStore{client: clientHandle, bucket: bucket}, nil
	default:
		return nil, fmt.Errorf("objectstore: unsupported scheme %q in %q (supported: file, gs, mem)", u.Scheme, rawURL)
	}
}
