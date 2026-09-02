package kvfs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	kvfspb "github.com/iampat/cloudy-neigh/proto/kvfs/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBranchOperations(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()

		m := &kvfspb.Manifest{
			LastWalSeq: 0,
			Entries: map[string]*kvfspb.ManifestEntry{
				"root.txt": {CasHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", SizeBytes: 100},
			},
		}
		rootHash, err := putManifest(ctx, s, m)
		require.NoError(t, err)

		gen1, err := updateBranch(ctx, s, "main", rootHash, "")
		require.NoError(t, err)
		assert.NotEmpty(t, gen1)

		resolvedHash, resolvedGen, err := resolveBranch(ctx, s, "main")
		require.NoError(t, err)
		assert.Equal(t, rootHash, resolvedHash)
		assert.Equal(t, gen1, resolvedGen)

		childHash, childGen, err := createBranch(ctx, s, "feature-x", "main")
		require.NoError(t, err)
		assert.Equal(t, rootHash, childHash)
		assert.NotEmpty(t, childGen)

		_, _, err = createBranch(ctx, s, "feature-x", "main")
		assert.ErrorIs(t, err, ErrBranchAlreadyExists)

		_, _, err = createBranch(ctx, s, "new-feat", "non-existent")
		assert.Error(t, err)

		m2 := &kvfspb.Manifest{LastWalSeq: 2}
		hash2, err := putManifest(ctx, s, m2)
		require.NoError(t, err)

		gen2, err := updateBranch(ctx, s, "main", hash2, gen1)
		require.NoError(t, err)
		assert.NotEqual(t, gen1, gen2)

		_, err = updateBranch(ctx, s, "main", hash2, gen1)
		assert.ErrorIs(t, err, objectstore.ErrPreconditionFailed)
	})
}

func TestResolveBranchNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		_, _, err := resolveBranch(ctx, s, "non-existent")
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

		for _, name := range invalidNames {
			t.Run(name, func(t *testing.T) {
				_, _, err := resolveBranch(ctx, s, name)
				assert.ErrorIs(t, err, ErrInvalidBranchName)

				_, err = updateBranch(ctx, s, name, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "")
				assert.ErrorIs(t, err, ErrInvalidBranchName)

				_, _, err = createBranch(ctx, s, name, "main")
				assert.ErrorIs(t, err, ErrInvalidBranchName)
			})
		}
	})
}

func TestConcurrentBranchUpdates(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s objectstore.Store) {
		ctx := context.Background()
		rootHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		initGen, err := updateBranch(ctx, s, "race-branch", rootHash, "")
		require.NoError(t, err)

		const writers = 16
		var (
			wg      sync.WaitGroup
			winners atomic.Int32
			errCh   = make(chan error, writers)
		)

		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				newHash := fmt.Sprintf("%064d", idx+1)
				_, err := updateBranch(ctx, s, "race-branch", newHash, initGen)
				if err == nil {
					winners.Add(1)
					return
				}
				if !errors.Is(err, objectstore.ErrPreconditionFailed) {
					errCh <- fmt.Errorf("unexpected error for writer %d: %w", idx, err)
				}
			}(i)
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}

		assert.Equal(t, int32(1), winners.Load(), "exactly one writer should win the generation CAS")
	})
}

func FuzzBranchValidation(f *testing.F) {
	f.Add("main")
	f.Add("feature-1")
	f.Add("feature/sub-branch")
	f.Add("123branch")
	f.Add("")
	f.Add("/")
	f.Add("a//b")
	f.Add("a/b/")
	f.Add("..")

	f.Fuzz(func(t *testing.T, branch string) {
		s, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		defer s.Close()

		ctx := context.Background()
		_, _, err = resolveBranch(ctx, s, branch)
		// Must not panic. If valid, error is ErrNotFound; if invalid, ErrInvalidBranchName.
		if err != nil {
			assert.True(t, errors.Is(err, objectstore.ErrNotFound) || errors.Is(err, ErrInvalidBranchName))
		}
	})
}
