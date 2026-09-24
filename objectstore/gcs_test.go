package objectstore_test

import (
	"os"
	"strings"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestGCS_TenantIsolation(t *testing.T) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	s1 := openURL(t, "gs://"+bucket+"/tenant-1")
	t.Cleanup(func() { s1.Close() })
	s2 := openURL(t, "gs://"+bucket+"/tenant-2")
	t.Cleanup(func() { s2.Close() })

	ctx := t.Context()
	_, err := s1.Put(ctx, "data.txt", strings.NewReader("tenant1"), objectstore.Condition{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s1.Delete(ctx, "data.txt") })

	exists, err := s2.Exists(ctx, "data.txt")
	require.NoError(t, err)
	assert.False(t, exists)
}
