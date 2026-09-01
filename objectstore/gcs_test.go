package objectstore_test

import (
	"os"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
)

func TestGCS(t *testing.T) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	runContract(t, func(t *testing.T) objectstore.Store {
		s := openURL(t, "gs://"+bucket)
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 8, casWriters: 4, casIters: 5})
}
