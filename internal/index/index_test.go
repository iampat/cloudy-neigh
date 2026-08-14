package index_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/iampat/cloudy-neigh/internal/cas"
	"github.com/iampat/cloudy-neigh/internal/index"
	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
)

type backend struct {
	name string
	open func(t testing.TB) *index.Store
}

// One table runs against both backends, so a divergence fails a test.
func backends() []backend {
	return []backend{
		{"memory", func(t testing.TB) *index.Store { return openStore(t, cas.NewMemory()) }},
		{"disk", func(t testing.TB) *index.Store {
			t.Helper()
			blobs, err := cas.OpenDisk(t.TempDir())
			if err != nil {
				t.Fatalf("OpenDisk: %v", err)
			}
			return openStore(t, blobs)
		}},
	}
}

func openStore(t testing.TB, blobs cas.Store) *index.Store {
	t.Helper()
	s, err := index.Open(blobs)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func document(id string, attribute string) *cloudyneigh.Document {
	return &cloudyneigh.Document{
		Id: id,
		Attributes: map[string]*cloudyneigh.Value{
			"content": {Kind: &cloudyneigh.Value_Text{Text: attribute}},
		},
	}
}

func TestStore(t *testing.T) {
	tests := []struct {
		name string
		run  func(ctx context.Context, t *testing.T, s *index.Store)
	}{
		{"upsert then lookup", func(ctx context.Context, t *testing.T, s *index.Store) {
			want := document("src/index/writer.go", "package index")
			if err := s.Upsert(ctx, "repo", []*cloudyneigh.Document{want}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			got, err := s.Lookup(ctx, "repo", want.GetId())
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if !proto.Equal(got, want) {
				t.Errorf("Lookup = %v, want %v", got, want)
			}
		}},
		{"the second write of an id wins", func(ctx context.Context, t *testing.T, s *index.Store) {
			for _, content := range []string{"first", "second"} {
				if err := s.Upsert(ctx, "repo", []*cloudyneigh.Document{document("a", content)}); err != nil {
					t.Fatalf("Upsert: %v", err)
				}
			}
			got, err := s.Lookup(ctx, "repo", "a")
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if !proto.Equal(got, document("a", "second")) {
				t.Errorf("Lookup = %v, want the second write", got)
			}
		}},
		{"lookup of an unknown id", func(ctx context.Context, t *testing.T, s *index.Store) {
			if err := s.Upsert(ctx, "repo", []*cloudyneigh.Document{document("a", "x")}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			if _, err := s.Lookup(ctx, "repo", "missing"); !errors.Is(err, index.ErrNotFound) {
				t.Errorf("Lookup error = %v, want ErrNotFound", err)
			}
		}},
		{"lookup in an unknown namespace", func(ctx context.Context, t *testing.T, s *index.Store) {
			if _, err := s.Lookup(ctx, "never-written", "a"); !errors.Is(err, index.ErrNotFound) {
				t.Errorf("Lookup error = %v, want ErrNotFound", err)
			}
		}},
		{"namespaces do not collide", func(ctx context.Context, t *testing.T, s *index.Store) {
			if err := s.Upsert(ctx, "one", []*cloudyneigh.Document{document("a", "in one")}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			if err := s.Upsert(ctx, "two", []*cloudyneigh.Document{document("a", "in two")}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			got, err := s.Lookup(ctx, "one", "a")
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if !proto.Equal(got, document("a", "in one")) {
				t.Errorf("Lookup in namespace one = %v, want the document written there", got)
			}
		}},
		{"concurrent writers do not lose an update", func(ctx context.Context, t *testing.T, s *index.Store) {
			const writers = 16
			errs := make(chan error, writers)
			var wg sync.WaitGroup
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- s.Upsert(ctx, "repo", []*cloudyneigh.Document{
						document(fmt.Sprintf("doc-%d", i), "content"),
					})
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("Upsert: %v", err)
				}
			}
			// A manifest update lost to a racing writer shows up here as a
			// missing document.
			for i := range writers {
				if _, err := s.Lookup(ctx, "repo", fmt.Sprintf("doc-%d", i)); err != nil {
					t.Errorf("Lookup doc-%d: %v", i, err)
				}
			}
		}},
	}

	for _, b := range backends() {
		for _, tt := range tests {
			t.Run(b.name+"/"+tt.name, func(t *testing.T) {
				tt.run(t.Context(), t, b.open(t))
			})
		}
	}
}

func TestReopenFindsEveryDocument(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	blobs, err := cas.OpenDisk(dir)
	if err != nil {
		t.Fatalf("OpenDisk: %v", err)
	}
	want := document("src/index/writer.go", "package index")
	if err := openStore(t, blobs).Upsert(ctx, "repo", []*cloudyneigh.Document{want}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	reopened, err := cas.OpenDisk(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := openStore(t, reopened).Lookup(ctx, "repo", want.GetId())
	if err != nil {
		t.Fatalf("Lookup after reopen: %v", err)
	}
	if !proto.Equal(got, want) {
		t.Errorf("Lookup after reopen = %v, want %v", got, want)
	}
}

func TestUnwritableRootLeavesNothingVisible(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	blobs, err := cas.OpenDisk(dir)
	if err != nil {
		t.Fatalf("OpenDisk: %v", err)
	}

	failing := &rootFailure{Store: blobs, err: errors.New("disk full")}
	if err := openStore(t, failing).Upsert(ctx, "repo", []*cloudyneigh.Document{document("a", "x")}); err == nil {
		t.Fatal("Upsert succeeded, want the SetRoot failure")
	}

	reopened, err := cas.OpenDisk(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := openStore(t, reopened).Lookup(ctx, "repo", "a"); !errors.Is(err, index.ErrNotFound) {
		t.Errorf("Lookup after a failed root swap = %v, want ErrNotFound", err)
	}
}

type rootFailure struct {
	cas.Store
	err error
}

func (r *rootFailure) SetRoot(cas.Digest) error { return r.err }

// The cost of an upsert grows with the manifest, so the benchmark holds the
// namespace at one size. A namespace that grows during the run makes the figure
// depend on -benchtime.
const manifestSize = 1000

func BenchmarkUpsert(b *testing.B) {
	for _, backend := range backends() {
		b.Run(backend.name, func(b *testing.B) {
			ctx := b.Context()
			s := backend.open(b)
			seed := make([]*cloudyneigh.Document, manifestSize)
			for i := range seed {
				seed[i] = document(fmt.Sprintf("doc-%d", i), "content")
			}
			if err := s.Upsert(ctx, "repo", seed); err != nil {
				b.Fatalf("seed: %v", err)
			}
			for i := 0; b.Loop(); i++ {
				docs := []*cloudyneigh.Document{
					document(fmt.Sprintf("doc-%d", i%manifestSize), fmt.Sprintf("content-%d", i)),
				}
				if err := s.Upsert(ctx, "repo", docs); err != nil {
					b.Fatalf("Upsert: %v", err)
				}
			}
		})
	}
}

func BenchmarkLookup(b *testing.B) {
	for _, backend := range backends() {
		b.Run(backend.name, func(b *testing.B) {
			ctx := b.Context()
			s := backend.open(b)
			if err := s.Upsert(ctx, "repo", []*cloudyneigh.Document{document("a", "content")}); err != nil {
				b.Fatalf("Upsert: %v", err)
			}
			for b.Loop() {
				if _, err := s.Lookup(ctx, "repo", "a"); err != nil {
					b.Fatalf("Lookup: %v", err)
				}
			}
		})
	}
}
