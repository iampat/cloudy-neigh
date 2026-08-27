package logstream_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"unsafe"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/iampat/cloudy-neigh/recordio"
)

func forEachBackend(t *testing.T, fn func(t *testing.T, s *objectstore.Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		s, err := objectstore.Open(context.Background(), "mem://")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := objectstore.Open(context.Background(), "file://"+dir+"?create_dir=true")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

func TestStreamValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s)
		ctx := context.Background()

		tests := []struct {
			name    string
			stream  string
			wantErr bool
		}{
			{name: "valid simple", stream: "main", wantErr: false},
			{name: "valid hyphen underscore", stream: "orders-topic_1", wantErr: false},
			{name: "valid single char", stream: "a", wantErr: false},
			{name: "valid digits", stream: "0123", wantErr: false},
			{name: "empty", stream: "", wantErr: true},
			{name: "slash", stream: "a/b", wantErr: true},
			{name: "leading slash", stream: "/main", wantErr: true},
			{name: "trailing slash", stream: "main/", wantErr: true},
			{name: "dot", stream: ".", wantErr: true},
			{name: "dot dot", stream: "..", wantErr: true},
			{name: "space", stream: "a b", wantErr: true},
			{name: "special char", stream: "a@b", wantErr: true},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, err := log.Append(ctx, tc.stream, []logstream.Record{[]byte("val")})
				if (err != nil) != tc.wantErr {
					t.Errorf("Append(%q) err = %v, wantErr %v", tc.stream, err, tc.wantErr)
				}
				_, err = log.Read(ctx, tc.stream, 1)
				if (err != nil) != tc.wantErr && (!tc.wantErr || !errors.Is(err, logstream.ErrEndOfStream)) {
					t.Errorf("Read(%q) err = %v, wantErr %v", tc.stream, err, tc.wantErr)
				}
				_, err = log.Tail(ctx, tc.stream)
				if (err != nil) != tc.wantErr {
					t.Errorf("Tail(%q) err = %v, wantErr %v", tc.stream, err, tc.wantErr)
				}
			})
		}
	})
}

func TestAppendAndRead(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s)
		ctx := context.Background()
		stream := "test-stream"

		tail, err := log.Tail(ctx, stream)
		if err != nil {
			t.Fatal(err)
		}
		if tail != 0 {
			t.Fatalf("tail on empty stream = %d, want 0", tail)
		}

		b1 := []logstream.Record{[]byte("r1"), []byte("r2")}
		seq1, err := log.Append(ctx, stream, b1)
		if err != nil {
			t.Fatal(err)
		}
		if seq1 != 1 {
			t.Fatalf("seq1 = %d, want 1", seq1)
		}

		b2 := []logstream.Record{[]byte("r3")}
		seq2, err := log.Append(ctx, stream, b2)
		if err != nil {
			t.Fatal(err)
		}
		if seq2 != 2 {
			t.Fatalf("seq2 = %d, want 2", seq2)
		}

		tail, err = log.Tail(ctx, stream)
		if err != nil {
			t.Fatal(err)
		}
		if tail != 2 {
			t.Fatalf("tail = %d, want 2", tail)
		}

		read1, err := log.Read(ctx, stream, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(read1) != 2 || string(read1[0]) != "r1" || string(read1[1]) != "r2" {
			t.Fatalf("unexpected read1: %v", read1)
		}

		read2, err := log.Read(ctx, stream, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(read2) != 1 || string(read2[0]) != "r3" {
			t.Fatalf("unexpected read2: %v", read2)
		}

		_, err = log.Read(ctx, stream, 3)
		if !errors.Is(err, logstream.ErrEndOfStream) {
			t.Fatalf("Read(3) err = %v, want ErrEndOfStream", err)
		}

		_, err = log.Read(ctx, stream, 0)
		if err == nil {
			t.Fatal("Read(0) err = nil, want error")
		}
	})
}

func TestAppendValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s, logstream.WithMaxRecordSize(10))
		ctx := context.Background()
		stream := "validation-stream"

		_, err := log.Append(ctx, stream, nil)
		if err == nil {
			t.Fatal("Append empty batch err = nil, want error")
		}

		_, err = log.Append(ctx, stream, []logstream.Record{})
		if err == nil {
			t.Fatal("Append 0-len batch err = nil, want error")
		}

		_, err = log.Append(ctx, stream, []logstream.Record{[]byte("12345678901")})
		if err == nil {
			t.Fatal("Append oversized record err = nil, want error")
		}

		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err = log.Append(canceledCtx, stream, []logstream.Record{[]byte("ok")})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Append canceled ctx err = %v, want context.Canceled", err)
		}
	})
}

func TestReadSharesBackingArray(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s)
		ctx := context.Background()
		stream := "backing-stream"

		records := []logstream.Record{
			[]byte("first record payload"),
			[]byte("second record payload"),
			[]byte("third record payload"),
		}
		seq, err := log.Append(ctx, stream, records)
		if err != nil {
			t.Fatal(err)
		}

		read, err := log.Read(ctx, stream, seq)
		if err != nil {
			t.Fatal(err)
		}
		if len(read) != 3 {
			t.Fatalf("len(read) = %d, want 3", len(read))
		}

		base := unsafe.SliceData(read[0])
		cap0 := cap(read[0])
		for i := 1; i < len(read); i++ {
			ptr := unsafe.SliceData(read[i])
			diff := uintptr(unsafe.Pointer(ptr)) - uintptr(unsafe.Pointer(base))
			if diff >= uintptr(cap0) {
				t.Fatalf("record %d pointer %p is outside record 0 backing array capacity %d (base %p)", i, ptr, cap0, base)
			}
		}
	})
}

func TestTailJumpColdStart(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		limit := 5
		log := logstream.New(s, logstream.WithTailListLimit(limit))
		ctx := context.Background()
		stream := "cold-jump"

		total := 23
		for i := 1; i <= total; i++ {
			seq, err := log.Append(ctx, stream, []logstream.Record{[]byte(fmt.Sprintf("rec-%d", i))})
			if err != nil {
				t.Fatalf("Append(%d): %v", i, err)
			}
			if seq != uint64(i) {
				t.Fatalf("Append seq = %d, want %d", seq, i)
			}
		}

		coldLog := logstream.New(s, logstream.WithTailListLimit(limit))
		tail, err := coldLog.Tail(ctx, stream)
		if err != nil {
			t.Fatal(err)
		}
		if tail != uint64(total) {
			t.Fatalf("Tail = %d, want %d", tail, total)
		}
	})
}

func TestAppendDriftRecovery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		ctx := context.Background()
		stream := "drift-stream"

		writerA := logstream.New(s)
		writerB := logstream.New(s)

		seqA1, err := writerA.Append(ctx, stream, []logstream.Record{[]byte("A1")})
		if err != nil {
			t.Fatal(err)
		}
		if seqA1 != 1 {
			t.Fatalf("seq = %d, want 1", seqA1)
		}

		for i := 2; i <= 30; i++ {
			seqB, err := writerB.Append(ctx, stream, []logstream.Record{[]byte(fmt.Sprintf("B%d", i))})
			if err != nil {
				t.Fatalf("writerB.Append(%d): %v", i, err)
			}
			if seqB != uint64(i) {
				t.Fatalf("writerB seq = %d, want %d", seqB, i)
			}
		}

		seqA2, err := writerA.Append(ctx, stream, []logstream.Record{[]byte("A2")})
		if err != nil {
			t.Fatalf("writerA drift Append failed: %v", err)
		}
		if seqA2 != 31 {
			t.Fatalf("seqA2 = %d, want 31", seqA2)
		}
	})
}

func TestConcurrentAppends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		tests := []struct {
			name      string
			stream    string
			writers   int
			perWriter int
			sharedLog bool
		}{
			{name: "single log", stream: "concurrent-single", writers: 8, perWriter: 5, sharedLog: true},
			{name: "multi log", stream: "concurrent-multi", writers: 4, perWriter: 4, sharedLog: false},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				totalAppends := tc.writers * tc.perWriter

				var shared *logstream.Log
				if tc.sharedLog {
					shared = logstream.New(s)
				}

				errCh := make(chan error, totalAppends)
				seqCh := make(chan uint64, totalAppends)

				var wg sync.WaitGroup
				for w := 0; w < tc.writers; w++ {
					wg.Add(1)
					go func(writerID int) {
						defer wg.Done()
						log := shared
						if log == nil {
							log = logstream.New(s)
						}
						for i := 0; i < tc.perWriter; i++ {
							rec := []byte(fmt.Sprintf("w%d-%d", writerID, i))
							seq, err := log.Append(ctx, tc.stream, []logstream.Record{rec})
							if err != nil {
								errCh <- fmt.Errorf("writer %d iter %d: %w", writerID, i, err)
								return
							}
							seqCh <- seq
						}
					}(w)
				}

				wg.Wait()
				close(errCh)
				close(seqCh)

				for err := range errCh {
					t.Fatal(err)
				}

				seen := make(map[uint64]bool)
				for seq := range seqCh {
					if seen[seq] {
						t.Fatalf("duplicate sequence number: %d", seq)
					}
					seen[seq] = true
				}

				if len(seen) != totalAppends {
					t.Fatalf("got %d unique seqs, want %d", len(seen), totalAppends)
				}

				for i := 1; i <= totalAppends; i++ {
					if !seen[uint64(i)] {
						t.Fatalf("missing sequence number %d", i)
					}
				}

				readerLog := shared
				if readerLog == nil {
					readerLog = logstream.New(s)
				}
				tail, err := readerLog.Tail(ctx, tc.stream)
				if err != nil {
					t.Fatal(err)
				}
				if tail != uint64(totalAppends) {
					t.Fatalf("tail = %d, want %d", tail, totalAppends)
				}
			})
		}
	})
}

func TestCorruptedSegment(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s)
		ctx := context.Background()
		stream := "corrupt-stream"

		seq, err := log.Append(ctx, stream, []logstream.Record{[]byte("valid payload")})
		if err != nil {
			t.Fatal(err)
		}

		key := fmt.Sprintf("wal/%s/%020d.recordio", stream, seq)
		rc, _, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		validBytes, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}

		corruptHeader := bytes.Clone(validBytes)
		corruptHeader[9] ^= 0xFF
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, key, bytes.NewReader(corruptHeader), nil); err != nil {
			t.Fatal(err)
		}

		_, err = log.Read(ctx, stream, seq)
		if !errors.Is(err, recordio.ErrHeaderCorrupted) {
			t.Fatalf("Read corrupt header err = %v, want ErrHeaderCorrupted", err)
		}

		corruptData := bytes.Clone(validBytes)
		corruptData[len(corruptData)-2] ^= 0xFF
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, key, bytes.NewReader(corruptData), nil); err != nil {
			t.Fatal(err)
		}

		_, err = log.Read(ctx, stream, seq)
		if !errors.Is(err, recordio.ErrDataCorrupted) {
			t.Fatalf("Read corrupt data err = %v, want ErrDataCorrupted", err)
		}

		truncated := validBytes[:len(validBytes)-2]
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, key, bytes.NewReader(truncated), nil); err != nil {
			t.Fatal(err)
		}

		_, err = log.Read(ctx, stream, seq)
		if !errors.Is(err, recordio.ErrTornWrite) {
			t.Fatalf("Read truncated segment err = %v, want ErrTornWrite", err)
		}
	})
}

func BenchmarkAppend(b *testing.B) {
	s, err := objectstore.Open(context.Background(), "mem://")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	log := logstream.New(s)
	ctx := context.Background()
	stream := "bench"
	rec := []logstream.Record{[]byte("benchmark payload data 128 bytes of information for testing throughput of append operation in logstream package")}

	for b.Loop() {
		if _, err := log.Append(ctx, stream, rec); err != nil {
			b.Fatal(err)
		}
	}
}
