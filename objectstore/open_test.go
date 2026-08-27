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

func TestOpenRejectsS3(t *testing.T) {
	for _, u := range []string{"s3://bucket/key", "azblob://c", "bogus://x", "relative/path"} {
		_, err := objectstore.Open(context.Background(), u)
		assert.Error(t, err, "Open(%q)", u)
	}
}
