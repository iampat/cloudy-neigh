package objectstore_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/require"
)

func TestDiskSharedDirSerializes(t *testing.T) {
	base := t.TempDir()
	dir := base + "/bucket"
	link := base + "/alias"
	s1 := openURL(t, "file://"+dir+"?create_dir=true")
	defer s1.Close()
	require.NoError(t, os.Symlink(dir, link))
	s2 := openURL(t, "file://"+link+"?create_dir=true")
	defer s2.Close()

	raceAbsentPut(t, []*objectstore.Store{s1, s2}, "race", 32)
}

func TestDiskReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir() + "/bucket"
	s := openURL(t, "file://"+dir+"?create_dir=true")
	gen := put(t, s, "k", "v1")
	require.NoError(t, s.Close())

	s = openURL(t, "file://"+dir+"?create_dir=true")
	defer s.Close()
	r, gen2, err := s.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v1", read(t, r))
	require.Equal(t, gen, gen2, "generation must survive a reopen")
	_, err = s.Put(ctx, "k", strings.NewReader("v2"), &objectstore.Condition{GenerationMatch: gen})
	require.NoError(t, err, "CAS after reopen")
}

func TestDiskNoAttrsSidecar(t *testing.T) {
	dir := t.TempDir() + "/bucket"
	s := openURL(t, "file://"+dir+"?create_dir=true")
	defer s.Close()
	put(t, s, "k", "v1")

	_, err := os.Stat(dir + "/k.attrs")
	require.True(t, os.IsNotExist(err), "attrs sidecar must not be created")
}
