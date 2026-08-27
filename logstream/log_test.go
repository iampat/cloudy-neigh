package logstream_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"unsafe"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/iampat/cloudy-neigh/recordio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forEachBackend(t *testing.T, fn func(t *testing.T, s *objectstore.Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		s, err := objectstore.Open(context.Background(), "mem://")
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		s, err := objectstore.Open(context.Background(), "file://"+dir+"?create_dir=true")
		require.NoError(t, err)
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
				if tc.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
				_, err = log.Read(ctx, tc.stream, 1)
				if tc.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
				_, err = log.Tail(ctx, tc.stream)
				if tc.wantErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
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
		assert.NoError(t, err)
		assert.Zero(t, tail)

		b1 := []logstream.Record{[]byte("r1"), []byte("r2")}
		seq1, err := log.Append(ctx, stream, b1)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), seq1)

		b2 := []logstream.Record{[]byte("r3")}
		seq2, err := log.Append(ctx, stream, b2)
		require.NoError(t, err)
		assert.Equal(t, uint64(2), seq2)

		tail, err = log.Tail(ctx, stream)
		assert.NoError(t, err)
		assert.Equal(t, uint64(2), tail)

		read1, err := log.Read(ctx, stream, 1)
		require.NoError(t, err)
		assert.Equal(t, []logstream.Record{[]byte("r1"), []byte("r2")}, read1)

		read2, err := log.Read(ctx, stream, 2)
		require.NoError(t, err)
		assert.Equal(t, []logstream.Record{[]byte("r3")}, read2)

		_, err = log.Read(ctx, stream, 3)
		assert.ErrorIs(t, err, logstream.ErrEndOfStream)

		_, err = log.Read(ctx, stream, 0)
		assert.Error(t, err)
	})
}

func TestAppendValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s, logstream.WithMaxRecordSize(10))
		ctx := context.Background()
		stream := "validation-stream"

		_, err := log.Append(ctx, stream, nil)
		assert.Error(t, err)

		_, err = log.Append(ctx, stream, []logstream.Record{})
		assert.Error(t, err)

		_, err = log.Append(ctx, stream, []logstream.Record{[]byte("12345678901")})
		assert.Error(t, err)

		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err = log.Append(canceledCtx, stream, []logstream.Record{[]byte("ok")})
		assert.ErrorIs(t, err, context.Canceled)
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
		require.NoError(t, err)

		read, err := log.Read(ctx, stream, seq)
		require.NoError(t, err)
		require.Len(t, read, 3)

		base := unsafe.SliceData(read[0])
		cap0 := cap(read[0])
		for i := 1; i < len(read); i++ {
			ptr := unsafe.SliceData(read[i])
			diff := uintptr(unsafe.Pointer(ptr)) - uintptr(unsafe.Pointer(base))
			assert.Less(t, diff, uintptr(cap0))
		}
	})
}

func TestTailJumpColdStart(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		log := logstream.New(s)
		ctx := context.Background()
		stream := "cold-jump"

		total := 1005
		for i := 1; i <= total; i++ {
			seq, err := log.Append(ctx, stream, []logstream.Record{[]byte(fmt.Sprintf("rec-%d", i))})
			require.NoError(t, err)
			assert.Equal(t, uint64(i), seq)
		}

		coldLog := logstream.New(s)
		tail, err := coldLog.Tail(ctx, stream)
		require.NoError(t, err)
		assert.Equal(t, uint64(total), tail)
	})
}

func TestAppendDriftRecovery(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *objectstore.Store) {
		ctx := context.Background()
		stream := "drift-stream"

		writerA := logstream.New(s)
		writerB := logstream.New(s)

		seqA1, err := writerA.Append(ctx, stream, []logstream.Record{[]byte("A1")})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), seqA1)

		for i := 2; i <= 30; i++ {
			seqB, err := writerB.Append(ctx, stream, []logstream.Record{[]byte(fmt.Sprintf("B%d", i))})
			require.NoError(t, err)
			assert.Equal(t, uint64(i), seqB)
		}

		seqA2, err := writerA.Append(ctx, stream, []logstream.Record{[]byte("A2")})
		require.NoError(t, err)
		assert.Equal(t, uint64(31), seqA2)
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
					require.NoError(t, err)
				}

				seen := make(map[uint64]bool)
				for seq := range seqCh {
					assert.False(t, seen[seq], "duplicate sequence number: %d", seq)
					seen[seq] = true
				}

				assert.Len(t, seen, totalAppends)

				for i := 1; i <= totalAppends; i++ {
					assert.True(t, seen[uint64(i)], "missing sequence number %d", i)
				}

				readerLog := shared
				if readerLog == nil {
					readerLog = logstream.New(s)
				}
				tail, err := readerLog.Tail(ctx, tc.stream)
				require.NoError(t, err)
				assert.Equal(t, uint64(totalAppends), tail)
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
		require.NoError(t, err)

		key := fmt.Sprintf("wal/%s/%020d.recordio", stream, seq)
		rc, _, err := s.Get(ctx, key)
		require.NoError(t, err)
		validBytes, err := io.ReadAll(rc)
		rc.Close()
		require.NoError(t, err)

		corruptHeader := bytes.Clone(validBytes)
		corruptHeader[9] ^= 0xFF
		require.NoError(t, s.Delete(ctx, key))
		_, err = s.Put(ctx, key, bytes.NewReader(corruptHeader), nil)
		require.NoError(t, err)

		_, err = log.Read(ctx, stream, seq)
		assert.ErrorIs(t, err, recordio.ErrHeaderCorrupted)

		corruptData := bytes.Clone(validBytes)
		corruptData[len(corruptData)-2] ^= 0xFF
		require.NoError(t, s.Delete(ctx, key))
		_, err = s.Put(ctx, key, bytes.NewReader(corruptData), nil)
		require.NoError(t, err)

		_, err = log.Read(ctx, stream, seq)
		assert.ErrorIs(t, err, recordio.ErrDataCorrupted)

		truncated := validBytes[:len(validBytes)-2]
		require.NoError(t, s.Delete(ctx, key))
		_, err = s.Put(ctx, key, bytes.NewReader(truncated), nil)
		require.NoError(t, err)

		_, err = log.Read(ctx, stream, seq)
		assert.ErrorIs(t, err, recordio.ErrTornWrite)
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
