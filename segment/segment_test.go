package segment_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/recordio"
	"github.com/iampat/cloudy-neigh/segment"
	"google.golang.org/protobuf/proto"
)

func TestRoundTrip(t *testing.T) {
	mutations := []*storagepb.DocumentMutation{
		{
			Branch:  "main",
			DocId:   "doc-1",
			Op:      storagepb.MutationOp_PUT,
			Payload: []byte("payload-1"),
		},
		{
			Branch: "main",
			DocId:  "doc-2",
			Op:     storagepb.MutationOp_DELETE,
		},
		{
			Branch:  "main",
			DocId:   "doc-3",
			Op:      storagepb.MutationOp_PUT,
			Payload: []byte{},
		},
		{
			Branch:  "branch-x",
			DocId:   "doc-4",
			Op:      storagepb.MutationOp_PUT,
			Payload: bytes.Repeat([]byte("large-payload-"), 100),
		},
	}

	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, m := range mutations {
		if err := w.Write(m); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	r := segment.NewReader(&buf)
	for i, want := range mutations {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("[%d] Next failed: %v", i, err)
		}
		if !proto.Equal(got, want) {
			t.Fatalf("[%d] mismatch: got %v, want %v", i, got, want)
		}
	}

	got, err := r.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v (record: %v)", err, got)
	}
}

func TestOrderPreservation(t *testing.T) {
	revisions := []*storagepb.DocumentMutation{
		{Branch: "main", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v1")},
		{Branch: "main", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v2")},
		{Branch: "main", DocId: "doc-1", Op: storagepb.MutationOp_DELETE},
		{Branch: "main", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v3")},
	}

	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, m := range revisions {
		if err := w.Write(m); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	r := segment.NewReader(&buf)
	for i, want := range revisions {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("[%d] Next failed: %v", i, err)
		}
		if !proto.Equal(got, want) {
			t.Fatalf("[%d] order mismatch: got %v, want %v", i, got, want)
		}
	}

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestTornTail(t *testing.T) {
	mutations := []*storagepb.DocumentMutation{
		{Branch: "main", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("first-record-data")},
		{Branch: "main", DocId: "doc-2", Op: storagepb.MutationOp_PUT, Payload: []byte("second-record-data")},
	}

	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, m := range mutations {
		if err := w.Write(m); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data := buf.Bytes()

	var firstRecordBuf bytes.Buffer
	wFirst := segment.NewWriter(&firstRecordBuf)
	if err := wFirst.Write(mutations[0]); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := wFirst.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	firstLen := firstRecordBuf.Len()

	tests := []struct {
		name       string
		cutoff     int
		expectDocs int
	}{
		{
			name:       "truncate mid first header",
			cutoff:     6,
			expectDocs: 0,
		},
		{
			name:       "truncate mid second header",
			cutoff:     firstLen + 6,
			expectDocs: 1,
		},
		{
			name:       "truncate mid second payload",
			cutoff:     firstLen + 15,
			expectDocs: 1,
		},
		{
			name:       "truncate mid second footer",
			cutoff:     len(data) - 2,
			expectDocs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			truncated := data[:tc.cutoff]
			r := segment.NewReader(bytes.NewReader(truncated))

			for i := 0; i < tc.expectDocs; i++ {
				got, err := r.Next()
				if err != nil {
					t.Fatalf("expected doc %d, got err: %v", i, err)
				}
				if !proto.Equal(got, mutations[i]) {
					t.Fatalf("doc mismatch: got %v, want %v", got, mutations[i])
				}
			}

			_, err := r.Next()
			if !errors.Is(err, recordio.ErrTornWrite) {
				t.Fatalf("expected ErrTornWrite, got %v", err)
			}
		})
	}
}

func TestEmpty(t *testing.T) {
	r := segment.NewReader(bytes.NewReader(nil))
	got, err := r.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v (record: %v)", err, got)
	}
}

func TestWriteErrors(t *testing.T) {
	errWriter := &failingWriter{}
	w := segment.NewWriter(errWriter)

	m := &storagepb.DocumentMutation{
		Branch: "main",
		DocId:  "doc-1",
		Op:     storagepb.MutationOp_PUT,
	}

	if err := w.Write(m); err != nil {
		t.Fatalf("unexpected error on buffered write: %v", err)
	}
	if err := w.Flush(); err == nil {
		t.Fatal("expected error on flush to failing writer, got nil")
	}
}

type failingWriter struct{}

func (f *failingWriter) Write(p []byte) (n int, err error) {
	return 0, fmt.Errorf("write error")
}

func TestCorruptProtoPayload(t *testing.T) {
	var buf bytes.Buffer
	rw := recordio.NewWriter(&buf)
	if _, _, err := rw.WriteRecord([]byte{0xff, 0xff, 0xff}); err != nil {
		t.Fatalf("WriteRecord failed: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	r := segment.NewReader(&buf)
	_, err := r.Next()
	if err == nil {
		t.Fatal("expected unmarshal error for invalid proto bytes, got nil")
	}
}

func BenchmarkWriter(b *testing.B) {
	m := &storagepb.DocumentMutation{
		Branch:  "main",
		DocId:   "doc-benchmark-1",
		Op:      storagepb.MutationOp_PUT,
		Payload: bytes.Repeat([]byte("test-payload-bytes"), 50),
	}

	for b.Loop() {
		w := segment.NewWriter(io.Discard)
		if err := w.Write(m); err != nil {
			b.Fatalf("Write failed: %v", err)
		}
		if err := w.Flush(); err != nil {
			b.Fatalf("Flush failed: %v", err)
		}
	}
}

func BenchmarkReader(b *testing.B) {
	m := &storagepb.DocumentMutation{
		Branch:  "main",
		DocId:   "doc-benchmark-1",
		Op:      storagepb.MutationOp_PUT,
		Payload: bytes.Repeat([]byte("test-payload-bytes"), 50),
	}

	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for i := 0; i < 1000; i++ {
		if err := w.Write(m); err != nil {
			b.Fatalf("Write failed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatalf("Close failed: %v", err)
	}
	data := buf.Bytes()

	for b.Loop() {
		r := segment.NewReader(bytes.NewReader(data))
		for {
			_, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				b.Fatalf("Next failed: %v", err)
			}
		}
	}
}

func TestBranchValidation(t *testing.T) {
	valid := []string{"main", "dev_1", "feature-branch", "Staging_v2-test"}
	for _, branch := range valid {
		var buf bytes.Buffer
		w := segment.NewWriter(&buf)
		m := &storagepb.DocumentMutation{
			Branch: branch,
			DocId:  "doc-1",
			Op:     storagepb.MutationOp_PUT,
		}
		if err := w.Write(m); err != nil {
			t.Errorf("expected valid branch %q, got error: %v", branch, err)
		}
	}

	invalid := []string{
		"",
		"features/search",
		"/root",
		"123num",
		"-dash",
		"_under",
		"branch space",
		"tag@1",
	}
	for _, branch := range invalid {
		var buf bytes.Buffer
		w := segment.NewWriter(&buf)
		m := &storagepb.DocumentMutation{
			Branch: branch,
			DocId:  "doc-1",
			Op:     storagepb.MutationOp_PUT,
		}
		err := w.Write(m)
		if !errors.Is(err, segment.ErrInvalidBranchName) {
			t.Errorf("expected ErrInvalidBranchName for %q, got %v", branch, err)
		}
	}

	w := segment.NewWriter(&bytes.Buffer{})
	if err := w.Write(nil); !errors.Is(err, segment.ErrNilMutation) {
		t.Errorf("expected ErrNilMutation, got %v", err)
	}
}

func TestBranchIsolation(t *testing.T) {
	mutations := []*storagepb.DocumentMutation{
		{Branch: "branch_a", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v1")},
		{Branch: "branch_b", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v1-b")},
		{Branch: "branch_a", DocId: "doc-1", Op: storagepb.MutationOp_DELETE},
		{Branch: "branch_b", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v2-b")},
	}

	var buf bytes.Buffer
	w := segment.NewWriter(&buf)
	for _, m := range mutations {
		if err := w.Write(m); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	branchState := make(map[string]map[string]*storagepb.DocumentMutation)
	r := segment.NewReader(&buf)
	for {
		m, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if _, ok := branchState[m.Branch]; !ok {
			branchState[m.Branch] = make(map[string]*storagepb.DocumentMutation)
		}
		if m.Op == storagepb.MutationOp_DELETE {
			delete(branchState[m.Branch], m.DocId)
		} else {
			branchState[m.Branch][m.DocId] = m
		}
	}

	if _, exists := branchState["branch_a"]["doc-1"]; exists {
		t.Fatal("expected doc-1 to be deleted in branch_a")
	}
	docB, exists := branchState["branch_b"]["doc-1"]
	if !exists {
		t.Fatal("expected doc-1 to exist in branch_b")
	}
	if string(docB.Payload) != "v2-b" {
		t.Fatalf("expected v2-b, got %s", string(docB.Payload))
	}
}

func TestWriterCloseCloser(t *testing.T) {
	cw := &closingWriter{}
	w := segment.NewWriter(cw)
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !cw.closed {
		t.Fatal("expected underlying closer to be closed")
	}
}

type closingWriter struct {
	closed bool
}

func (c *closingWriter) Write(p []byte) (int, error) { return len(p), nil }
func (c *closingWriter) Close() error                { c.closed = true; return nil }
