package namespace_test

import (
	"context"
	"io"
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

func TestBranchRegistration(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	scope := namespace.Scope{Namespace: "prod"}

	branches, err := scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, branches)

	for _, b := range []string{"main", "feature-1", "feature-2", "feature-1"} {
		require.NoError(t, scope.AddBranch(ctx, s, b))
	}
	branches, err = scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"feature-1", "feature-2", "main"}, branches)

	require.NoError(t, scope.RemoveBranch(ctx, s, "feature-1"))
	require.NoError(t, scope.RemoveBranch(ctx, s, "missing"))
	branches, err = scope.ListBranches(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, []string{"feature-2", "main"}, branches)

	rc, _, err := s.Get(ctx, "ns/prod/branches.json")
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.JSONEq(t, `{"branches":{"main":{},"feature-2":{}}}`, string(data))
}
