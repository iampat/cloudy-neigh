package objectstore_test

import (
	"context"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen(t *testing.T) {
	ctx := context.Background()
	s, err := objectstore.Open(ctx, "mem://")
	require.NoError(t, err)
	s.Close()
	s, err = objectstore.Open(ctx, "file://"+t.TempDir()+"/bucket?create_dir=true")
	require.NoError(t, err)
	s.Close()
}

func TestOpenValidationErrors(t *testing.T) {
	ctx := context.Background()
	invalidURLs := []string{
		"file://",
		"file://?create_dir=true",
		"gs://",
		"gs:///",
		"s3://bucket/key",
		"azblob://c",
		"bogus://x",
		"relative/path",
		"://control-char\x7f",
	}
	for _, raw := range invalidURLs {
		t.Run(raw, func(t *testing.T) {
			_, err := objectstore.Open(ctx, raw)
			assert.Error(t, err)
		})
	}
}

func TestOpenFileHostFallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := objectstore.Open(ctx, "file://"+dir+"?create_dir=true")
	require.NoError(t, err)
	s.Close()
}
