package kvfs_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iampat/cloudy-neigh/kvfs"
	"github.com/iampat/cloudy-neigh/objectstore"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func forEachBackend(t *testing.T, fn func(t *testing.T, s objectstore.Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		s, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := objectstore.Open(context.Background(), "file://"+dir+"?create_dir=true")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

func sampleManifest(checkpointSeq uint64) *storagepb.BranchManifest {
	return &storagepb.BranchManifest{
		CheckpointSeq: checkpointSeq,
		SchemaVersion: 1,
		Segments: []*storagepb.SegmentRef{
			{
				SegmentId:    "seg_001",
				MinDocId:     1,
				MaxDocId:     100,
				DocCount:     100,
				Level:        0,
				VectorsSize:  1024,
				PostingsSize: 2048,
				DocsSize:     4096,
			},
		},
	}
}

func TestBranchOperations(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()

		m1 := sampleManifest(10)
		gen1, err := kvfs.UpdateBranch(ctx, s, "main", m1, "")
		require.NoError(t, err)
		assert.NotEmpty(t, gen1)

		resolved1, rGen1, err := kvfs.ResolveBranch(ctx, s, "main")
		require.NoError(t, err)
		assert.Equal(t, gen1, rGen1)
		assert.True(t, proto.Equal(m1, resolved1))

		forkManifest, childGen, err := kvfs.CreateBranch(ctx, s, "feature-x", "main")
		require.NoError(t, err)
		assert.NotEmpty(t, childGen)
		assert.True(t, proto.Equal(m1, forkManifest))

		resolvedChild, _, err := kvfs.ResolveBranch(ctx, s, "feature-x")
		require.NoError(t, err)
		assert.True(t, proto.Equal(m1, resolvedChild))

		_, _, err = kvfs.CreateBranch(ctx, s, "feature-x", "main")
		assert.ErrorIs(t, err, kvfs.ErrBranchAlreadyExists)

		_, _, err = kvfs.CreateBranch(ctx, s, "feature-y", "nonexistent")
		assert.Error(t, err)

		m2 := sampleManifest(20)
		gen2, err := kvfs.UpdateBranch(ctx, s, "main", m2, gen1)
		require.NoError(t, err)
		assert.NotEqual(t, gen1, gen2)

		_, err = kvfs.UpdateBranch(ctx, s, "main", m2, gen1)
		assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)

		_, err = kvfs.UpdateBranch(ctx, s, "main", nil, gen2)
		assert.ErrorIs(t, err, kvfs.ErrNilManifest)

		require.NoError(t, kvfs.DeleteBranch(ctx, s, "feature-x"))
		_, _, err = kvfs.ResolveBranch(ctx, s, "feature-x")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func TestResolveBranchNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		_, _, err := kvfs.ResolveBranch(ctx, s, "nonexistent")
		assert.ErrorIs(t, err, objectstore.ErrNotFound)
	})
}

func TestBranchNameValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		invalidNames := []string{
			"",
			"/main",
			"main/",
			"a//b",
			"123branch",
			"-branch",
			"_branch",
			"branch with spaces",
			"branch..traversal",
			"branch@tag",
		}

		m := sampleManifest(1)
		for _, name := range invalidNames {
			t.Run(name, func(t *testing.T) {
				_, _, err := kvfs.ResolveBranch(ctx, s, name)
				assert.ErrorIs(t, err, kvfs.ErrInvalidBranchName)

				_, err = kvfs.UpdateBranch(ctx, s, name, m, "")
				assert.ErrorIs(t, err, kvfs.ErrInvalidBranchName)

				_, _, err = kvfs.CreateBranch(ctx, s, name, "main")
				assert.ErrorIs(t, err, kvfs.ErrInvalidBranchName)

				err = kvfs.DeleteBranch(ctx, s, name)
				assert.ErrorIs(t, err, kvfs.ErrInvalidBranchName)
			})
		}
	})
}

func TestConcurrentBranchUpdates(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		m := sampleManifest(1)
		gen, err := kvfs.UpdateBranch(ctx, s, "main", m, "")
		require.NoError(t, err)

		const writers = 16
		var wins, losses atomic.Int32
		var wg sync.WaitGroup
		errCh := make(chan error, writers)

		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(seq uint64) {
				defer wg.Done()
				nextM := sampleManifest(seq)
				_, err := kvfs.UpdateBranch(ctx, s, "main", nextM, gen)
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, objectstore.ErrPreconditionFailed):
					losses.Add(1)
				default:
					errCh <- err
				}
			}(uint64(i + 10))
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}

		assert.Equal(t, int32(1), wins.Load())
		assert.Equal(t, int32(writers-1), losses.Load())
	})
}
