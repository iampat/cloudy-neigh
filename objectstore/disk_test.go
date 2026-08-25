package objectstore_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
)

// Two handles on one directory -- one of them through a symlink alias -- must
// share the write mutex, or racing conditional writes fall through to
// fileblob's stat-then-rename and produce several winners.
func TestDiskSharedDirSerializes(t *testing.T) {
	base := t.TempDir()
	dir := base + "/bucket"
	link := base + "/alias"
	s1 := openURL(t, "file://"+dir+"?create_dir=true")
	defer s1.Close()
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	s2 := openURL(t, "file://"+link+"?create_dir=true")
	defer s2.Close()

	raceAbsentPut(t, []*objectstore.Store{s1, s2}, "race", 32)
}

func TestDiskReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir() + "/bucket"
	s := openURL(t, "file://"+dir+"?create_dir=true")
	gen := put(t, s, "k", "v1")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openURL(t, "file://"+dir+"?create_dir=true")
	defer s.Close()
	r, gen2, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, r); got != "v1" || gen2 != gen {
		t.Fatalf("after reopen: (%q, %q), want (%q, %q)", got, gen2, "v1", gen)
	}
	if _, err := s.Put(ctx, "k", strings.NewReader("v2"), &objectstore.Condition{GenerationMatch: gen}); err != nil {
		t.Fatalf("CAS after reopen: %v", err)
	}
}
