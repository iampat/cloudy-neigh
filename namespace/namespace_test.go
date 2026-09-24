package namespace_test

import (
	"errors"
	"testing"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty", input: "", wantErr: true},
		{name: "start with digit", input: "1test", wantErr: true},
		{name: "start with hyphen", input: "-test", wantErr: true},
		{name: "start with underscore", input: "_test", wantErr: true},
		{name: "with slash", input: "a/b", wantErr: true},
		{name: "with space", input: "a b", wantErr: true},
		{name: "with dot", input: "a.b", wantErr: true},
		{name: "with at", input: "a@b", wantErr: true},
		{name: "with colon", input: "a:b", wantErr: true},
		{name: "single lowercase", input: "a", wantErr: false},
		{name: "single uppercase", input: "A", wantErr: false},
		{name: "alphanumeric", input: "Cloudy123", wantErr: false},
		{name: "with hyphen and underscore", input: "a-b_c-1", wantErr: false},
		{name: "default tenant", input: namespace.DefaultTenant, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := namespace.ValidateName(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, namespace.ErrInvalidName))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	assert.NoError(t, namespace.ValidateName("tenant1"))
	assert.ErrorIs(t, namespace.ValidateName("1tenant"), namespace.ErrInvalidName)

	assert.NoError(t, namespace.ValidateName("ns1"))
	assert.ErrorIs(t, namespace.ValidateName("1ns"), namespace.ErrInvalidName)

	assert.NoError(t, namespace.ValidateName("main"))
	assert.ErrorIs(t, namespace.ValidateName("1branch"), namespace.ErrInvalidName)
}

func TestScope_ZeroValue(t *testing.T) {
	var s namespace.Scope
	require.NoError(t, s.Validate())

	assert.Equal(t, "cloudy/ns/default", s.Prefix())
	assert.Equal(t, "cloudy/ns.json", s.CatalogPath())
	assert.Equal(t, "cloudy/ns/default/wal", s.WALPrefix())
	assert.Equal(t, "cloudy/ns/default/refs/head/main", s.BranchRef("main"))
	assert.Equal(t, "cloudy/ns/default/segments", s.SegmentsPrefix())
	assert.Equal(t, "cloudy/ns/default/custom/path", s.Path("custom/path"))
}

func TestScope_StorageHierarchy(t *testing.T) {
	s, err := namespace.NewScope("acme-corp", "catalog")
	require.NoError(t, err)
	require.NoError(t, s.Validate())

	assert.Equal(t, "acme-corp/ns/catalog", s.Prefix())
	assert.Equal(t, "acme-corp/ns.json", s.CatalogPath())
	assert.Equal(t, "acme-corp/ns/catalog/wal", s.WALPrefix())
	assert.Equal(t, "acme-corp/ns/catalog/refs/head/main", s.BranchRef("main"))
	assert.Equal(t, "acme-corp/ns/catalog/refs/head/experiment", s.BranchRef("experiment"))
	assert.Equal(t, "acme-corp/ns/catalog/segments", s.SegmentsPrefix())
	assert.Equal(t, "acme-corp/ns/catalog/custom", s.Path("custom"))
}

func TestScope_TenantOnly(t *testing.T) {
	s, err := namespace.NewScope("tenant1", "")
	require.NoError(t, err)

	assert.Equal(t, "tenant1/ns/default", s.Prefix())
	assert.Equal(t, "tenant1/ns.json", s.CatalogPath())
	assert.Equal(t, "tenant1/ns/default/wal", s.WALPrefix())
	assert.Equal(t, "tenant1/ns/default/refs/head/main", s.BranchRef("main"))
}

func TestScope_NamespaceOnly(t *testing.T) {
	s, err := namespace.NewScope("", "wiki")
	require.NoError(t, err)

	assert.Equal(t, "cloudy/ns/wiki", s.Prefix())
	assert.Equal(t, "cloudy/ns.json", s.CatalogPath())
	assert.Equal(t, "cloudy/ns/wiki/wal", s.WALPrefix())
	assert.Equal(t, "cloudy/ns/wiki/refs/head/main", s.BranchRef("main"))
}

func TestBranchRef(t *testing.T) {
	assert.Equal(t, "cloudy/ns/default/refs/head/main", namespace.BranchRef("", "", ""))
	assert.Equal(t, "cloudy/ns/default/refs/head/main", namespace.BranchRef("cloudy", "default", "main"))
	assert.Equal(t, "acme/ns/prod/refs/head/main", namespace.BranchRef("acme", "prod", "main"))
	assert.Equal(t, "cloudy/ns/prod/refs/head/main", namespace.BranchRef("", "prod", "main"))
	assert.Equal(t, "acme/ns/prod/segments/00000000000000000001.recordio", namespace.SegmentKey("acme", "prod", "00000000000000000001"))
	scope, _ := namespace.ScopeFromRef("acme/ns/prod/refs/head/main")
	assert.Equal(t, "acme/ns/prod/segments/00000000000000000001.recordio", scope.SegmentKey("00000000000000000001"))
	assert.Equal(t, "acme/ns/prod/branches.json", namespace.BranchesPath("acme", "prod"))
}

func TestScope_ValidationErrors(t *testing.T) {
	_, err := namespace.NewScope("1bad", "wiki")
	assert.ErrorIs(t, err, namespace.ErrInvalidName)

	_, err = namespace.NewScope("tenant", "1bad")
	assert.ErrorIs(t, err, namespace.ErrInvalidName)

	invalidScope := namespace.Scope{Tenant: "1bad", Namespace: "wiki"}
	assert.ErrorIs(t, invalidScope.Validate(), namespace.ErrInvalidName)

	invalidScope2 := namespace.Scope{Tenant: "tenant", Namespace: "1bad"}
	assert.ErrorIs(t, invalidScope2.Validate(), namespace.ErrInvalidName)
}

func BenchmarkValidate(b *testing.B) {
	const name = "cloudy-benchmark_namespace-123"
	for b.Loop() {
		if err := namespace.ValidateName(name); err != nil {
			b.Fatal(err)
		}
	}
}
