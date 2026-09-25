package manifest_test

import (
	"context"
	"io"
	"testing"

	"github.com/iampat/cloudy-neigh/manifest"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func newTestStore(t *testing.T) objectstore.Store {
	t.Helper()
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleManifest(seq uint64) *storagepb.BranchManifest {
	return &storagepb.BranchManifest{
		CheckpointSeq: seq,
		SchemaVersion: 1,
		Segments: []*storagepb.SegmentRef{
			{
				SegmentId: "seg-1",
				DocCount:  42,
				DocsSize:  1024,
			},
		},
	}
}

func TestReadNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, _, err := manifest.Read(ctx, s, "nonexistent")
	assert.ErrorIs(t, err, objectstore.ErrNotFound)
}

func TestWriteAbsent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	m := sampleManifest(10)
	gen, err := manifest.Write(ctx, s, "refs/heads/main.json", m, "")
	require.NoError(t, err)
	assert.NotEmpty(t, gen)

	got, gotGen, err := manifest.Read(ctx, s, "refs/heads/main.json")
	require.NoError(t, err)
	assert.Equal(t, gen, gotGen)
	assert.True(t, proto.Equal(m, got))

	rc, _, err := s.Get(ctx, "refs/heads/main.json")
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"checkpoint_seq": "10",
		"schema_version": "1",
		"segments": [{"segment_id": "seg-1", "doc_count": "42", "docs_size": "1024"}]
	}`, string(data))
}

func TestWritePreconditionFailure(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	m := sampleManifest(10)
	g1, err := manifest.Write(ctx, s, "refs/heads/main.json", m, "")
	require.NoError(t, err)

	m2 := sampleManifest(20)
	_, err = manifest.Write(ctx, s, "refs/heads/main.json", m2, "")
	assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)

	_, err = manifest.Write(ctx, s, "refs/heads/main.json", m2, g1)
	require.NoError(t, err)

	m3 := sampleManifest(30)
	_, err = manifest.Write(ctx, s, "refs/heads/main.json", m3, g1)
	assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)
}

func TestWriteNilManifest(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := manifest.Write(ctx, s, "refs/heads/main.json", nil, "")
	assert.ErrorIs(t, err, manifest.ErrNilManifest)
}
