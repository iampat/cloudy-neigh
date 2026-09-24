package namespace_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iampat/cloudy-neigh/namespace"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry(t *testing.T) {
	t.Run("empty stores", func(t *testing.T) {
		_, err := namespace.NewRegistry(nil)
		assert.Error(t, err)

		_, err = namespace.NewRegistry(map[string]objectstore.Store{})
		assert.Error(t, err)
	})

	t.Run("invalid tenant name", func(t *testing.T) {
		store, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		defer store.Close()

		_, err = namespace.NewRegistry(map[string]objectstore.Store{
			"123-bad": store,
		})
		assert.Error(t, err)
	})

	t.Run("nil store entry", func(t *testing.T) {
		_, err := namespace.NewRegistry(map[string]objectstore.Store{
			"acme": nil,
		})
		assert.Error(t, err)
	})

	t.Run("valid stores", func(t *testing.T) {
		store1, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		store2, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)

		reg, err := namespace.NewRegistry(map[string]objectstore.Store{
			"krusty": store1,
			"acme":   store2,
		})
		require.NoError(t, err)
		defer reg.Close()

		assert.Equal(t, []string{"acme", "krusty"}, reg.Tenants())

		s, ok := reg.Store("acme")
		assert.True(t, ok)
		assert.Equal(t, store2, s)

		s, ok = reg.Store("krusty")
		assert.True(t, ok)
		assert.Equal(t, store1, s)

		_, ok = reg.Store("unknown")
		assert.False(t, ok)
	})
}

func TestLoadRegistry(t *testing.T) {
	ctx := context.Background()

	t.Run("missing file", func(t *testing.T) {
		_, err := namespace.LoadRegistry(ctx, "/path/does/not/exist.json")
		assert.Error(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "bad.json")
		require.NoError(t, os.WriteFile(tmp, []byte("not-json"), 0o600))

		_, err := namespace.LoadRegistry(ctx, tmp)
		assert.Error(t, err)
	})

	t.Run("empty tenants", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "empty.json")
		require.NoError(t, os.WriteFile(tmp, []byte(`{"tenants":{}}`), 0o600))

		_, err := namespace.LoadRegistry(ctx, tmp)
		assert.Error(t, err)
	})

	t.Run("missing storage url", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "missing_url.json")
		require.NoError(t, os.WriteFile(tmp, []byte(`{"tenants":{"acme":{}}}`), 0o600))

		_, err := namespace.LoadRegistry(ctx, tmp)
		assert.Error(t, err)
	})

	t.Run("valid configuration", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		content := fmt.Sprintf(`{
			"tenants": {
				"acme": {
					"storage_url": "file://%s"
				},
				"krusty": {
					"storage_url": "file://%s"
				}
			}
		}`, dir1, dir2)

		tmp := filepath.Join(t.TempDir(), "tenants.json")
		require.NoError(t, os.WriteFile(tmp, []byte(content), 0o600))

		reg, err := namespace.LoadRegistry(ctx, tmp)
		require.NoError(t, err)
		defer reg.Close()

		assert.Equal(t, []string{"acme", "krusty"}, reg.Tenants())

		s1, ok := reg.Store("acme")
		assert.True(t, ok)
		assert.NotNil(t, s1)

		s2, ok := reg.Store("krusty")
		assert.True(t, ok)
		assert.NotNil(t, s2)

		assert.Len(t, reg.Stores(), 2)
	})
}
