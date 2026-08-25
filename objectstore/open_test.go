package objectstore_test

import (
	"context"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
)

func TestOpen(t *testing.T) {
	ctx := context.Background()
	s, err := objectstore.Open(ctx, "mem://")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = objectstore.Open(ctx, "file://"+t.TempDir()+"/bucket?create_dir=true")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestOpenRejectsS3(t *testing.T) {
	for _, u := range []string{"s3://bucket/key", "azblob://c", "bogus://x", "relative/path"} {
		if _, err := objectstore.Open(context.Background(), u); err == nil {
			t.Errorf("Open(%q) = nil error, want a rejection", u)
		}
	}
}
