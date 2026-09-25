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
	assert.ErrorIs(t, s.Validate(), namespace.ErrInvalidName)
}

func TestScope_StorageHierarchy(t *testing.T) {
	s := namespace.Scope{Namespace: "catalog"}
	require.NoError(t, s.Validate())

	assert.Equal(t, "ns/catalog", s.Prefix())
	assert.Equal(t, "ns/catalog/wal", s.WALPrefix())
	assert.Equal(t, "ns/catalog/refs/heads/main.json", s.ManifestKey("main"))
	assert.Equal(t, "ns/catalog/refs/heads/experiment.json", s.ManifestKey("experiment"))
	assert.Equal(t, "ns/catalog/segments", s.SegmentsPrefix())
	assert.Equal(t, "ns/catalog/custom", s.Path("custom"))
}

func TestKeys(t *testing.T) {
	assert.Equal(t, "ns/default/refs/heads/main.json", namespace.Scope{Namespace: "default"}.ManifestKey("main"))
	assert.Equal(t, "ns/prod/refs/heads/main.json", namespace.Scope{Namespace: "prod"}.ManifestKey("main"))
	assert.Equal(t, "ns/prod/segments/00000000000000000001.recordio", namespace.Scope{Namespace: "prod"}.SegmentKey("00000000000000000001"))
	scope := namespace.Scope{Namespace: "prod"}
	assert.Equal(t, "ns/prod/segments/00000000000000000001.recordio", scope.SegmentKey("00000000000000000001"))
}

func TestScope_ValidationErrors(t *testing.T) {
	invalidScope := namespace.Scope{Namespace: "1bad"}
	assert.ErrorIs(t, invalidScope.Validate(), namespace.ErrInvalidName)
}

func BenchmarkValidate(b *testing.B) {
	const name = "cloudy-benchmark_namespace-123"
	for b.Loop() {
		if err := namespace.ValidateName(name); err != nil {
			b.Fatal(err)
		}
	}
}
