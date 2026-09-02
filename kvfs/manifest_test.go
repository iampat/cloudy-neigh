package kvfs_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPutAndGetManifest(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()

		tests := []struct {
			name     string
			manifest *kvfspb.Manifest
		}{
			{
				name:     "empty",
				manifest: &kvfspb.Manifest{},
			},
			{
				name: "single_entry",
				manifest: &kvfspb.Manifest{
					LastWalSeq: 42,
					Entries: map[string]*kvfspb.ManifestEntry{
						"docs/a.txt": {
							CasHash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
							SizeBytes: 1024,
						},
					},
				},
			},
			{
				name: "multiple_entries",
				manifest: &kvfspb.Manifest{
					LastWalSeq: 100,
					Entries: map[string]*kvfspb.ManifestEntry{
						"k1": {CasHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", SizeBytes: 10},
						"k2": {CasHash: "0000000000000000000000000000000000000000000000000000000000000000", SizeBytes: 20},
						"k3": {CasHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", SizeBytes: 30},
					},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				hash, err := kvfs.PutManifest(ctx, s, tt.manifest)
				require.NoError(t, err)
				assert.Len(t, hash, 64)

				got, err := kvfs.GetManifest(ctx, s, hash)
				require.NoError(t, err)
				assert.True(t, proto.Equal(tt.manifest, got))
			})
		}
	})
}

func TestManifestDeduplication(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		m := &kvfspb.Manifest{
			LastWalSeq: 1,
			Entries: map[string]*kvfspb.ManifestEntry{
				"k": {CasHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", SizeBytes: 5},
			},
		}

		h1, err := kvfs.PutManifest(ctx, s, m)
		require.NoError(t, err)

		h2, err := kvfs.PutManifest(ctx, s, m)
		require.NoError(t, err)
		assert.Equal(t, h1, h2)
	})
}

func TestGetManifestNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		_, err := kvfs.GetManifest(ctx, s, "0000000000000000000000000000000000000000000000000000000000000000")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func TestGetManifestInvalidHash(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		badHashes := []string{"", "short", "invalid-hash", "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855"}
		for _, hash := range badHashes {
			t.Run(hash, func(t *testing.T) {
				_, err := kvfs.GetManifest(ctx, s, hash)
				assert.ErrorIs(t, err, kvfs.ErrInvalidHash)
			})
		}
	})
}

func BenchmarkPutManifest(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	entries := make(map[string]*kvfspb.ManifestEntry, 1000)
	for i := 0; i < 1000; i++ {
		entries[fmt.Sprintf("key-%d", i)] = &kvfspb.ManifestEntry{
			CasHash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			SizeBytes: 1024,
		}
	}
	m := &kvfspb.Manifest{LastWalSeq: 1000, Entries: entries}
	b.ReportAllocs()

	for b.Loop() {
		_, err := kvfs.PutManifest(ctx, s, m)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetManifest(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()

	entries := make(map[string]*kvfspb.ManifestEntry, 1000)
	for i := 0; i < 1000; i++ {
		entries[fmt.Sprintf("key-%d", i)] = &kvfspb.ManifestEntry{
			CasHash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			SizeBytes: 1024,
		}
	}
	m := &kvfspb.Manifest{LastWalSeq: 1000, Entries: entries}
	hash, err := kvfs.PutManifest(ctx, s, m)
	require.NoError(b, err)

	b.ReportAllocs()

	for b.Loop() {
		got, err := kvfs.GetManifest(ctx, s, hash)
		if err != nil {
			b.Fatal(err)
		}
		if len(got.Entries) != 1000 {
			b.Fatalf("expected 1000 entries, got %d", len(got.Entries))
		}
	}
}

func FuzzManifestRoundTrip(f *testing.F) {
	f.Add(uint64(0), "k1", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", uint64(100))
	f.Add(uint64(10), "docs/nested/path.txt", "0000000000000000000000000000000000000000000000000000000000000000", uint64(0))

	f.Fuzz(func(t *testing.T, walSeq uint64, key, casHash string, size uint64) {
		s, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		defer s.Close()

		ctx := context.Background()
		m := &kvfspb.Manifest{
			LastWalSeq: walSeq,
			Entries: map[string]*kvfspb.ManifestEntry{
				key: {CasHash: casHash, SizeBytes: size},
			},
		}

		hash, err := kvfs.PutManifest(ctx, s, m)
		require.NoError(t, err)

		got, err := kvfs.GetManifest(ctx, s, hash)
		require.NoError(t, err)
		assert.True(t, proto.Equal(m, got))
	})
}
