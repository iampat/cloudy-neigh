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
			errTenant := namespace.ValidateTenant(tc.input)
			errNS := namespace.ValidateNamespace(tc.input)
			errBranch := namespace.ValidateBranch(tc.input)
			if tc.wantErr {
				require.Error(t, errTenant)
				assert.True(t, errors.Is(errTenant, namespace.ErrInvalidName))
				require.Error(t, errNS)
				assert.True(t, errors.Is(errNS, namespace.ErrInvalidName))
				require.Error(t, errBranch)
				assert.True(t, errors.Is(errBranch, namespace.ErrInvalidName))
			} else {
				require.NoError(t, errTenant)
				require.NoError(t, errNS)
				require.NoError(t, errBranch)
			}
		})
	}
}

func TestValidateAliases(t *testing.T) {
	assert.NoError(t, namespace.ValidateTenant("tenant1"))
	assert.ErrorIs(t, namespace.ValidateTenant("1tenant"), namespace.ErrInvalidName)

	assert.NoError(t, namespace.ValidateNamespace("ns1"))
	assert.ErrorIs(t, namespace.ValidateNamespace("1ns"), namespace.ErrInvalidName)

	assert.NoError(t, namespace.ValidateBranch("main"))
	assert.ErrorIs(t, namespace.ValidateBranch("1branch"), namespace.ErrInvalidName)
}

func TestScope_ZeroValue(t *testing.T) {
	var s namespace.Scope
	require.NoError(t, s.Validate())

	assert.Equal(t, "", s.Prefix())
	assert.Equal(t, "wal", s.WALPrefix())
	assert.Equal(t, "refs/heads/main", s.BranchRef("main"))
	assert.Equal(t, "segments", s.SegmentsPrefix())
	assert.Equal(t, "custom/path", s.Path("custom/path"))
}

func TestScope_WithTenantAndNamespace(t *testing.T) {
	s, err := namespace.NewScope(namespace.DefaultTenant, "wiki")
	require.NoError(t, err)
	require.NoError(t, s.Validate())

	assert.Equal(t, "cloudy/wiki", s.Prefix())
	assert.Equal(t, "cloudy/wiki/wal", s.WALPrefix())
	assert.Equal(t, "cloudy/wiki/refs/heads/main", s.BranchRef("main"))
	assert.Equal(t, "cloudy/wiki/segments", s.SegmentsPrefix())
	assert.Equal(t, "cloudy/wiki/custom", s.Path("custom"))
}

func TestScope_TenantOnly(t *testing.T) {
	s, err := namespace.NewScope("tenant1", "")
	require.NoError(t, err)

	assert.Equal(t, "tenant1", s.Prefix())
	assert.Equal(t, "tenant1/wal", s.WALPrefix())
	assert.Equal(t, "tenant1/refs/heads/main", s.BranchRef("main"))
}

func TestScope_NamespaceOnly(t *testing.T) {
	s, err := namespace.NewScope("", "wiki")
	require.NoError(t, err)

	assert.Equal(t, "wiki", s.Prefix())
	assert.Equal(t, "wiki/wal", s.WALPrefix())
	assert.Equal(t, "wiki/refs/heads/main", s.BranchRef("main"))
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
		if err := namespace.ValidateNamespace(name); err != nil {
			b.Fatal(err)
		}
	}
}
