package logstream_test

import (
	"context"
	"os"
	"testing"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
)

func TestGCS(t *testing.T) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	ctx := context.Background()
	s, err := objectstore.Open(ctx, "gs://"+bucket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	stream := t.Name()
	prefix := "wal/" + stream + "/"
	objs, err := s.List(ctx, prefix, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if err := s.Delete(ctx, o.Key); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		objs, _ := s.List(ctx, prefix, "", 0)
		for _, o := range objs {
			_ = s.Delete(ctx, o.Key)
		}
	})

	log := logstream.New(s)
	seq, err := log.Append(ctx, stream, []logstream.Record{[]byte("gcs-test-record")})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("seq = %d, want 1", seq)
	}
	records, err := log.Read(ctx, stream, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || string(records[0]) != "gcs-test-record" {
		t.Fatalf("unexpected records: %v", records)
	}
}
