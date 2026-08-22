package objectstore_test

import (
	"context"
	"os"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	"golang.org/x/oauth2"
)

func TestGCS(t *testing.T) {
	bucket := os.Getenv("OBJECTSTORE_TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("OBJECTSTORE_TEST_GCS_BUCKET is not set")
	}
	var ts oauth2.TokenSource
	if tok := os.Getenv("OBJECTSTORE_TEST_GCS_TOKEN"); tok != "" {
		ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})
	}
	runContract(t, func(t *testing.T) objectstore.Store {
		s, err := objectstore.OpenGCS(context.Background(), bucket, ts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}, contractConfig{raceWriters: 8, casWriters: 4, casIters: 5, listGeneration: true})
}
