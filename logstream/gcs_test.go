package logstream_test

import (
	"context"
	"os"
	"testing"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGCS(t *testing.T) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	ctx := context.Background()
	s, err := objectstore.Open(ctx, "gs://"+bucket)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	stream := t.Name()
	prefix := "wal/" + stream + "/"
	objs, err := s.List(ctx, prefix, "", 0)
	require.NoError(t, err)
	for _, o := range objs {
		require.NoError(t, s.Delete(ctx, o.Key))
	}
	t.Cleanup(func() {
		objs, _ := s.List(ctx, prefix, "", 0)
		for _, o := range objs {
			_ = s.Delete(ctx, o.Key)
		}
	})

	log, err := logstream.New(s.Adapter(), stream)
	require.NoError(t, err)
	seq, err := log.Append(ctx, []logstream.Record{[]byte("gcs-test-record")})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), seq)
	records, err := log.Read(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, []logstream.Record{[]byte("gcs-test-record")}, records)
}
