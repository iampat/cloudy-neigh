package objectstore

import (
	"context"
	"fmt"
	"net/url"
)

func Open(ctx context.Context, rawURL string) (Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "mem":
		return OpenMem(), nil
	case "file":
		return OpenDisk(u.Path)
	case "gs":
		return OpenGCS(ctx, u.Host, nil)
	}
	return nil, fmt.Errorf("objectstore: unsupported scheme %q in %q", u.Scheme, rawURL)
}
