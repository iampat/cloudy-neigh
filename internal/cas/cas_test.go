package cas_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/iampat/cloudy-neigh/internal/cas"
)

type backend struct {
	name string
	open func(t testing.TB) cas.Store
}

// One table runs against both backends, so a divergence fails a test.
func backends() []backend {
	return []backend{
		{"memory", func(testing.TB) cas.Store { return cas.NewMemory() }},
		{"disk", func(t testing.TB) cas.Store {
			t.Helper()
			s, err := cas.OpenDisk(t.TempDir())
			if err != nil {
				t.Fatalf("OpenDisk: %v", err)
			}
			return s
		}},
	}
}

func TestStore(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, s cas.Store)
	}{
		{"put then get", func(t *testing.T, s cas.Store) {
			want := []byte("the cat")
			d, err := s.Put(want)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if d != cas.Digest(sha256.Sum256(want)) {
				t.Errorf("Put digest = %s, want the SHA-256 of the input", d)
			}
			got, err := s.Get(d)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("Get = %q, want %q", got, want)
			}
		}},
		{"put is idempotent", func(t *testing.T, s cas.Store) {
			first, err := s.Put([]byte("same"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			second, err := s.Put([]byte("same"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if first != second {
				t.Errorf("digests %s and %s differ for the same content", first, second)
			}
		}},
		{"get of an unknown digest", func(t *testing.T, s cas.Store) {
			_, err := s.Get(cas.Digest(sha256.Sum256([]byte("never stored"))))
			if !errors.Is(err, cas.ErrNotFound) {
				t.Errorf("Get error = %v, want cas.ErrNotFound", err)
			}
		}},
		{"root is unset until it is set", func(t *testing.T, s cas.Store) {
			if _, ok, err := s.Root(); err != nil || ok {
				t.Fatalf("Root = (_, %v, %v), want (_, false, nil)", ok, err)
			}
			want, err := s.Put([]byte("manifest"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := s.SetRoot(want); err != nil {
				t.Fatalf("SetRoot: %v", err)
			}
			got, ok, err := s.Root()
			if err != nil || !ok {
				t.Fatalf("Root = (_, %v, %v), want (_, true, nil)", ok, err)
			}
			if got != want {
				t.Errorf("Root = %s, want %s", got, want)
			}
		}},
		{"the store does not alias caller memory", func(t *testing.T, s cas.Store) {
			data := []byte("original")
			d, err := s.Put(data)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			copy(data, "mutated!")

			got, err := s.Get(d)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(got) != "original" {
				t.Errorf("Get = %q after the caller mutated its slice, want %q", got, "original")
			}
			copy(got, "mutated!")
			again, err := s.Get(d)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(again) != "original" {
				t.Errorf("Get = %q after the caller mutated the result, want %q", again, "original")
			}
		}},
	}

	for _, b := range backends() {
		for _, tt := range tests {
			t.Run(b.name+"/"+tt.name, func(t *testing.T) {
				tt.run(t, b.open(t))
			})
		}
	}
}

func TestDiskSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	first, err := cas.OpenDisk(dir)
	if err != nil {
		t.Fatalf("OpenDisk: %v", err)
	}
	want := []byte("durable")
	d, err := first.Put(want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := first.SetRoot(d); err != nil {
		t.Fatalf("SetRoot: %v", err)
	}

	second, err := cas.OpenDisk(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := second.Get(d)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get after reopen = %q, want %q", got, want)
	}
	root, ok, err := second.Root()
	if err != nil || !ok || root != d {
		t.Errorf("Root after reopen = (%s, %v, %v), want (%s, true, nil)", root, ok, err, d)
	}
}

func BenchmarkPut(b *testing.B) {
	data := bytes.Repeat([]byte("x"), 4096)
	for _, backend := range backends() {
		b.Run(backend.name, func(b *testing.B) {
			s := backend.open(b)
			for i := 0; b.Loop(); i++ {
				binary.LittleEndian.PutUint64(data, uint64(i))
				if _, err := s.Put(data); err != nil {
					b.Fatalf("Put: %v", err)
				}
			}
		})
	}
}
