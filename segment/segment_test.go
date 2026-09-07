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
