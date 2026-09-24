package namespace_test

import (
	"context"
	"testing"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) objectstore.Store {
	t.Helper()
	s, err := objectstore.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestListBranchesDefault(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	branches, err := namespace.ListBranches(ctx, s, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"ns/default/refs/head/main"}, branches)
}

func TestBranchRegistration(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	scope := namespace.Scope{Namespace: "prod"}

	// Initially empty -> returns default branch ref
	branches, err := scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"ns/prod/refs/head/main"}, branches)

	// Add branches
	err = scope.AddBranch(ctx, s, scope.BranchRef("main"))
	require.NoError(t, err)
	err = scope.AddBranch(ctx, s, scope.BranchRef("feature-1"))
	require.NoError(t, err)
	err = scope.AddBranch(ctx, s, scope.BranchRef("feature-2"))
	require.NoError(t, err)

	branches, err = scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"ns/prod/refs/head/main",
		"ns/prod/refs/head/feature-1",
		"ns/prod/refs/head/feature-2",
	}, branches)

	// Idempotent add
	err = scope.AddBranch(ctx, s, scope.BranchRef("feature-1"))
	require.NoError(t, err)
	branches, err = scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.Len(t, branches, 3)

	// Remove branch
	err = scope.RemoveBranch(ctx, s, scope.BranchRef("feature-1"))
	require.NoError(t, err)

	branches, err = scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"ns/prod/refs/head/main",
		"ns/prod/refs/head/feature-2",
	}, branches)
}
