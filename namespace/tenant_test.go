package namespace_test

import (
	"context"
	"testing"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTenantsDefault(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tenants, err := namespace.ListTenants(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, []string{namespace.DefaultTenant}, tenants)
}

func TestTenantRegistration(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := namespace.AddTenant(ctx, s, "acme")
	require.NoError(t, err)
	err = namespace.AddTenant(ctx, s, "beta")
	require.NoError(t, err)

	tenants, err := namespace.ListTenants(ctx, s)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"acme", "beta"}, tenants)

	err = namespace.AddTenant(ctx, s, "acme")
	require.NoError(t, err)
	tenants, err = namespace.ListTenants(ctx, s)
	require.NoError(t, err)
	assert.Len(t, tenants, 2)
}
